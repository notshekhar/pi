package google

import "github.com/notshekhar/pi/internal/modules/ai/jsonschema"

// toOpenAPISchema converts a JSON Schema draft-07 document into the OpenAPI
// 3.0 subset Google accepts.
//
// This conversion is required, not cosmetic: Google rejects a request whose
// schema carries keywords outside the subset. In particular
// additionalProperties, $schema and $defs are dropped, and a $ref cannot be
// represented at all — a recursive tool input has to be flattened before it
// reaches this point.
//
// isRoot suppresses an empty root schema, because Google rejects a parameters
// object with no properties; a nested empty object is legal and preserved.
func toOpenAPISchema(s *jsonschema.Schema, isRoot bool) any {
	if s == nil {
		return nil
	}

	if isEmptyObject(s) {
		if isRoot {
			// A tool with no arguments must omit parameters entirely.
			return nil
		}
		if s.Description != "" {
			return map[string]any{"type": "object", "description": s.Description}
		}
		return map[string]any{"type": "object"}
	}

	out := map[string]any{}

	if s.Description != "" {
		out["description"] = s.Description
	}
	if s.Type != "" {
		out["type"] = string(s.Type)
	}
	if s.Format != "" {
		out["format"] = s.Format
	}
	if len(s.Required) > 0 {
		out["required"] = s.Required
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	if s.Const != nil {
		// OpenAPI 3.0 has no const, so a single-value enum expresses it.
		out["enum"] = []any{s.Const}
	}
	if s.MinLength != nil {
		out["minLength"] = *s.MinLength
	}
	if s.MaxLength != nil {
		out["maxLength"] = *s.MaxLength
	}
	if s.Minimum != nil {
		out["minimum"] = *s.Minimum
	}
	if s.Maximum != nil {
		out["maximum"] = *s.Maximum
	}
	if s.MinItems != nil {
		out["minItems"] = *s.MinItems
	}
	if s.MaxItems != nil {
		out["maxItems"] = *s.MaxItems
	}
	if s.Pattern != "" {
		out["pattern"] = s.Pattern
	}
	if s.Title != "" {
		out["title"] = s.Title
	}

	if s.Properties != nil && s.Properties.Len() > 0 {
		props := map[string]any{}
		for _, name := range s.Properties.Names() {
			child, _ := s.Properties.Get(name)
			if converted := toOpenAPISchema(child, false); converted != nil {
				props[name] = converted
			}
		}
		out["properties"] = props
	}

	if s.Items != nil {
		if converted := toOpenAPISchema(s.Items, false); converted != nil {
			out["items"] = converted
		}
	}

	// A union containing null becomes a nullable schema, because OpenAPI 3.0
	// has no null type of its own.
	if len(s.AnyOf) > 0 {
		out = mergeUnion(out, s.AnyOf, "anyOf")
	}
	if len(s.OneOf) > 0 {
		out = mergeUnion(out, s.OneOf, "oneOf")
	}
	if len(s.AllOf) > 0 {
		converted := make([]any, 0, len(s.AllOf))
		for _, sub := range s.AllOf {
			if c := toOpenAPISchema(sub, false); c != nil {
				converted = append(converted, c)
			}
		}
		out["allOf"] = converted
	}

	return out
}

// mergeUnion converts a union, collapsing an optional-value union into a
// nullable schema.
func mergeUnion(out map[string]any, subs []*jsonschema.Schema, key string) map[string]any {
	nonNull := make([]*jsonschema.Schema, 0, len(subs))
	hasNull := false

	for _, sub := range subs {
		if sub != nil && sub.Type == jsonschema.Null {
			hasNull = true
			continue
		}
		nonNull = append(nonNull, sub)
	}

	// One real branch plus null is just that branch, marked nullable. This is
	// the common shape a nullable field produces, and Google handles it far
	// better than a two-element union.
	if hasNull && len(nonNull) == 1 {
		if converted, ok := toOpenAPISchema(nonNull[0], false).(map[string]any); ok {
			for k, v := range converted {
				out[k] = v
			}
		}
		out["nullable"] = true
		return out
	}

	converted := make([]any, 0, len(nonNull))
	for _, sub := range nonNull {
		if c := toOpenAPISchema(sub, false); c != nil {
			converted = append(converted, c)
		}
	}
	if len(converted) > 0 {
		out[key] = converted
	}
	if hasNull {
		out["nullable"] = true
	}
	return out
}

// isEmptyObject reports an object schema that constrains nothing, which is
// what a tool taking no arguments produces.
func isEmptyObject(s *jsonschema.Schema) bool {
	if s.Type != jsonschema.Object {
		return false
	}
	if s.Properties != nil && s.Properties.Len() > 0 {
		return false
	}
	// A composition or enum keyword constrains the value even with no
	// properties, so the schema is not empty and must be converted.
	if len(s.AnyOf) > 0 || len(s.OneOf) > 0 || len(s.AllOf) > 0 || len(s.Enum) > 0 || s.Const != nil {
		return false
	}

	// additionalProperties: false is what the reflector emits for every
	// object, so it does not make a schema non-empty. A subschema does,
	// because it describes the permitted values.
	switch v := s.AdditionalProperties.(type) {
	case nil:
		return true
	case bool:
		return !v
	default:
		return false
	}
}
