package jsonschema

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Validate checks a decoded JSON value (as encoding/json produces it: maps,
// slices, float64, string, bool, nil) against the schema. The error names
// the first offending path, so a model can correct exactly that field.
//
// The keywords are the ones tool schemas use; $ref is not followed.
func (s *Schema) Validate(v any) error {
	return s.validate(v, "")
}

func (s *Schema) validate(v any, path string) error {
	if s == nil {
		return nil
	}
	checks := []func(any, string) error{
		s.checkType, s.checkEnum, s.checkNumber, s.checkString,
		s.checkArray, s.checkObject, s.checkComposition,
	}
	for _, check := range checks {
		if err := check(v, path); err != nil {
			return err
		}
	}
	return nil
}

func fail(path, format string, args ...any) error {
	where := path
	if where == "" {
		where = "input"
	}
	return fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...))
}

func (s *Schema) checkType(v any, path string) error {
	if s.Type == "" || hasType(v, s.Type) {
		return nil
	}
	return fail(path, "expected %s, received %s", s.Type, typeOf(v))
}

func hasType(v any, t Type) bool {
	switch t {
	case Object:
		_, ok := v.(map[string]any)
		return ok
	case Array:
		_, ok := v.([]any)
		return ok
	case String:
		_, ok := v.(string)
		return ok
	case Number:
		_, ok := v.(float64)
		return ok
	case Integer:
		n, ok := v.(float64)
		return ok && n == math.Trunc(n)
	case Boolean:
		_, ok := v.(bool)
		return ok
	case Null:
		return v == nil
	}
	return true
}

func typeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	}
	return fmt.Sprintf("%T", v)
}

func (s *Schema) checkEnum(v any, path string) error {
	if s.Const != nil && !reflect.DeepEqual(v, s.Const) {
		return fail(path, "must be %v", s.Const)
	}
	if len(s.Enum) == 0 {
		return nil
	}
	for _, option := range s.Enum {
		if reflect.DeepEqual(v, option) {
			return nil
		}
	}
	options := make([]string, len(s.Enum))
	for i, o := range s.Enum {
		options[i] = fmt.Sprintf("%q", fmt.Sprint(o))
	}
	return fail(path, "must be one of %s, received %q", strings.Join(options, " | "), fmt.Sprint(v))
}

func (s *Schema) checkNumber(v any, path string) error {
	n, ok := v.(float64)
	if !ok {
		return nil
	}
	switch {
	case s.Minimum != nil && n < *s.Minimum:
		return fail(path, "must be >= %g", *s.Minimum)
	case s.ExclusiveMinimum != nil && n <= *s.ExclusiveMinimum:
		return fail(path, "must be > %g", *s.ExclusiveMinimum)
	case s.Maximum != nil && n > *s.Maximum:
		return fail(path, "must be <= %g", *s.Maximum)
	}
	return nil
}

func (s *Schema) checkString(v any, path string) error {
	str, ok := v.(string)
	if !ok {
		return nil
	}
	length := utf8.RuneCountInString(str)
	switch {
	case s.MinLength != nil && length < *s.MinLength:
		return fail(path, "must contain at least %d character(s)", *s.MinLength)
	case s.MaxLength != nil && length > *s.MaxLength:
		return fail(path, "must contain at most %d character(s)", *s.MaxLength)
	}
	if s.Pattern != "" {
		if re, err := regexp.Compile(s.Pattern); err == nil && !re.MatchString(str) {
			return fail(path, "must match %s", s.Pattern)
		}
	}
	return nil
}

func (s *Schema) checkArray(v any, path string) error {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	switch {
	case s.MinItems != nil && len(items) < *s.MinItems:
		return fail(path, "must contain at least %d item(s)", *s.MinItems)
	case s.MaxItems != nil && len(items) > *s.MaxItems:
		return fail(path, "must contain at most %d item(s)", *s.MaxItems)
	}
	for i, item := range items {
		if err := s.Items.validate(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Schema) checkObject(v any, path string) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	for _, name := range s.Required {
		if _, present := obj[name]; !present {
			return fail(join(path, name), "required")
		}
	}
	for name, value := range obj {
		prop, known := s.property(name)
		if !known {
			if s.AdditionalProperties == false {
				return fail(join(path, name), "unrecognized key")
			}
			continue
		}
		if err := prop.validate(value, join(path, name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Schema) property(name string) (*Schema, bool) {
	if s.Properties == nil {
		return nil, false
	}
	return s.Properties.Get(name)
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// checkComposition: every allOf branch must hold, and at least one anyOf or
// oneOf branch (oneOf is read as anyOf — tool schemas use it for unions).
func (s *Schema) checkComposition(v any, path string) error {
	for _, sub := range s.AllOf {
		if err := sub.validate(v, path); err != nil {
			return err
		}
	}
	for _, branches := range [][]*Schema{s.AnyOf, s.OneOf} {
		if len(branches) == 0 {
			continue
		}
		var first error
		for _, sub := range branches {
			err := sub.validate(v, path)
			if err == nil {
				first = nil
				break
			}
			if first == nil {
				first = err
			}
		}
		if first != nil {
			return first
		}
	}
	if s.Not != nil && s.Not.validate(v, path) == nil {
		return fail(path, "must not match the excluded schema")
	}
	return nil
}
