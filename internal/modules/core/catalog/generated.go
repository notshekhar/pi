package catalog

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
)

// The full model catalog, embedded from models.dev.
//
// Embedded JSON rather than generated Go: it is 743 models and it changes
// every time a vendor ships something, so the update is a file copy rather
// than a code change. `encoding/json` is stdlib, so this costs no dependency.
//
// The curated `seed` still matters and is not replaced. It carries the ORDER
// — `Default` takes the first entry for a provider, and "first" has to be a
// good default, not whatever sorts first alphabetically. So the seed leads,
// in its own order, and the generated catalog supplies everything else.

//go:embed models.json
var modelsJSON []byte

// generatedModel is one entry of models.json.
type generatedModel struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	MaxOutput     int    `json:"maxOutput"`
	Cost          struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cacheRead"`
		CacheWrite float64 `json:"cacheWrite"`
	} `json:"cost"`
	Reasoning bool `json:"reasoning"`
	Available bool `json:"available"`
}

// catalog is the merged list, built once.
var catalog = buildCatalog()

// buildCatalog merges the curated seed with the embedded catalog.
//
// A parse failure falls back to the seed alone rather than panicking: a
// corrupt catalog should cost you the long tail of models, not the ability to
// start.
func buildCatalog() []Model {
	out := append([]Model{}, seed...)
	seen := map[string]bool{}
	for _, m := range out {
		seen[m.ID] = true
	}

	var raw map[string]generatedModel
	if err := json.Unmarshal(modelsJSON, &raw); err != nil {
		return out
	}

	extra := make([]Model, 0, len(raw))
	for _, g := range raw {
		if !g.Available || seen[g.ID] {
			continue
		}
		short := strings.TrimPrefix(g.ID, g.Provider+"/")
		extra = append(extra, Model{
			ID:       g.ID,
			Provider: g.Provider,
			ShortID:  short,
			Name:     nameOr(g.Name, short),
			Context:  g.ContextWindow,
			MaxOut:   g.MaxOutput,
			Cost: Cost{
				Input:      g.Cost.Input,
				Output:     g.Cost.Output,
				CacheRead:  g.Cost.CacheRead,
				CacheWrite: g.Cost.CacheWrite,
			},
			Reasoning: g.Reasoning,
		})
	}
	// Map iteration is random, so the tail is sorted to keep the list — and
	// therefore every picker built from it — stable between runs.
	sort.Slice(extra, func(i, j int) bool { return extra[i].ID < extra[j].ID })
	return append(out, extra...)
}

func nameOr(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}
