package catalog

import (
	_ "embed"
	"encoding/json"
)

// loop's own model tables, exported by scripts/sync-catalog/export.ts:
// GENERATED_MODELS (the models.dev snapshot), FALLBACK_MODELS (curated, wins
// collisions), KIMI_CODE_MODELS, and fallbackModelsForSdk per SDK.

//go:embed data/generated.json
var generatedJSON []byte

//go:embed data/fallbacks.json
var fallbacksJSON []byte

var (
	generatedModels = decodeGenerated()
	fallbackData    = decodeFallbacks()
)

type fallbacks struct {
	Fallback []Model            `json:"fallback"`
	KimiCode []Model            `json:"kimiCode"`
	SDK      map[string][]Model `json:"sdk"`
}

// generated keeps the snapshot's key order (loop iterates it in order).
type orderedModels struct {
	order []string
	byID  map[string]Model
}

func decodeGenerated() orderedModels {
	out := orderedModels{byID: map[string]Model{}}
	var raw map[string]json.RawMessage
	if json.Unmarshal(generatedJSON, &raw) != nil {
		return out
	}
	// Recover file order: encoding/json maps lose it.
	dec := json.NewDecoder(bytesReader(generatedJSON))
	if tok, err := dec.Token(); err == nil && tok == json.Delim('{') {
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			id, _ := tok.(string)
			var m Model
			if dec.Decode(&m) != nil {
				break
			}
			out.order = append(out.order, id)
			out.byID[id] = m.withShort()
		}
	}
	return out
}

func decodeFallbacks() fallbacks {
	var f fallbacks
	_ = json.Unmarshal(fallbacksJSON, &f)
	for i := range f.Fallback {
		f.Fallback[i] = f.Fallback[i].withShort()
	}
	for i := range f.KimiCode {
		f.KimiCode[i] = f.KimiCode[i].withShort()
	}
	return f
}

// FallbackModelsForSdk is loop's fallbackModelsForSdk.
func FallbackModelsForSdk(sdk string) []Model {
	if ms, ok := fallbackData.SDK[sdk]; ok {
		return ms
	}
	return fallbackData.SDK["openai-compatible"]
}
