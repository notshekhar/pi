package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/jsonstore"
)

// ~/.loop/catalog.json — loop's cacheStore: availability + ts, models.dev
// definitions + modelsTs, vendor refs, gateway customMeta, and keys pi does
// not produce (bedrockModels, chatgptModels, …) which are left untouched.

const (
	modelsSource = "https://models.dev/api.json"
	refreshTTL   = time.Hour
)

var modelProviders = []string{"xai", "anthropic", "openai", "google", "openrouter", "deepseek", "mistral", "groq", "cerebras", "vercel"}

var availabilityProviders = []string{"xai", "anthropic", "openai", "openrouter", "vercel"}

var (
	storesMu sync.Mutex
	stores   = map[string]*jsonstore.Store{}
)

func cacheStore() *jsonstore.Store {
	path := home.Path("catalog.json")
	storesMu.Lock()
	defer storesMu.Unlock()
	s, ok := stores[path]
	if !ok {
		s = jsonstore.New(path, jsonstore.Defaults("availability", map[string]any{}, "ts", 0, "models", map[string]any{}, "modelsTs", 0))
		stores[path] = s
	}
	return s
}

type cacheView struct {
	availability map[string][]string
	ts           int64
	models       map[string]Model
	modelsOrder  []string
	modelsTS     int64
	refs         map[string]Model
	customMeta   map[string]*CustomMeta
}

func readCache() cacheView {
	s := cacheStore()
	v := cacheView{availability: map[string][]string{}, models: map[string]Model{}, refs: map[string]Model{}, customMeta: map[string]*CustomMeta{}}
	s.Get("availability", &v.availability)
	s.Get("ts", &v.ts)
	s.Get("modelsTs", &v.modelsTS)
	s.Get("refs", &v.refs)
	s.Get("customMeta", &v.customMeta)
	if raw := s.Raw("models"); raw != nil {
		if o, err := jsonstore.ParseObject(raw); err == nil {
			for _, id := range o.Keys() {
				var m Model
				if o.Get(id, &m) {
					v.models[id] = m
					v.modelsOrder = append(v.modelsOrder, id)
				}
			}
		}
	}
	return v
}

// RefreshIfStale revalidates in the background once the cache is older than
// an hour (loop's stale-while-revalidate in getCatalog).
func RefreshIfStale() {
	if time.Since(time.UnixMilli(readCache().ts)) <= refreshTTL {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _, _ = Refresh(ctx)
	}()
}

var (
	refreshMu sync.Mutex
)

// Refresh is loop's refreshAvailability: custom-provider rediscovery,
// availability per provider and models.dev definitions, all written to
// catalog.json. Returns how many models are available of how many known.
func Refresh(ctx context.Context) (available, total int, err error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	h := currentHooks()

	var customMeta map[string]CustomMeta
	var wg sync.WaitGroup
	if h.RefreshCustom != nil {
		wg.Add(1)
		go func() { defer wg.Done(); customMeta = h.RefreshCustom(ctx) }()
	}
	results := make([][]string, len(availabilityProviders))
	oks := make([]bool, len(availabilityProviders))
	for i, p := range availabilityProviders {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], oks[i] = fetchAvailability(ctx, h, p) }()
	}
	var defs *jsonstore.Object
	var raw map[string]rawDevProvider
	wg.Add(1)
	go func() { defer wg.Done(); defs, raw = fetchModelDefs(ctx) }()
	wg.Wait()

	s := cacheStore()
	if customMeta == nil {
		customMeta = map[string]CustomMeta{}
	}
	_ = s.Set("customMeta", customMeta)
	availability := jsonstore.NewObject()
	for i, p := range availabilityProviders {
		if oks[i] {
			_ = availability.Set(p, results[i])
		}
	}
	_ = s.Set("availability", availability)
	_ = s.Set("ts", time.Now().UnixMilli())
	if defs != nil {
		_ = s.Set("models", defs)
		_ = s.Set("modelsTs", time.Now().UnixMilli())
		_ = s.Set("refs", vendorRefs(h, raw))
	}
	Bust()
	for _, m := range All() {
		total++
		if m.Available {
			available++
		}
	}
	return available, total, nil
}

type rawDevModel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost struct {
		Input      float64  `json:"input"`
		Output     float64  `json:"output"`
		CacheRead  float64  `json:"cache_read"`
		CacheWrite float64  `json:"cache_write"`
		Reasoning  *float64 `json:"reasoning"`
	} `json:"cost"`
	Reasoning  bool `json:"reasoning"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
}

type rawDevProvider struct {
	Models map[string]rawDevModel `json:"models"`
}

func toModelInfo(provider, rawID string, m rawDevModel) Model {
	name := m.Name
	if name == "" {
		name = rawID
	}
	mods := m.Modalities.Input
	if mods == nil {
		mods = []string{"text"}
	}
	return Model{
		ID: provider + "/" + rawID, Provider: provider, Name: name,
		Context: m.Limit.Context, MaxOut: m.Limit.Output,
		Cost:      Cost{Input: m.Cost.Input, Output: m.Cost.Output, CacheRead: m.Cost.CacheRead, CacheWrite: m.Cost.CacheWrite, Reasoning: m.Cost.Reasoning},
		Reasoning: m.Reasoning, Modalities: mods, Available: true,
	}
}

var nonChatID = regexp.MustCompile(`(?i)embed|rerank|whisper|transcrib|realtime`)

func isVercelChatModel(rawID string, output []string) bool {
	if output == nil {
		output = []string{"text"}
	}
	return contains(output, "text") && !nonChatID.MatchString(rawID)
}

// fetchModelDefs reads models.dev; nil on failure or a payload too small to
// be real, so a broken response never wipes the catalog.
func fetchModelDefs(ctx context.Context) (*jsonstore.Object, map[string]rawDevProvider) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var raw map[string]rawDevProvider
	if !getJSON(ctx, modelsSource, nil, &raw) {
		return nil, nil
	}
	out := jsonstore.NewObject()
	for _, p := range modelProviders {
		ids := sortedKeys(raw[p].Models)
		for _, rawID := range ids {
			m := raw[p].Models[rawID]
			if p == "vercel" && !isVercelChatModel(rawID, m.Modalities.Output) {
				continue
			}
			_ = out.Set(p+"/"+rawID, toModelInfo(p, rawID, m))
		}
	}
	if len(out.Keys()) < 20 {
		return nil, nil
	}
	return out, raw
}

// vendorRefs are the models.dev slices for gateway vendor prefixes in use —
// metadata inference only, never offered in a picker.
func vendorRefs(h Hooks, raw map[string]rawDevProvider) map[string]Model {
	wanted := map[string]bool{}
	if h.CustomProviders != nil {
		for _, c := range h.CustomProviders() {
			for _, m := range c.Models {
				vendor := parseGatewayModelID(m.ID).vendor
				if vendor == "" || contains(modelProviders, vendor) {
					continue
				}
				if _, ok := raw[vendor]; ok {
					wanted[vendor] = true
				}
			}
		}
	}
	out := map[string]Model{}
	for p := range wanted {
		for rawID, m := range raw[p].Models {
			out[p+"/"+rawID] = toModelInfo(p, rawID, m)
		}
	}
	return out
}

func fetchAvailability(ctx context.Context, h Hooks, provider string) ([]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	key := func(p string) string {
		if h.APIKey == nil {
			return ""
		}
		return h.APIKey(p)
	}
	var body struct {
		Data []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"data"`
	}
	var ok bool
	switch provider {
	case "xai":
		token := ""
		if h.XAIToken != nil {
			token = h.XAIToken(ctx)
		}
		if token == "" {
			token = key("xai")
		}
		if token == "" {
			return nil, false
		}
		ok = getJSON(ctx, "https://api.x.ai/v1/models", map[string]string{"Authorization": "Bearer " + token}, &body)
	case "openai":
		k := key("openai")
		if k == "" {
			return nil, false
		}
		ok = getJSON(ctx, "https://api.openai.com/v1/models", map[string]string{"Authorization": "Bearer " + k}, &body)
	case "anthropic":
		k := key("anthropic")
		if k == "" {
			return nil, false
		}
		ok = getJSON(ctx, "https://api.anthropic.com/v1/models",
			map[string]string{"x-api-key": k, "anthropic-version": "2023-06-01"}, &body)
	case "openrouter":
		ok = getJSON(ctx, "https://openrouter.ai/api/v1/models", nil, &body)
	case "vercel":
		ok = getJSON(ctx, "https://ai-gateway.vercel.sh/v1/models", nil, &body)
	}
	if !ok {
		return nil, false
	}
	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		if provider == "vercel" && m.Type != "language" {
			continue
		}
		ids = append(ids, provider+"/"+m.ID)
	}
	return ids, true
}

func getJSON(ctx context.Context, url string, headers map[string]string, into any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(into) == nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
