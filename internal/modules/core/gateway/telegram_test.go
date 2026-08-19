package gateway

import (
	"strings"
	"testing"
)

func TestNewTelegramNeedsAToken(t *testing.T) {
	if NewTelegram("", 0) != nil {
		t.Error("an empty token should disable the gateway")
	}
	if NewTelegram("123:abc", 0) == nil {
		t.Error("a token should enable it")
	}
}

func TestSplitPrefersLineBoundaries(t *testing.T) {
	text := strings.Repeat("line of text\n", 500)
	chunks := split(text, 100)
	if len(chunks) < 2 {
		t.Fatal("long text was not split")
	}
	for _, c := range chunks {
		if len(c) > 100 {
			t.Errorf("chunk is %d bytes, over the limit", len(c))
		}
		// A cut mid-line would leave a fragment; every chunk should end on a
		// complete line.
		if strings.HasSuffix(c, "line of tex") {
			t.Errorf("chunk severed a line: %q", c)
		}
	}
	if joined := strings.ReplaceAll(strings.Join(chunks, "\n"), "\n\n", "\n"); !strings.HasPrefix(joined, "line of text") {
		t.Error("split lost content")
	}
}

func TestSplitLeavesShortTextAlone(t *testing.T) {
	if got := split("short", 100); len(got) != 1 || got[0] != "short" {
		t.Errorf("split = %v", got)
	}
}
