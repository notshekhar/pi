package main

import (
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/core/extension"
	"github.com/notshekhar/pi/internal/modules/core/tools"
)

// The lsp tool must actually reach the agent when the extension is enabled —
// the interface existing and nothing consulting it is the exact failure this
// whole pass was about.
func TestLspToolIsContributed(t *testing.T) {
	var lspExtension extension.Extension
	for _, e := range extension.All() {
		if e.Name() == "lsp" {
			lspExtension = e
		}
	}
	if lspExtension == nil {
		t.Fatal("no lsp extension registered")
	}
	ctx := &tools.Context{CWD: t.TempDir(), Registry: tools.NewRegistry()}
	added := extension.ToolsFrom([]extension.Extension{lspExtension}, ctx)
	names := []string{}
	for _, tool := range added {
		names = append(names, tool.Name())
	}
	if len(added) != 1 || added[0].Name() != "lsp" {
		t.Fatalf("contributed %v, want just lsp", names)
	}
	if !strings.Contains(added[0].Description(), "goToDefinition") {
		t.Error("the tool description does not document the operations")
	}
	if added[0].InputSchema() == nil {
		t.Error("the tool has no input schema")
	}
}

// And it must WRAP write/edit/read so diagnostics can be appended.
func TestLspWrapsTheFileTools(t *testing.T) {
	var lspExtension extension.Extension
	for _, e := range extension.All() {
		if e.Name() == "lsp" {
			lspExtension = e
		}
	}
	ctx := &tools.Context{CWD: t.TempDir(), Registry: tools.NewRegistry()}
	base := tools.All(ctx)
	wrapped := extension.WrapTools([]extension.Extension{lspExtension}, base)

	changed := 0
	for i := range base {
		if &base[i] != &wrapped[i] || base[i] != wrapped[i] {
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("no tool was wrapped")
	}
	// Wrapping must not change the tool set the model sees.
	if len(wrapped) != len(base) {
		t.Errorf("wrapping changed the tool count: %d -> %d", len(base), len(wrapped))
	}
	for i := range base {
		if wrapped[i].Name() != base[i].Name() {
			t.Errorf("wrapping reordered the tools at %d", i)
		}
	}
}
