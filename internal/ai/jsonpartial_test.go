package ai

import (
	"encoding/json"
	"testing"
)

func TestCompleteJSON(t *testing.T) {
	cases := []struct {
		name    string
		partial string
		want    string
		ok      bool
	}{
		{"empty", "", "", false},
		{"whitespace only", "  \n", "", false},
		{"complete object", `{"a":1}`, `{"a":1}`, true},
		{"opening brace", `{`, `{}`, true},
		{"partial key", `{"su`, `{}`, true},
		{"key without value", `{"a":`, `{}`, true},
		{"partial string value", `{"a":"he`, `{"a":"he"}`, true},
		{"complete pair", `{"a":"hi"`, `{"a":"hi"}`, true},
		{"trailing comma", `{"a":"hi",`, `{"a":"hi"}`, true},
		{"nested", `{"a":{"b":[1,2`, `{"a":{"b":[1]}}`, true},
		{"array of objects", `[{"a":1},{"a":`, `[{"a":1},{}]`, true},
		// A number at the end may still be growing: 1 could become 12.
		{"growing number", `{"a":1`, `{}`, true},
		{"number then comma", `{"a":1,`, `{"a":1}`, true},
		{"escaped quote in value", `{"a":"say \"hi`, `{"a":"say \"hi"}`, true},
		// Closing on a lone backslash would escape the quote we add.
		{"dangling backslash", `{"a":"path\`, `{"a":"path"}`, true},
		{"partial unicode escape", `{"a":"caf\u00`, `{"a":"caf"}`, true},
		{"boolean pending", `{"a":tru`, `{}`, true},
		{"boolean done", `{"a":true,`, `{"a":true}`, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := completeJSON(tc.partial)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tc.ok, got)
			}
			if got != tc.want {
				t.Errorf("completeJSON(%q) = %q, want %q", tc.partial, got, tc.want)
			}
		})
	}
}

// TestCompleteJSONAlwaysParses is the property that matters: whatever prefix
// arrives, the completion has to be something json.Unmarshal accepts.
func TestCompleteJSONAlwaysParses(t *testing.T) {
	const document = `{"summary":"a \"quoted\" café","score":42,"tags":["go","json"],"nested":{"ok":true,"n":null}}`

	for i := 1; i <= len(document); i++ {
		got, ok := completeJSON(document[:i])
		if !ok {
			continue
		}
		if !jsonValid(got) {
			t.Fatalf("prefix %d (%q) completed to invalid JSON %q", i, document[:i], got)
		}
	}
}

// jsonValid reports whether s parses, kept separate so the failure message
// above stays readable.
func jsonValid(s string) bool {
	var v any
	return json.Unmarshal([]byte(s), &v) == nil
}
