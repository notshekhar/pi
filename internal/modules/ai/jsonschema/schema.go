// Package jsonschema provides the JSON Schema draft-07 subset that language
// model APIs accept for tool inputs and structured output, plus a reflector
// that derives a schema from a Go type.
//
// The reflector inlines definitions rather than emitting $ref/$defs, because
// several provider strict-mode implementations reject or mishandle references.
// References appear only for genuinely recursive types, where inlining is
// impossible.
package jsonschema

import (
	"bytes"
	"encoding/json"
)

// Type is a JSON Schema primitive type name.
type Type string

// JSON Schema primitive types.
const (
	Object  Type = "object"
	Array   Type = "array"
	String  Type = "string"
	Number  Type = "number"
	Integer Type = "integer"
	Boolean Type = "boolean"
	Null    Type = "null"
)

// Schema is a JSON Schema draft-07 document, restricted to the keywords that
// model providers actually honour. The zero value marshals to `{}`, which is
// a valid "anything goes" schema.
type Schema struct {
	// Version is the $schema keyword. Set it on root schemas only.
	Version string `json:"$schema,omitempty"`
	// Ref points at an entry in Defs. Set only for recursive types.
	Ref string `json:"$ref,omitempty"`
	// Defs holds named subschemas referenced by Ref.
	Defs map[string]*Schema `json:"$defs,omitempty"`

	Type        Type   `json:"type,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`

	// Properties and Required describe object shapes. Properties preserves
	// declaration order when marshalled.
	Properties *Properties `json:"properties,omitempty"`
	Required   []string    `json:"required,omitempty"`
	// AdditionalProperties is either a bool or a *Schema. Providers in strict
	// mode require it to be false.
	AdditionalProperties any `json:"additionalProperties,omitempty"`

	// Items describes array elements.
	Items *Schema `json:"items,omitempty"`

	// Enum restricts a value to a fixed set. Const restricts it to one value.
	Enum  []any `json:"enum,omitempty"`
	Const any   `json:"const,omitempty"`

	// AnyOf, OneOf, AllOf and Not compose subschemas.
	AnyOf []*Schema `json:"anyOf,omitempty"`
	OneOf []*Schema `json:"oneOf,omitempty"`
	AllOf []*Schema `json:"allOf,omitempty"`
	Not   *Schema   `json:"not,omitempty"`

	// Format is a draft-07 semantic hint such as "date-time" or "uri".
	Format string `json:"format,omitempty"`

	Minimum   *float64 `json:"minimum,omitempty"`
	Maximum   *float64 `json:"maximum,omitempty"`
	MinLength *int     `json:"minLength,omitempty"`
	MaxLength *int     `json:"maxLength,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
	MinItems  *int     `json:"minItems,omitempty"`
	MaxItems  *int     `json:"maxItems,omitempty"`

	// UniqueItems constrains arrays to distinct elements.
	UniqueItems bool `json:"uniqueItems,omitempty"`

	// Default is an example value shown to the model.
	Default any `json:"default,omitempty"`
}

// Property is one named entry of an object schema.
type Property struct {
	Name   string
	Schema *Schema
}

// Properties is an ordered map of property name to schema. Order is preserved
// through marshalling because it is the order the model reads the fields in,
// and struct declaration order usually reflects intent.
type Properties struct {
	entries []Property
	index   map[string]int
}

// NewProperties returns an empty ordered property map.
func NewProperties() *Properties {
	return &Properties{index: map[string]int{}}
}

// Set adds or replaces a property, appending new names in insertion order.
func (p *Properties) Set(name string, s *Schema) {
	if p.index == nil {
		p.index = map[string]int{}
	}
	if i, ok := p.index[name]; ok {
		p.entries[i].Schema = s
		return
	}
	p.index[name] = len(p.entries)
	p.entries = append(p.entries, Property{Name: name, Schema: s})
}

// Get returns the schema for a property name.
func (p *Properties) Get(name string) (*Schema, bool) {
	if p == nil || p.index == nil {
		return nil, false
	}
	i, ok := p.index[name]
	if !ok {
		return nil, false
	}
	return p.entries[i].Schema, true
}

// Names returns the property names in insertion order.
func (p *Properties) Names() []string {
	if p == nil {
		return nil
	}
	out := make([]string, len(p.entries))
	for i, e := range p.entries {
		out[i] = e.Name
	}
	return out
}

// Len returns the number of properties.
func (p *Properties) Len() int {
	if p == nil {
		return 0
	}
	return len(p.entries)
}

// MarshalJSON writes the properties as a JSON object in insertion order.
func (p *Properties) MarshalJSON() ([]byte, error) {
	if p == nil || len(p.entries) == 0 {
		return []byte("{}"), nil
	}
	buf := []byte{'{'}
	for i, e := range p.entries {
		if i > 0 {
			buf = append(buf, ',')
		}
		name, err := json.Marshal(e.Name)
		if err != nil {
			return nil, err
		}
		val, err := json.Marshal(e.Schema)
		if err != nil {
			return nil, err
		}
		buf = append(buf, name...)
		buf = append(buf, ':')
		buf = append(buf, val...)
	}
	return append(buf, '}'), nil
}

// UnmarshalJSON reads a JSON object into the property map. Key order follows
// the source document.
func (p *Properties) UnmarshalJSON(data []byte) error {
	var raw map[string]*Schema
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	order, err := objectKeyOrder(data)
	if err != nil {
		return err
	}
	p.entries = nil
	p.index = map[string]int{}
	for _, k := range order {
		p.Set(k, raw[k])
	}
	return nil
}

// objectKeyOrder returns the keys of a JSON object in document order.
func objectKeyOrder(data []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	// Consume the opening brace.
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			continue
		}
		keys = append(keys, key)
		// Skip the value.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// Ptr returns a pointer to v, for populating optional Schema fields.
func Ptr[T any](v T) *T { return &v }
