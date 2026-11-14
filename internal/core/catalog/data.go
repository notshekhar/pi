package catalog

import (
	_ "embed"
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
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
	var raw map[string]jsontext.Value
	if json.Unmarshal(generatedJSON, &raw, jsonopt.Read) != nil {
		return out
	}
	// Recover file order: a Go map loses it.
	dec := jsontext.NewDecoder(bytesReader(generatedJSON), jsonopt.Read)
	if tok, err := dec.ReadToken(); err == nil && tok.Kind() == '{' {
		for dec.PeekKind() != '}' {
			tok, err := dec.ReadToken()
			if err != nil {
				break
			}
			id := tok.String()
			var m Model
			if json.UnmarshalDecode(dec, &m) != nil {
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
	_ = json.Unmarshal(fallbacksJSON, &f, jsonopt.Read)
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
