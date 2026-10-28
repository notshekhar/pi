package config

import (
	"encoding/json"

	"github.com/notshekhar/pi/internal/core/jsonstore"
)

// ~/.loop/extensions.json — loop's extensions/store.ts: install records for
// external extensions and the on/off state of built-ins, shared with loop.

// ExtensionsStore is ~/.loop/extensions.json.
func ExtensionsStore() *jsonstore.Store {
	return store("extensions.json", func() *jsonstore.Object {
		return jsonstore.Defaults("extensions", map[string]any{})
	})
}

// ExtensionRecord is one installed extension (loop's ExtensionRecord).
type ExtensionRecord struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Source      string `json:"source"`
	SourceKind  string `json:"sourceKind"`
	Enabled     bool   `json:"enabled"`
	LinkPath    string `json:"linkPath,omitempty"`
	InstalledAt int64  `json:"installedAt"`
}

// ExtensionRecords lists installed extensions.
func ExtensionRecords() map[string]ExtensionRecord {
	out := map[string]ExtensionRecord{}
	ExtensionsStore().Get("extensions", &out)
	return out
}

// PutExtensionRecord writes a record, re-reading the file first.
func PutExtensionRecord(rec ExtensionRecord) error {
	return mutateObjectKey(ExtensionsStore(), "extensions", func(o *jsonstore.Object) { _ = o.Set(rec.Name, rec) })
}

// DeleteExtensionRecord removes a record.
func DeleteExtensionRecord(name string) error {
	return mutateObjectKey(ExtensionsStore(), "extensions", func(o *jsonstore.Object) { o.Delete(name) })
}

// BuiltinEnabled is a built-in's state, or fallback when never set.
func BuiltinEnabled(name string, fallback bool) bool {
	b := map[string]bool{}
	ExtensionsStore().Get("builtins", &b)
	if v, ok := b[name]; ok {
		return v
	}
	return fallback
}

// ExtensionState is every built-in the user has an opinion about.
func ExtensionState() map[string]bool {
	b := map[string]bool{}
	ExtensionsStore().Get("builtins", &b)
	for name, rec := range ExtensionRecords() {
		b[name] = rec.Enabled
	}
	return b
}

// SetBuiltinEnabled records a built-in's state.
func SetBuiltinEnabled(name string, on bool) error {
	return mutateObjectKey(ExtensionsStore(), "builtins", func(o *jsonstore.Object) { _ = o.Set(name, on) })
}

// mutateObjectKey edits one object-valued key of a store in place, keeping
// the order of its entries, against the freshest copy of the file.
func mutateObjectKey(s *jsonstore.Store, key string, change func(*jsonstore.Object)) error {
	return s.Mutate(func(o *jsonstore.Object) {
		inner := jsonstore.NewObject()
		if raw := o.Raw(key); raw != nil {
			if parsed, err := jsonstore.ParseObject(raw); err == nil {
				inner = parsed
			}
		}
		change(inner)
		raw, _ := inner.MarshalJSON()
		o.SetRaw(key, json.RawMessage(raw))
	})
}
