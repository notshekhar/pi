package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// The nine navigation operations behind the `lsp` tool, and how their results
// are rendered.
//
// Positions cross three coordinate systems: the model speaks 1-based
// line/character (what an editor shows, and what `read` output implies), LSP
// speaks 0-based, and results come back 0-based and must be shown 1-based
// again. The conversion happens HERE, once, at both ends — an off-by-one in
// an LSP client is invisible until it silently answers about the wrong symbol.

// Operations is every operation, in the order the tool documents them.
var Operations = []string{
	"goToDefinition",
	"findReferences",
	"hover",
	"documentSymbol",
	"workspaceSymbol",
	"goToImplementation",
	"prepareCallHierarchy",
	"incomingCalls",
	"outgoingCalls",
}

// positional are the operations that need a line and column; the rest work on
// a file or the whole workspace.
var positional = map[string]bool{
	"goToDefinition": true, "findReferences": true, "hover": true,
	"goToImplementation": true, "prepareCallHierarchy": true,
	"incomingCalls": true, "outgoingCalls": true,
}

// NeedsPosition reports whether an operation requires a position.
func NeedsPosition(operation string) bool { return positional[operation] }

// KnownOperation reports whether the name is one of the nine.
func KnownOperation(operation string) bool {
	for _, o := range Operations {
		if o == operation {
			return true
		}
	}
	return false
}

// capability is the server capability that answers each operation, so a
// refusal can say WHICH server cannot do it rather than returning nothing.
var capability = map[string]string{
	"goToDefinition": "definitionProvider", "findReferences": "referencesProvider",
	"hover": "hoverProvider", "documentSymbol": "documentSymbolProvider",
	"workspaceSymbol": "workspaceSymbolProvider", "goToImplementation": "implementationProvider",
	"prepareCallHierarchy": "callHierarchyProvider", "incomingCalls": "callHierarchyProvider",
	"outgoingCalls": "callHierarchyProvider",
}

// Input is one operation request. Line and Character are 1-based, as the
// model supplies them.
type Input struct {
	Operation string
	File      string
	Line      int
	Character int
	Query     string
}

// Result is what an operation produced.
type Result struct {
	// Lines is the rendered answer, empty when nothing was found.
	Lines []string
	// Unsupported is set when the server cannot answer this at all.
	Unsupported bool
}

// where is a `file:line:col` label, repo-relative where that is shorter.
func where(cwd, uri string, r Range) string {
	return fmt.Sprintf("%s:%d:%d", RelativeTo(cwd, URIToPath(uri)), r.Start.Line+1, r.Start.Character+1)
}

// relativeTo makes a path repo-relative, falling back to the absolute one.
//
// Retried through EvalSymlinks because a server answers with the path IT
// resolved, which is not always the one we asked about: on macOS /var is a
// symlink to /private/var, so a workspace anywhere under /tmp compares as
// unrelated to its own files and every result comes back as a long absolute
// path.
func RelativeTo(cwd, path string) string {
	if rel, ok := tryRel(cwd, path); ok {
		return rel
	}
	realCWD, err1 := filepath.EvalSymlinks(cwd)
	realPath, err2 := filepath.EvalSymlinks(path)
	if err1 != nil || err2 != nil {
		return path
	}
	if rel, ok := tryRel(realCWD, realPath); ok {
		return rel
	}
	return path
}

func tryRel(base, path string) (string, bool) {
	rel, err := filepath.Rel(base, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// toLocations accepts the three shapes a definition can come back as: one
// Location, a list of them, or the newer LocationLink.
//
// Servers genuinely differ, and a client that handled only one shape would
// look like it worked until someone pointed it at a different language.
func toLocations(raw json.RawMessage) []Location {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		items = []json.RawMessage{raw}
	}
	var out []Location
	for _, item := range items {
		var loc struct {
			URI                  string `json:"uri"`
			Range                *Range `json:"range"`
			TargetURI            string `json:"targetUri"`
			TargetRange          *Range `json:"targetRange"`
			TargetSelectionRange *Range `json:"targetSelectionRange"`
		}
		if err := json.Unmarshal(item, &loc); err != nil {
			continue
		}
		switch {
		case loc.URI != "" && loc.Range != nil:
			out = append(out, Location{URI: loc.URI, Range: *loc.Range})
		case loc.TargetURI != "" && loc.TargetRange != nil:
			r := *loc.TargetRange
			// The selection range is the symbol itself; the target range is
			// its whole body. The former is what a reader wants to jump to.
			if loc.TargetSelectionRange != nil {
				r = *loc.TargetSelectionRange
			}
			out = append(out, Location{URI: loc.TargetURI, Range: r})
		}
	}
	return out
}

func kindName(kind int) string {
	if name, ok := SymbolKind[kind]; ok {
		return name
	}
	return fmt.Sprintf("kind:%d", kind)
}

// renderOutline renders nested DocumentSymbols as an indented outline.
func renderOutline(symbols []DocumentSymbol, depth int) []string {
	var out []string
	for _, s := range symbols {
		detail := ""
		if s.Detail != "" {
			detail = " " + s.Detail
		}
		out = append(out, fmt.Sprintf("%s%d: %s %s%s",
			strings.Repeat("  ", depth), s.SelectionRange.Start.Line+1, kindName(s.Kind), s.Name, detail))
		if len(s.Children) > 0 {
			out = append(out, renderOutline(s.Children, depth+1)...)
		}
	}
	return out
}

func renderFlatSymbols(cwd string, symbols []SymbolInformation) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		container := ""
		if s.ContainerName != "" {
			container = " (in " + s.ContainerName + ")"
		}
		out = append(out, fmt.Sprintf("%s  %s %s%s",
			where(cwd, s.Location.URI, s.Location.Range), kindName(s.Kind), s.Name, container))
	}
	return out
}

func renderCallHierarchyItems(cwd string, items []CallHierarchyItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		detail := ""
		if item.Detail != "" {
			detail = " " + item.Detail
		}
		out = append(out, fmt.Sprintf("%s  %s %s%s",
			where(cwd, item.URI, item.SelectionRange), kindName(item.Kind), item.Name, detail))
	}
	return out
}

func positionParams(in Input) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": PathToURI(in.File)},
		// The one place the model's 1-based numbers become the protocol's
		// 0-based ones.
		"position": map[string]any{"line": in.Line - 1, "character": in.Character - 1},
	}
}

// prepare resolves the callable at a position into a call-hierarchy item.
func prepare(ctx context.Context, c *Client, in Input) []CallHierarchyItem {
	raw := c.Send(ctx, "textDocument/prepareCallHierarchy", positionParams(in), RequestTimeout)
	var items []CallHierarchyItem
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &items)
	}
	return items
}

// callHierarchy is two round trips: prepare resolves the symbol into an item,
// and that item is what the calls request takes.
func callHierarchy(ctx context.Context, c *Client, in Input, direction, cwd string) []string {
	items := prepare(ctx, c, in)
	if len(items) == 0 {
		return nil
	}
	method := "callHierarchy/outgoingCalls"
	arrow := "calls"
	if direction == "incomingCalls" {
		method, arrow = "callHierarchy/incomingCalls", "called by"
	}

	var out []string
	for _, item := range items {
		raw := c.Send(ctx, method, map[string]any{"item": item}, RequestTimeout)
		if len(raw) == 0 {
			continue
		}
		var calls []struct {
			From *CallHierarchyItem `json:"from"`
			To   *CallHierarchyItem `json:"to"`
		}
		if err := json.Unmarshal(raw, &calls); err != nil {
			continue
		}
		for _, call := range calls {
			target := call.To
			if direction == "incomingCalls" {
				target = call.From
			}
			if target == nil {
				continue
			}
			out = append(out, fmt.Sprintf("%s %s %s  %s",
				arrow, kindName(target.Kind), target.Name, where(cwd, target.URI, target.SelectionRange)))
		}
	}
	return out
}

// maxWorkspaceSymbols caps a workspace-wide search. An empty query asks for
// every symbol in the project, which on a large one is tens of thousands.
const maxWorkspaceSymbols = 100

// Run executes one operation against one server.
func Run(ctx context.Context, c *Client, in Input, cwd string) Result {
	if !c.Supports(capability[in.Operation]) {
		return Result{Unsupported: true}
	}

	switch in.Operation {
	case "goToDefinition":
		raw := c.Send(ctx, "textDocument/definition", positionParams(in), RequestTimeout)
		return Result{Lines: locationLines(cwd, raw)}

	case "goToImplementation":
		raw := c.Send(ctx, "textDocument/implementation", positionParams(in), RequestTimeout)
		return Result{Lines: locationLines(cwd, raw)}

	case "findReferences":
		params := positionParams(in)
		params["context"] = map[string]any{"includeDeclaration": true}
		raw := c.Send(ctx, "textDocument/references", params, RequestTimeout)
		return Result{Lines: locationLines(cwd, raw)}

	case "hover":
		raw := c.Send(ctx, "textDocument/hover", positionParams(in), RequestTimeout)
		if len(raw) == 0 {
			return Result{}
		}
		var hover struct {
			Contents any `json:"contents"`
		}
		if err := json.Unmarshal(raw, &hover); err != nil {
			return Result{}
		}
		text := HoverText(hover.Contents)
		if text == "" {
			return Result{}
		}
		return Result{Lines: strings.Split(text, "\n")}

	case "documentSymbol":
		raw := c.Send(ctx, "textDocument/documentSymbol", map[string]any{
			"textDocument": map[string]any{"uri": PathToURI(in.File)},
		}, RequestTimeout)
		if len(raw) == 0 {
			return Result{}
		}
		// Servers return either the nested or the flat shape. The nested one
		// is distinguished by `selectionRange`, and both are worth rendering
		// natively — flattening the nested one would throw away the structure
		// that makes an outline readable.
		var nested []DocumentSymbol
		if err := json.Unmarshal(raw, &nested); err == nil && len(nested) > 0 && nested[0].SelectionRange != (Range{}) {
			return Result{Lines: renderOutline(nested, 0)}
		}
		var flat []SymbolInformation
		if err := json.Unmarshal(raw, &flat); err != nil {
			return Result{}
		}
		return Result{Lines: renderFlatSymbols(cwd, flat)}

	case "workspaceSymbol":
		raw := c.Send(ctx, "workspace/symbol", map[string]any{"query": in.Query}, RequestTimeout)
		var symbols []SymbolInformation
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &symbols)
		}
		if len(symbols) > maxWorkspaceSymbols {
			symbols = symbols[:maxWorkspaceSymbols]
		}
		return Result{Lines: renderFlatSymbols(cwd, symbols)}

	case "prepareCallHierarchy":
		return Result{Lines: renderCallHierarchyItems(cwd, prepare(ctx, c, in))}

	case "incomingCalls", "outgoingCalls":
		return Result{Lines: callHierarchy(ctx, c, in, in.Operation, cwd)}
	}
	return Result{}
}

func locationLines(cwd string, raw json.RawMessage) []string {
	locations := toLocations(raw)
	out := make([]string, 0, len(locations))
	for _, l := range locations {
		out = append(out, where(cwd, l.URI, l.Range))
	}
	return out
}
