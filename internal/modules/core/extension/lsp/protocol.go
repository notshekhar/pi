// Package lsp speaks the Language Server Protocol.
//
// Only the slice this extension uses: initialize, document sync,
// publishDiagnostics, and the navigation requests behind the `lsp` tool
// (definition, references, hover, symbols, implementation, call hierarchy).
// LSP is JSON-RPC framed with `Content-Length` headers over a child process's
// stdio.
package lsp

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// Severity levels.
const (
	SeverityError = 1
	SeverityWarn  = 2
	SeverityInfo  = 3
	SeverityHint  = 4
)

// SeverityLabel names a severity.
var SeverityLabel = map[int]string{
	SeverityError: "error", SeverityWarn: "warning",
	SeverityInfo: "info", SeverityHint: "hint",
}

// Position is zero-based, as the protocol has it. Every user-facing number is
// one-based, which is the single most common off-by-one in an LSP client — so
// the conversion happens at the edges and nowhere else.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a span.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Diagnostic is one reported problem.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// PublishDiagnosticsParams is the push notification's payload.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Location is a place in a file.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// LocationLink is the newer shape some servers return instead.
type LocationLink struct {
	TargetURI            string `json:"targetUri"`
	TargetRange          Range  `json:"targetRange"`
	TargetSelectionRange *Range `json:"targetSelectionRange,omitempty"`
}

// SymbolInformation is the flat symbol shape (workspace/symbol, and older
// documentSymbol servers).
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	ContainerName string   `json:"containerName,omitempty"`
	Location      Location `json:"location"`
}

// DocumentSymbol is the nested shape modern servers return.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// CallHierarchyItem is a callable.
type CallHierarchyItem struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	Detail         string `json:"detail,omitempty"`
	URI            string `json:"uri"`
	Range          Range  `json:"range"`
	SelectionRange Range  `json:"selectionRange"`
}

// CallHierarchyIncomingCall is a caller.
type CallHierarchyIncomingCall struct {
	From       CallHierarchyItem `json:"from"`
	FromRanges []Range           `json:"fromRanges"`
}

// CallHierarchyOutgoingCall is a callee.
type CallHierarchyOutgoingCall struct {
	To         CallHierarchyItem `json:"to"`
	FromRanges []Range           `json:"fromRanges"`
}

// SymbolKind names the protocol's 1-based enum.
var SymbolKind = map[int]string{
	1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class",
	6: "method", 7: "property", 8: "field", 9: "constructor", 10: "enum",
	11: "interface", 12: "function", 13: "variable", 14: "constant",
	15: "string", 16: "number", 17: "boolean", 18: "array", 19: "object",
	20: "key", 21: "null", 22: "enum-member", 23: "struct", 24: "event",
	25: "operator", 26: "type-parameter",
}

// PathToURI turns an absolute path into a file:// URI.
func PathToURI(absPath string) string {
	p := filepath.ToSlash(absPath)
	// Windows paths start with a drive letter, and the URI needs the extra
	// slash: file:///C:/x rather than file://C:/x.
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

// URIToPath turns a file:// URI back into a path.
func URIToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return strings.TrimPrefix(uri, "file://")
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p)
}

// HoverText flattens LSP's `MarkupContent | MarkedString | MarkedString[]`
// union into plain text.
//
// Servers disagree on which of the three they return, and the model only ever
// wants the prose — so all three are accepted and the markup is discarded.
func HoverText(contents any) string {
	one := func(value any) string {
		switch v := value.(type) {
		case string:
			return v
		case map[string]any:
			if s, ok := v["value"].(string); ok {
				return s
			}
		}
		return ""
	}
	var parts []string
	if list, ok := contents.([]any); ok {
		for _, item := range list {
			parts = append(parts, one(item))
		}
	} else {
		parts = append(parts, one(contents))
	}
	var kept []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n\n"))
}
