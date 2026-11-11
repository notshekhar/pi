package jsonschema

import (
	"encoding/json"
	"strings"
	"testing"
)

const todoSchema = `{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object",
"properties":{"content":{"type":"string","minLength":1},"status":{"type":"string","enum":["pending","in_progress","completed"]}},
"required":["content","status"],"additionalProperties":false}},"limit":{"type":"integer","minimum":1,"maximum":10}},
"required":["todos"],"additionalProperties":false}`

func TestValidate(t *testing.T) {
	var s Schema
	if err := json.Unmarshal([]byte(todoSchema), &s); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		`{"todos":[{"content":"a","status":"pending"}]}`:          "",
		`{"todos":[{"content":"a","status":"started"}]}`:          `todos[0].status: must be one of`,
		`{"todos":[{"content":"","status":"pending"}]}`:           `todos[0].content: must contain at least 1`,
		`{"todos":[{"status":"pending"}]}`:                        `todos[0].content: required`,
		`{}`:                                                      `todos: required`,
		`{"todos":"x"}`:                                           `todos: expected array, received string`,
		`{"todos":[],"extra":1}`:                                  `extra: unrecognized key`,
		`{"todos":[],"limit":1.5}`:                                `limit: expected integer`,
		`{"todos":[],"limit":11}`:                                 `limit: must be <= 10`,
		`{"todos":[{"content":"a","status":"pending","x":true}]}`: `todos[0].x: unrecognized key`,
	}
	for input, want := range cases {
		var v any
		if err := json.Unmarshal([]byte(input), &v); err != nil {
			t.Fatal(err)
		}
		err := s.Validate(v)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: unexpected %v", input, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: got %v, want %q", input, err, want)
		}
	}
}

func TestValidateAnyOf(t *testing.T) {
	var s Schema
	if err := json.Unmarshal([]byte(`{"anyOf":[{"type":"string"},{"type":"number"}]}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Validate("x") != nil || s.Validate(1.0) != nil {
		t.Error("a matching branch was rejected")
	}
	if s.Validate(true) == nil {
		t.Error("no branch matches a boolean")
	}
}
