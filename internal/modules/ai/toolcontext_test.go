package ai

import (
	"context"
	"testing"
)

func TestToolCallIDRoundTrips(t *testing.T) {
	ctx := withToolCallID(context.Background(), "call_abc")
	if got := ToolCallID(ctx); got != "call_abc" {
		t.Errorf("ToolCallID = %q, want call_abc", got)
	}
}

// A tool asking outside a call must get an empty string, not a panic.
func TestToolCallIDOutsideACallIsEmpty(t *testing.T) {
	if got := ToolCallID(context.Background()); got != "" {
		t.Errorf("ToolCallID = %q, want empty", got)
	}
}
