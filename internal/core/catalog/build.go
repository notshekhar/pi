package catalog

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// getCatalog (catalog/index.ts), minus the providers pi cannot call (ollama
// discovery, Bedrock listing, the ChatGPT plan, native agents).

// CustomModelDecl is a model a custom provider declares in auth.json.
type CustomModelDecl struct {
	ID            string
	Name          string
	ContextWindow int
	MaxOutput     int
	Cost          *PartialCost
}

// CustomProvider is what the catalog needs of a custom gateway.
type CustomProvider struct {
	Name   string
	SDK    string
	Models []CustomModelDecl
}

// CustomMeta is what a gateway reported about one of its models (cached
// under catalog.json's customMeta, keyed by full id).
type CustomMeta struct {
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	MaxOutput     int    `json:"maxOutput,omitempty"`
}

// Hooks are what the catalog asks of the auth layer, which sits above it.
type Hooks struct {
	// APIKey is the stored key for a provider, or "".
	APIKey func(provider string) string
	// XAIToken is an OAuth bearer for xAI, or "".
	XAIToken func(ctx context.Context) string
	// Authorized lists providers with stored credentials.
	Authorized func() []string
	// CustomProviders lists the configured gateways.
	CustomProviders func() []CustomProvider
	// RefreshCustom rediscovers gateways' models (persisting them) and
	// returns what each gateway said about its models.
	RefreshCustom func(ctx context.Context) map[string]CustomMeta
}

var (
	hooksMu sync.RWMutex
	hooks   Hooks
)

// Configure installs the hooks and drops the merged catalog.
func Configure(h Hooks) {
	hooksMu.Lock()
	hooks = h
	hooksMu.Unlock()
	Bust()
}

func currentHooks() Hooks {
	hooksMu.RLock()
	defer hooksMu.RUnlock()
	return hooks
}

// built is one merged catalog.
type built struct {
	models         []Model
	byID           map[string]Model
	customFallback func(id string) (Model, bool)
}

var (
	builtMu sync.Mutex
	merged  *built
)

// Bust drops the merged catalog so the next read rebuilds it.
func Bust() {
	builtMu.Lock()
	merged = nil
	builtMu.Unlock()
}

func current() *built {
	builtMu.Lock()
	defer builtMu.Unlock()
	if merged == nil {
		merged = build()
	}
	return merged
}

func (b *built) put(m Model) {
	m = m.withShort()
	if _, ok := b.byID[m.ID]; !ok {
		b.models = append(b.models, m)
	} else {
		for i := range b.models {
			if b.models[i].ID == m.ID {
				b.models[i] = m
				break
			}
		}
	}
	b.byID[m.ID] = m
}

func isKimiSubscriptionKey(h Hooks) bool {
	return h.APIKey != nil && strings.HasPrefix(h.APIKey("kimi"), "sk-kimi-")
}

func build() *built {
	h := currentHooks()
	cache := readCache()
	out := &built{byID: map[string]Model{}}

	// Curated fallbacks win id collisions over the generated catalog.
	for _, m := range fallbackData.Fallback {
		out.put(m)
	}
	// Runtime-fetched defs override the snapshot; the snapshot fills in.
	defs := orderedModels{byID: map[string]Model{}}
	for _, id := range generatedModels.order {
		defs.order = append(defs.order, id)
		defs.byID[id] = generatedModels.byID[id]
	}
	for _, id := range cache.modelsOrder {
		if _, ok := defs.byID[id]; !ok {
			defs.order = append(defs.order, id)
		}
		defs.byID[id] = cache.models[id].withShort()
	}
	for _, id := range defs.order {
		if _, taken := out.byID[id]; taken {
			continue
		}
		m := defs.byID[id]
		if ids, ok := cache.availability[m.Provider]; ok {
			m.Available = contains(ids, id)
		} else {
			m.Available = true
		}
		out.put(m)
	}
	// github-copilot is gated by auth presence.
	authed := map[string]bool{}
	if h.Authorized != nil {
		for _, p := range h.Authorized() {
			authed[p] = true
		}
	}
	for i, m := range out.models {
		if m.Provider == "github-copilot" {
			m.Available = authed["github-copilot"]
			out.models[i], out.byID[m.ID] = m, m
		}
	}
	// A Kimi Code subscription key only calls the plan's own models.
	if isKimiSubscriptionKey(h) {
		kept := out.models[:0]
		for _, m := range out.models {
			if m.Provider == "kimi" {
				delete(out.byID, m.ID)
				continue
			}
			kept = append(kept, m)
		}
		out.models = kept
		for _, m := range fallbackData.KimiCode {
			out.put(m)
		}
	}

	// Custom providers: declared limits, then the gateway's own report, then
	// the vendor catalog, then the floor.
	byShort := map[string]Model{}
	for _, m := range out.models {
		if _, ok := byShort[m.ShortID]; !ok {
			byShort[m.ShortID] = m
		}
	}
	resolver := vendorResolver{
		qualified: chain(mapIndex(cache.refs), mapIndex(out.byID)),
		short:     mapIndex(byShort),
	}
	var customs []CustomProvider
	if h.CustomProviders != nil {
		customs = h.CustomProviders()
	}
	out.customFallback = func(id string) (Model, bool) {
		slash := strings.IndexByte(id, '/')
		if slash < 0 || slash == len(id)-1 {
			return Model{}, false
		}
		provID, local := id[:slash], id[slash+1:]
		for _, c := range customs {
			if "custom:"+c.Name == provID {
				meta := cache.customMeta[id]
				f := gatewayFloor
				f.Name = local
				return buildModelInfo(id, provID, []*Metadata{fromDiscovery(meta), resolver.resolve(local)}, f), true
			}
		}
		return Model{}, false
	}
	for _, c := range customs {
		provID := "custom:" + c.Name
		entries := c.Models
		if len(entries) == 0 {
			for _, m := range FallbackModelsForSdk(c.SDK) {
				in, outp, cr, cw := m.Cost.Input, m.Cost.Output, m.Cost.CacheRead, m.Cost.CacheWrite
				entries = append(entries, CustomModelDecl{
					ID: m.ShortID, Name: m.Name, ContextWindow: m.Context, MaxOutput: m.MaxOut,
					Cost: &PartialCost{Input: &in, Output: &outp, CacheRead: &cr, CacheWrite: &cw},
				})
			}
		}
		for _, m := range entries {
			full := provID + "/" + m.ID
			f := gatewayFloor
			f.Name = m.ID
			out.put(buildModelInfo(full, provID,
				[]*Metadata{fromDeclaration(m), fromDiscovery(cache.customMeta[full]), resolver.resolve(m.ID)}, f))
		}
	}

	// ~/.loop/models.json patches win: {...out[id], ...patch, id}.
	for _, id := range overrideOrder() {
		patch := readOverrides()[id]
		base := map[string]json.RawMessage{}
		if m, ok := out.byID[id]; ok {
			raw, _ := json.Marshal(m)
			_ = json.Unmarshal(raw, &base)
		}
		var p map[string]json.RawMessage
		if json.Unmarshal(patch, &p) != nil {
			continue
		}
		for k, v := range p {
			base[k] = v
		}
		base["id"], _ = json.Marshal(id)
		raw, _ := json.Marshal(base)
		var m Model
		if json.Unmarshal(raw, &m) == nil {
			out.put(m)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
