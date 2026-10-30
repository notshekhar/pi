package providertest

import (
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
)

func TestViolations(t *testing.T) {
	good := []provider.StreamPart{
		provider.StreamStart{},
		provider.ReasoningStart{ID: "0"}, provider.ReasoningDelta{ID: "0", Delta: "x"}, provider.ReasoningEnd{ID: "0"},
		provider.TextStart{ID: "0"}, provider.TextDelta{ID: "0", Delta: "y"}, provider.TextEnd{ID: "0"},
		provider.ToolInputStart{ID: "c"}, provider.ToolInputDelta{ID: "c", Delta: "{}"}, provider.ToolInputEnd{ID: "c"},
		provider.ToolCall{ToolCallID: "c"},
		provider.Finish{},
	}
	if v := Violations(good); len(v) != 0 {
		t.Errorf("a good stream: %v", v)
	}
	for name, c := range map[string]struct {
		parts []provider.StreamPart
		want  string
	}{
		"delta without start": {[]provider.StreamPart{provider.TextDelta{ID: "a"}, provider.Finish{}}, "not open"},
		"never ended":         {[]provider.StreamPart{provider.ReasoningStart{ID: "a"}, provider.Finish{}}, "never ended"},
		"call before input":   {[]provider.StreamPart{provider.ToolInputStart{ID: "c"}, provider.ToolCall{ToolCallID: "c"}, provider.ToolInputEnd{ID: "c"}, provider.Finish{}}, "before its input ended"},
		"no finish":           {[]provider.StreamPart{provider.StreamStart{}}, "no Finish"},
		"after finish":        {[]provider.StreamPart{provider.Finish{}, provider.TextStart{ID: "a"}, provider.TextEnd{ID: "a"}}, "after the Finish"},
	} {
		if v := Violations(c.parts); !strings.Contains(strings.Join(v, "\n"), c.want) {
			t.Errorf("%s: %v", name, v)
		}
	}
}
