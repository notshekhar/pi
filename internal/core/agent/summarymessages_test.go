package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/session"
)

// The compaction summary is sent the span's own messages (loop's
// summary-messages.test.ts), not one block of serialized text.

func storedEntry(t *testing.T, msg provider.Message) session.Entry {
	t.Helper()
	role, content, err := session.EncodeContent(msg)
	if err != nil {
		t.Fatal(err)
	}
	return session.Entry{Type: session.TypeMessage, Role: role, Content: content}
}

func callRead(id string) provider.Message {
	return provider.AssistantMessage{Content: []provider.AssistantPart{
		provider.ReasoningPart{Text: "SECRET"},
		provider.ToolCallPart{ToolCallID: id, ToolName: "read", Input: map[string]any{"path": id + ".ts"}},
	}}
}

func readResult(id, out string) provider.Message {
	return provider.ToolMessage{Content: []provider.ToolPart{
		provider.ToolResultPart{ToolCallID: id, ToolName: "read", Output: provider.ToolOutputText{Value: out}},
	}}
}

func userSays(text string) provider.Message {
	return provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: text}}}
}

func TestSummaryMessagesFilterTheSpan(t *testing.T) {
	msgs := toSummaryMessages([]session.Entry{
		storedEntry(t, userSays("fix the parser")),
		storedEntry(t, callRead("a")),
		storedEntry(t, readResult("a", "HEAD"+strings.Repeat("x", 5_000)+"TAIL")),
	})
	if len(msgs) != 3 {
		t.Fatalf("messages = %d", len(msgs))
	}
	all, _ := json.Marshal(msgs[1].parts)
	if strings.Contains(string(all), "SECRET") {
		t.Error("reasoning reached the summarizer")
	}
	res, _ := json.Marshal(msgs[2].parts)
	if !strings.Contains(string(res), "more characters truncated") || strings.Contains(string(res), "TAIL") {
		t.Errorf("tool result not capped: %.200s", res)
	}
	for _, m := range msgs {
		if _, ok := m.message(); !ok {
			t.Errorf("%s message does not decode", m.role)
		}
	}
}

func TestChunkMessagesKeepsCallsWithResults(t *testing.T) {
	var span []session.Entry
	for _, id := range []string{"a", "b", "c"} {
		span = append(span, storedEntry(t, userSays(strings.Repeat("u", 300))),
			storedEntry(t, callRead(id)), storedEntry(t, readResult(id, strings.Repeat("r", 300))))
	}
	chunks := chunkMessages(toSummaryMessages(span), 900)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	total := 0
	for _, c := range chunks {
		if c[0].role == "tool" {
			t.Error("a chunk starts on a tool result")
		}
		total += len(c)
	}
	if total != 9 {
		t.Errorf("messages lost in chunking: %d", total)
	}
	big := chunkMessages([]summaryMessage{{role: "user", text: strings.Repeat("z", 50_000)}}, 4_000)
	if big[0][0].size() >= 4_000 {
		t.Errorf("an oversized message was not clipped: %d", big[0][0].size())
	}
}

// End to end against the fake gateway: the summary request carries the
// span as tool_use/tool_result blocks, declares the tools with tool choice
// none, and ends on the instructions.
func TestCompactionSendsRealMessages(t *testing.T) {
	r, g, _ := newTurn(t)
	var entries []session.Entry
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		entries = append(entries,
			storedEntry(t, userSays("request "+id+" "+strings.Repeat("w", 30_000))),
			storedEntry(t, callRead(id)),
			storedEntry(t, readResult(id, "contents of "+id)))
	}
	if err := r.Session.AppendAll(entries...); err != nil {
		t.Fatal(err)
	}
	if _, err := Compact(context.Background(), r, CompactOptions{}); err != nil {
		t.Fatal(err)
	}
	bodies := g.requests()
	if len(bodies) == 0 {
		t.Fatal("no summary request")
	}
	body := bodies[0]
	raw, _ := json.Marshal(body["messages"])
	for _, want := range []string{`"type":"tool_use"`, `"type":"tool_result"`, "request a ", "The conversation to summarize follows."} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("summary messages lack %s", want)
		}
	}
	if strings.Contains(string(raw), "<conversation>") || strings.Contains(string(raw), "[User]:") {
		t.Error("the span was still serialized to text")
	}
	if tc, _ := body["tool_choice"].(map[string]any); tc["type"] != "none" {
		t.Errorf("tool_choice = %v", body["tool_choice"])
	}
	if tools, _ := body["tools"].([]any); len(tools) != 1 {
		t.Errorf("tools = %v", body["tools"])
	}
	msgs, _ := body["messages"].([]any)
	last, _ := json.Marshal(msgs[len(msgs)-1])
	if !strings.Contains(string(last), "## Critical Context") {
		t.Errorf("the instructions are not last: %.200s", last)
	}
}

// /compact <instructions> reaches the model as its own user message, after
// the standing instructions.
func TestCompactInstructionsAreTheirOwnMessage(t *testing.T) {
	msgs := summaryRequest(nil, "STANDING", "", "keep the API changes")
	last, ok := msgs[len(msgs)-1].(provider.UserMessage)
	if !ok {
		t.Fatalf("last message is %T", msgs[len(msgs)-1])
	}
	text := last.Content[0].(provider.TextPart).Text
	if !strings.Contains(text, "keep the API changes") {
		t.Errorf("last message = %q", text)
	}
	prev := msgs[len(msgs)-2].(provider.UserMessage).Content[0].(provider.TextPart).Text
	if prev != "STANDING" {
		t.Errorf("standing instructions = %q", prev)
	}
	if got := len(summaryRequest(nil, "STANDING", "", "")); got != 2 {
		t.Errorf("no instructions should add no message, got %d", got)
	}
}
