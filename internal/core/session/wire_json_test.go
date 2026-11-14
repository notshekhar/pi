package session

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/ai/provider"
)

// encoding/json/v2's omitempty drops a raw value that is an empty object or
// an empty string; v1 only dropped a missing one. loop expects a tool call's
// "input" and a text output's "value" to be there even when empty.
func TestEncodeContentKeepsEmptyRawValues(t *testing.T) {
	_, call, err := EncodeContent(provider.AssistantMessage{Content: []provider.AssistantPart{
		provider.ToolCallPart{ToolCallID: "c1", ToolName: "ls", Input: map[string]any{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(call), `"input":{}`) {
		t.Errorf("tool call lost its empty input: %s", call)
	}

	_, result, err := EncodeContent(provider.ToolMessage{Content: []provider.ToolPart{
		provider.ToolResultPart{ToolCallID: "c1", ToolName: "ls", Output: provider.ToolOutputText{Value: ""}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"output":{"type":"text","value":""}`) {
		t.Errorf("empty text output lost its value: %s", result)
	}
}

// loop's TypeScript writes a lone surrogate when it slices a string mid-pair,
// and tool output is raw bytes. v1 accepted both; v2 rejects them unless told
// otherwise, which would make a loop session unloadable in pi.
func TestEntryRoundTripsLoopsInvalidUnicode(t *testing.T) {
	payload := `{"type":"message","ts":1,"role":"tool","content":"cut \ud83d here","id":"a1b2c3d4","parentId":null}`
	var e Entry
	if err := json.Unmarshal([]byte(payload), &e, jsonopt.Read); err != nil {
		t.Fatalf("loop's payload did not load: %v", err)
	}
	out, err := json.Marshal(e, jsonopt.Write)
	if err != nil {
		t.Fatalf("re-encoding failed: %v", err)
	}
	if !strings.Contains(string(out), `"content":"cut \ud83d here"`) {
		t.Errorf("the lone surrogate was not kept as loop wrote it: %s", out)
	}

	raw := jsontext.Value("\"bad \xff byte\"")
	e = Entry{Type: TypeMessage, Role: "tool", Content: raw}
	if _, err := json.Marshal(e, jsonopt.Write); err != nil {
		t.Errorf("an entry carrying invalid UTF-8 could not be saved: %v", err)
	}
}
