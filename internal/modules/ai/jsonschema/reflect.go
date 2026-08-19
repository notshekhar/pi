package jsonschema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Reflector derives JSON Schemas from Go types.
//
// The zero Reflector is ready to use and produces schemas suited to tool
// inputs: definitions inlined, and additionalProperties set to false on every
// object so that providers running in strict mode accept them.
type Reflector struct {
	// AllowAdditionalProperties leaves additionalProperties unset instead of
	// forcing it to false. Strict mode on OpenAI and Anthropic requires false,
	// so leave this off unless a provider complains.
	AllowAdditionalProperties bool

	// RequireAll marks every field required regardless of omitempty or
	// pointer-ness. Some models handle a fully-required schema more reliably,
	// at the cost of forcing the model to supply every field.
	RequireAll bool

	// ExpandedStruct is unused for tool inputs and reserved for future use.
	ExpandedStruct bool
}

// Reflect returns the schema for T.
func Reflect[T any]() *Schema {
	var r Reflector
	return r.Reflect(reflect.TypeFor[T]())
}

// Reflect returns the schema for the given type.
func (r *Reflector) Reflect(t reflect.Type) *Schema {
	st := &reflectState{
		reflector: r,
		onPath:    map[reflect.Type]bool{},
		defs:      map[string]*Schema{},
		recursive: map[reflect.Type]string{},
	}
	s := st.schemaFor(t, tagOptions{})
	if len(st.defs) > 0 {
		s.Defs = st.defs
	}
	return s
}

// reflectState carries the cycle-detection bookkeeping for one Reflect call.
type reflectState struct {
	reflector *Reflector
	// onPath holds the struct types currently being expanded, which is how
	// recursion is detected.
	onPath map[reflect.Type]bool
	// defs collects subschemas for types that turned out to be recursive.
	defs map[string]*Schema
	// recursive maps a recursive type to its name in defs.
	recursive map[reflect.Type]string
}

// jsonMarshaler is checked so that types with custom JSON encodings are not
// described by their Go field layout, which would be wrong on the wire.
var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	timeType          = reflect.TypeFor[time.Time]()
	rawMessageType    = reflect.TypeFor[json.RawMessage]()
)

// schemaFor builds the schema for a type, applying field-level tag options.
func (st *reflectState) schemaFor(t reflect.Type, opts tagOptions) *Schema {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	s := &Schema{}

	switch {
	case t == timeType:
		s.Type = String
		s.Format = "date-time"
	case t == rawMessageType:
		// A raw message can hold any JSON value, so impose no type.
	case t.Kind() != reflect.Struct && t.Implements(jsonMarshalerType):
		// Custom encoding with an unknown shape: describe nothing.
	default:
		st.fill(s, t)
	}

	opts.apply(s)
	return s
}

// fill populates s according to the kind of t.
func (st *reflectState) fill(s *Schema, t reflect.Type) {
	switch t.Kind() {
	case reflect.Bool:
		s.Type = Boolean

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		s.Type = Integer

	case reflect.Float32, reflect.Float64:
		s.Type = Number

	case reflect.String:
		s.Type = String

	case reflect.Slice, reflect.Array:
		// []byte is base64-encoded by encoding/json, so it is a string.
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			s.Type = String
			return
		}
		s.Type = Array
		s.Items = st.schemaFor(t.Elem(), tagOptions{})

	case reflect.Map:
		s.Type = Object
		// A map has no fixed properties; its values constrain the schema.
		if t.Elem().Kind() != reflect.Interface {
			s.AdditionalProperties = st.schemaFor(t.Elem(), tagOptions{})
		}

	case reflect.Struct:
		st.fillStruct(s, t)

	case reflect.Interface:
		// An interface can hold any JSON value: impose no type.

	default:
		// Chan, Func, UnsafePointer and friends are not JSON-representable.
		// Leaving the schema empty is the honest description.
	}
}

// fillStruct expands a struct into an object schema, emitting a $ref instead
// if the type is already being expanded further up the stack.
func (st *reflectState) fillStruct(s *Schema, t reflect.Type) {
	if st.onPath[t] {
		// Recursive type: inlining would not terminate, so name it and refer
		// to it. The definition is filled in by the outermost expansion.
		name := st.defName(t)
		st.recursive[t] = name
		s.Ref = "#/$defs/" + name
		return
	}

	st.onPath[t] = true
	defer delete(st.onPath, t)

	s.Type = Object
	props := NewProperties()
	var required []string

	st.collectFields(t, props, &required)

	if props.Len() > 0 {
		s.Properties = props
	}
	s.Required = required
	if !st.reflector.AllowAdditionalProperties {
		s.AdditionalProperties = false
	}

	// If expanding this struct revealed that it refers back to itself, publish
	// the completed schema under the name the inner $ref used.
	if name, ok := st.recursive[t]; ok {
		if _, done := st.defs[name]; !done {
			// Copy so the definition is not the same pointer as the inlined
			// schema, which callers may mutate.
			def := *s
			st.defs[name] = &def
		}
	}
}

// collectFields walks a struct's fields, flattening embedded structs the way
// encoding/json does.
func (st *reflectState) collectFields(t reflect.Type, props *Properties, required *[]string) {
	for i := range t.NumField() {
		f := t.Field(i)

		jsonTag := f.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}
		name, jsonOpts := parseJSONTag(jsonTag)

		// An embedded struct with no JSON name is inlined by encoding/json.
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				st.collectFields(ft, props, required)
				continue
			}
		}

		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}

		opts := parseSchemaTag(f.Tag.Get("jsonschema"))
		props.Set(name, st.schemaFor(f.Type, opts))

		if st.isRequired(f, jsonOpts, opts) {
			*required = append(*required, name)
		}
	}
}

// isRequired decides whether a field must be present.
//
// The default follows the shape of the Go type: a pointer or an omitempty
// field is optional, everything else is required. The jsonschema tag can force
// either answer.
func (st *reflectState) isRequired(f reflect.StructField, jsonOpts []string, opts tagOptions) bool {
	if opts.optional {
		return false
	}
	if opts.required || st.reflector.RequireAll {
		return true
	}
	if f.Type.Kind() == reflect.Ptr {
		return false
	}
	for _, o := range jsonOpts {
		if o == "omitempty" || o == "omitzero" {
			return false
		}
	}
	return true
}

// defName returns a stable, unique name for a recursive type.
func (st *reflectState) defName(t reflect.Type) string {
	name := t.Name()
	if name == "" {
		name = "anon"
	}
	// Disambiguate same-named types from different packages.
	for existing, n := range st.recursive {
		if n == name && existing != t {
			return t.PkgPath() + "." + name
		}
	}
	return name
}

// parseJSONTag splits a json struct tag into its name and options.
func parseJSONTag(tag string) (string, []string) {
	if tag == "" {
		return "", nil
	}
	parts := strings.Split(tag, ",")
	return parts[0], parts[1:]
}

// tagOptions is the parsed content of a `jsonschema:"..."` struct tag.
type tagOptions struct {
	description string
	title       string
	format      string
	pattern     string
	enum        []any
	def         any
	hasDefault  bool

	minimum, maximum     *float64
	minLength, maxLength *int
	minItems, maxItems   *int
	uniqueItems          bool

	required bool
	optional bool
}

// apply writes the tag options onto a schema.
func (o tagOptions) apply(s *Schema) {
	if o.description != "" {
		s.Description = o.description
	}
	if o.title != "" {
		s.Title = o.title
	}
	if o.format != "" {
		s.Format = o.format
	}
	if o.pattern != "" {
		s.Pattern = o.pattern
	}
	if len(o.enum) > 0 {
		s.Enum = o.enum
	}
	if o.hasDefault {
		s.Default = o.def
	}
	if o.minimum != nil {
		s.Minimum = o.minimum
	}
	if o.maximum != nil {
		s.Maximum = o.maximum
	}
	if o.minLength != nil {
		s.MinLength = o.minLength
	}
	if o.maxLength != nil {
		s.MaxLength = o.maxLength
	}
	if o.minItems != nil {
		s.MinItems = o.minItems
	}
	if o.maxItems != nil {
		s.MaxItems = o.maxItems
	}
	if o.uniqueItems {
		s.UniqueItems = true
	}
}

// parseSchemaTag reads a `jsonschema:"..."` tag.
//
// Entries are comma-separated key=value pairs or bare flags. A value
// containing a comma must be single-quoted, e.g.
// `jsonschema:"description='one, two',required"`.
func parseSchemaTag(tag string) tagOptions {
	var o tagOptions
	if tag == "" {
		return o
	}

	for _, entry := range splitTag(tag) {
		key, value, hasValue := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		value = unquote(strings.TrimSpace(value))

		switch key {
		case "description", "desc":
			o.description = value
		case "title":
			o.title = value
		case "format":
			o.format = value
		case "pattern":
			o.pattern = value
		case "enum":
			o.enum = append(o.enum, coerce(value))
		case "default":
			o.def, o.hasDefault = coerce(value), true
		case "minimum", "min":
			o.minimum = parseFloat(value)
		case "maximum", "max":
			o.maximum = parseFloat(value)
		case "minLength":
			o.minLength = parseInt(value)
		case "maxLength":
			o.maxLength = parseInt(value)
		case "minItems":
			o.minItems = parseInt(value)
		case "maxItems":
			o.maxItems = parseInt(value)
		case "uniqueItems":
			o.uniqueItems = !hasValue || value == "true"
		case "required":
			o.required = true
		case "optional":
			o.optional = true
		}
	}
	return o
}

// splitTag splits on commas that are not inside single quotes.
func splitTag(tag string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false

	for _, r := range tag {
		switch {
		case r == '\'':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// unquote strips surrounding single quotes.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}

// coerce converts a tag value to the narrowest JSON type it parses as, so
// that `enum=1` produces a number rather than the string "1".
func coerce(s string) any {
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n
	}
	return s
}

func parseFloat(s string) *float64 {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &n
}

func parseInt(s string) *int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}

// String renders the schema as indented JSON, for debugging.
func (s *Schema) String() string {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Sprintf("<invalid schema: %v>", err)
	}
	return string(b)
}
