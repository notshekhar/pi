package tui

import (
	"strings"
	"testing"
)

// Queued input renders between the panels and the editor, one row each:
// loop's PendingMessageLine — " queued i/n: " then the message, whitespace
// collapsed onto one line and cut at the edge.
func TestQueuedRowsSitAboveTheEditor(t *testing.T) {
	a := &App{Conversation: NewConversation(""), editor: NewEditor(), theme: testTheme(), size: Size{Rows: 30, Cols: 40}}
	a.SetQueue([]string{"say ALPHA", "a long\nmultiline   message that will not fit in forty columns"})
	rows := a.renderChrome(40)
	var plain []string
	for _, r := range rows {
		plain = append(plain, stripANSI(r))
	}
	first, second := -1, -1
	for i, r := range plain {
		if strings.HasPrefix(r, " queued 1/2: say ALPHA") {
			first = i
		}
		if strings.HasPrefix(r, " queued 2/2: a long multiline") {
			second = i
		}
	}
	if first < 0 || second != first+1 {
		t.Fatalf("queue rows missing or out of order:\n%s", strings.Join(plain, "\n"))
	}
	if w := visibleWidth(rows[second]); w > 40 {
		t.Fatalf("queued row is %d cells, wider than the screen", w)
	}
	if !strings.HasPrefix(plain[second+1], "───") {
		t.Fatalf("the editor's top rule should follow the queue, got %q", plain[second+1])
	}
}
