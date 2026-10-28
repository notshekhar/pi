// Package jsonstore is loop's CachedStore (auth/storage.ts): a JSON object
// file under ~/.loop, read whole, cached in memory, and rewritten whole on
// every change.
//
// It writes byte-for-byte what loop's configstore writes, because the two
// programs share these files: tab indentation, no trailing newline, keys in
// insertion order, defaults merged in front, mode 0600, atomic replace. A key
// pi does not know is carried through untouched — the file belongs to both.
package jsonstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Object is a JSON object that remembers its key order.
type Object struct {
	keys   []string
	values map[string]json.RawMessage
}

// NewObject is an empty object.
func NewObject() *Object { return &Object{values: map[string]json.RawMessage{}} }

// ParseObject reads a JSON object, keeping key order.
func ParseObject(data []byte) (*Object, error) {
	o := NewObject()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("jsonstore: not a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("jsonstore: bad key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		o.SetRaw(key, raw)
	}
	return o, nil
}

// Keys is the keys in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Has reports whether key is present.
func (o *Object) Has(key string) bool { _, ok := o.values[key]; return ok }

// Raw is a key's JSON, nil when absent.
func (o *Object) Raw(key string) json.RawMessage { return o.values[key] }

// Get decodes a key into v; false when absent, null or undecodable.
func (o *Object) Get(key string, v any) bool {
	raw, ok := o.values[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

// SetRaw stores a key's JSON. An existing key keeps its position, a new one
// goes last — the order a JS object gives.
func (o *Object) SetRaw(key string, raw json.RawMessage) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = append(json.RawMessage(nil), raw...)
}

// Set stores a Go value under key.
func (o *Object) Set(key string, v any) error {
	raw, err := Marshal(v)
	if err != nil {
		return err
	}
	o.SetRaw(key, raw)
	return nil
}

// Delete removes a key.
func (o *Object) Delete(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Clone is a deep copy.
func (o *Object) Clone() *Object {
	c := NewObject()
	for _, k := range o.keys {
		c.SetRaw(k, o.values[k])
	}
	return c
}

// MarshalJSON writes the object compactly, in order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.values[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads an object in order.
func (o *Object) UnmarshalJSON(data []byte) error {
	p, err := ParseObject(data)
	if err != nil {
		return err
	}
	*o = *p
	return nil
}

// Encode is the object as loop writes it: JSON.stringify(v, undefined, "\t").
func (o *Object) Encode() []byte {
	compact, _ := o.MarshalJSON()
	return Indent(compact)
}

// Indent formats JSON the way JSON.stringify(v, undefined, "\t") does.
func Indent(compact []byte) []byte {
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "\t"); err != nil {
		return compact
	}
	return out.Bytes()
}

// Marshal is json.Marshal without HTML escaping, matching JSON.stringify.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// Store is one JSON object file, cached in memory.
type Store struct {
	path     string
	defaults *Object

	mu     sync.Mutex
	cache  *Object
	loaded bool
}

// New is a store over path with defaults merged in front of the file's own
// keys on first use (configstore's constructor).
func New(path string, defaults *Object) *Store {
	if defaults == nil {
		defaults = NewObject()
	}
	return &Store{path: path, defaults: defaults}
}

// Path is the file the store keeps.
func (s *Store) Path() string { return s.path }

// load reads the file once, merging defaults the way configstore does
// ({...defaults, ...file}), and writes the merge back if it differs.
func (s *Store) load() *Object {
	if s.loaded {
		return s.cache
	}
	file := NewObject()
	data, err := os.ReadFile(s.path)
	if err == nil {
		if parsed, perr := ParseObject(data); perr == nil {
			file = parsed
		}
	}
	merged := NewObject()
	for _, k := range s.defaults.keys {
		if file.Has(k) {
			merged.SetRaw(k, file.values[k])
		} else {
			merged.SetRaw(k, s.defaults.values[k])
		}
	}
	for _, k := range file.keys {
		merged.SetRaw(k, file.values[k])
	}
	s.cache, s.loaded = merged, true
	if encoded := merged.Encode(); err != nil || !bytes.Equal(encoded, data) {
		_ = s.write(merged)
	}
	return merged
}

// write replaces the file atomically with mode 0600.
func (s *Store) write(o *Object) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "."+filepath.Base(s.path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(o.Encode()); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, s.path)
}

// All is a copy of the whole object.
func (s *Store) All() *Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load().Clone()
}

// Get decodes key into v.
func (s *Store) Get(key string, v any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load().Get(key, v)
}

// Raw is key's JSON, nil when absent.
func (s *Store) Raw(key string) json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(json.RawMessage(nil), s.load().Raw(key)...)
}

// Has reports whether key is present.
func (s *Store) Has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load().Has(key)
}

// Set writes one key through. Like configstore it re-reads the file first, so
// a key another process wrote meanwhile is not lost.
func (s *Store) Set(key string, v any) error {
	raw, err := Marshal(v)
	if err != nil {
		return err
	}
	return s.Mutate(func(o *Object) { o.SetRaw(key, raw) })
}

// Delete removes one key.
func (s *Store) Delete(key string) error {
	return s.Mutate(func(o *Object) { o.Delete(key) })
}

// Mutate applies a change to the freshest copy of the file and writes it.
func (s *Store) Mutate(change func(*Object)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = false
	o := s.load().Clone()
	change(o)
	if err := s.write(o); err != nil {
		return err
	}
	s.cache = o
	return nil
}

// ReplaceAll writes a whole object (configstore's `all =`).
func (s *Store) ReplaceAll(o *Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(o); err != nil {
		return err
	}
	s.cache, s.loaded = o.Clone(), true
	return nil
}

// Refresh drops the cache so the next read comes from disk.
func (s *Store) Refresh() {
	s.mu.Lock()
	s.loaded, s.cache = false, nil
	s.mu.Unlock()
}

// Defaults builds a defaults object from ordered key/value pairs.
func Defaults(pairs ...any) *Object {
	o := NewObject()
	for i := 0; i+1 < len(pairs); i += 2 {
		_ = o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}
