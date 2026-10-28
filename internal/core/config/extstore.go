package config

import "fmt"

// The per-extension settings store.
//
// Lives here rather than in the extension package because that package must
// not read or write the settings file itself: an extension that could open
// the file could also read another extension's keys, and the whole point of
// namespacing them is that it cannot.

// ExtensionStore is one extension's slice of the settings file.
type ExtensionStore struct{ name string }

// OpenExtensionStore returns the store for an extension.
func OpenExtensionStore(name string) *ExtensionStore { return &ExtensionStore{name: name} }

// Get reads a key, or the fallback when it has never been set.
func (s *ExtensionStore) Get(key, fallback string) string {
	if v, ok := LoadSettings().ExtensionSettings[s.name][key]; ok {
		switch t := v.(type) {
		case string:
			if t != "" {
				return t
			}
		case nil:
		default:
			return fmt.Sprint(t)
		}
	}
	return fallback
}

// Set writes a key.
func (s *ExtensionStore) Set(key, value string) error {
	return Update(func(st *Settings) {
		if st.ExtensionSettings == nil {
			st.ExtensionSettings = map[string]map[string]any{}
		}
		if st.ExtensionSettings[s.name] == nil {
			st.ExtensionSettings[s.name] = map[string]any{}
		}
		st.ExtensionSettings[s.name][key] = value
	})
}
