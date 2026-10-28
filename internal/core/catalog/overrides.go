package catalog

import (
	"encoding/json"
	"os"
	"sort"

	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/jsonstore"
)

// ~/.loop/models.json — loop's user override file (catalog/index.ts
// readUserOverrides): full ids mapped to partial model definitions. An entry
// for a known model patches it; an unknown id adds a custom model.

func overridesPath() string { return home.Path("models.json") }

func readOverrides() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	data, err := os.ReadFile(overridesPath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

func writeOverrides(o map[string]json.RawMessage) error {
	// loop: JSON.stringify(overrides, null, 2) + "\n". Go sorts map keys;
	// loop keeps insertion order — harmless for an object keyed by id.
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home.Dir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(overridesPath(), append(data, '\n'), 0o644)
}

// overrideOrder is the file's ids in file order.
func overrideOrder() []string {
	data, err := os.ReadFile(overridesPath())
	if err != nil {
		return nil
	}
	o, err := jsonstore.ParseObject(data)
	if err != nil {
		return nil
	}
	return o.Keys()
}

// CustomModelInput is loop's CustomModelInput.
type CustomModelInput struct {
	Provider      string
	ModelID       string
	Name          string
	ContextWindow int
	MaxOutput     int
	InputCost     float64
	OutputCost    float64
}

// AddCustomModel adds a user model to models.json; returns the full id.
func AddCustomModel(in CustomModelInput) (string, error) {
	id := in.Provider + "/" + in.ModelID
	name := in.Name
	if name == "" {
		name = in.ModelID
	}
	ctx, maxOut := in.ContextWindow, in.MaxOutput
	if ctx == 0 {
		ctx = 128_000
	}
	if maxOut == 0 {
		maxOut = 8_192
	}
	raw, _ := json.Marshal(map[string]any{
		"id": id, "provider": in.Provider, "name": name,
		"contextWindow": ctx, "maxOutput": maxOut,
		"cost":      map[string]float64{"input": in.InputCost, "output": in.OutputCost, "cacheRead": 0, "cacheWrite": 0},
		"reasoning": false, "modalities": []string{"text"}, "available": true,
	})
	o := readOverrides()
	o[id] = raw
	defer Bust()
	return id, writeOverrides(o)
}

// RemoveCustomModel drops an id from models.json.
func RemoveCustomModel(id string) (bool, error) {
	o := readOverrides()
	if _, ok := o[id]; !ok {
		return false, nil
	}
	delete(o, id)
	defer Bust()
	return true, writeOverrides(o)
}

// ListCustomModelIDs is every id the user added or overrode.
func ListCustomModelIDs() []string {
	var out []string
	for id := range readOverrides() {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
