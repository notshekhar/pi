package extension

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/core/extension/lsp"
	"github.com/notshekhar/pi/internal/modules/core/tools"
)

// The LSP extension — code intelligence, in two halves.
//
//  1. AMBIENT DIAGNOSTICS. After a write or an edit, the changed file is run
//     past every language server that handles it and the errors are appended
//     to the tool result. This is the half that changes behaviour most: the
//     agent finds out what it just broke without being asked to check, and it
//     costs nothing when the file is clean.
//  2. THE `lsp` TOOL. Nine navigation operations that answer semantically
//     where grep can only answer textually — where a symbol is defined, who
//     calls it, what implements it.

// reportedSeverity is errors only. Warnings are mostly style, and the agent
// acts on everything it is shown — a turn spent silencing a lint suggestion is
// a turn not spent on the task.
const reportedSeverity = lsp.SeverityError

// maxPerFile caps the report, so one catastrophically broken file cannot
// flood the turn.
const maxPerFile = 20

type lspExt struct {
	store Store

	mu       sync.Mutex
	managers map[string]*lsp.Manager
	cwd      string
}

func init() { Register(&lspExt{managers: map[string]*lsp.Manager{}}) }

func (*lspExt) Name() string { return "lsp" }
func (*lspExt) About() string {
	return "Code intelligence: type errors after write/edit, and an `lsp` tool for definitions, references, hover, symbols and call hierarchy."
}

func (x *lspExt) UseStore(s Store) { x.store = s }

// manager is the client pool for a workspace, started on first use.
func (x *lspExt) manager(cwd string) *lsp.Manager {
	x.mu.Lock()
	defer x.mu.Unlock()
	if m := x.managers[cwd]; m != nil {
		return m
	}
	m := lsp.NewManager(cwd)
	x.managers[cwd] = m
	return m
}

// Shutdown reaps every server. Called when the session ends — a leaked
// language server holds a whole toolchain's memory.
func (x *lspExt) Shutdown() {
	x.mu.Lock()
	managers := make([]*lsp.Manager, 0, len(x.managers))
	for _, m := range x.managers {
		managers = append(managers, m)
	}
	x.managers = map[string]*lsp.Manager{}
	x.mu.Unlock()
	for _, m := range managers {
		m.Shutdown()
	}
}

// ── half one: ambient diagnostics ───────────────────────────────────────────

// AugmentedTools is the pair whose results carry diagnostics, plus `read` —
// which appends nothing and is here only to WARM the server for the file, so
// the diagnostics after the next edit are already waiting instead of paying
// startup.
func (*lspExt) AugmentedTools() []string { return []string{"write", "edit", "read"} }

func (x *lspExt) Augment(ctx context.Context, tool, _ string, args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		return ""
	}
	cwd := x.workspace()
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}

	if tool == "read" {
		// Warm the servers and say nothing. Started in the background: a read
		// must not wait on a language server booting.
		go x.manager(cwd).ClientsFor(context.WithoutCancel(ctx), abs)
		return ""
	}

	diagnostics := x.manager(cwd).Diagnose(ctx, abs)
	block := reportDiagnostics(cwd, abs, diagnostics)
	if block == "" {
		return ""
	}
	return "\n\nLSP errors detected in this file, please fix:\n" + block
}

// reportDiagnostics is the `<diagnostics>` block, empty when the file is clean.
func reportDiagnostics(cwd, absPath string, diagnostics []lsp.Diagnostic) string {
	var errors []lsp.Diagnostic
	for _, d := range diagnostics {
		severity := d.Severity
		if severity == 0 {
			severity = lsp.SeverityError
		}
		if severity == reportedSeverity {
			errors = append(errors, d)
		}
	}
	if len(errors) == 0 {
		return ""
	}

	shown := errors
	suffix := ""
	if len(shown) > maxPerFile {
		suffix = fmt.Sprintf("\n... and %d more", len(shown)-maxPerFile)
		shown = shown[:maxPerFile]
	}

	rel := lsp.RelativeTo(cwd, absPath)
	lines := make([]string, 0, len(shown))
	for _, d := range shown {
		lines = append(lines, prettyDiagnostic(d))
	}
	return fmt.Sprintf("<diagnostics file=%q>\n%s%s\n</diagnostics>", rel, strings.Join(lines, "\n"), suffix)
}

var whitespaceRun = regexp.MustCompile(`\s+`)

func prettyDiagnostic(d lsp.Diagnostic) string {
	severity := d.Severity
	if severity == 0 {
		severity = lsp.SeverityError
	}
	message := strings.TrimSpace(whitespaceRun.ReplaceAllString(d.Message, " "))
	return fmt.Sprintf("%s [%d:%d] %s",
		strings.ToUpper(lsp.SeverityLabel[severity]),
		d.Range.Start.Line+1, d.Range.Start.Character+1, message)
}

// ── half two: the tool ──────────────────────────────────────────────────────

// workspace is the directory the tools are running in.
func (x *lspExt) workspace() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.cwd != "" {
		return x.cwd
	}
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

// Tools contributes the `lsp` tool.
func (x *lspExt) Tools(ctx *tools.Context) []ai.Tool {
	x.mu.Lock()
	x.cwd = ctx.CWD
	x.mu.Unlock()
	return []ai.Tool{ai.NewTool("lsp", operationHelp, x.run)}
}

// lspArgs is the tool's input.
type lspArgs struct {
	Operation string `json:"operation" jsonschema:"description=Which question to answer — goToDefinition, findReferences, hover, documentSymbol, workspaceSymbol, goToImplementation, prepareCallHierarchy, incomingCalls, outgoingCalls"`
	FilePath  string `json:"filePath,omitempty" jsonschema:"description=Path to the file. For workspaceSymbol this may be a directory or omitted — it only selects which language server answers."`
	Line      int    `json:"line,omitempty" jsonschema:"description=Line number, 1-based, exactly as read and grep report it. Required for position operations."`
	Symbol    string `json:"symbol,omitempty" jsonschema:"description=The name at that line to point at, e.g. runTurn — its column is resolved for you, so you never have to count characters. First occurrence on the line wins. Preferred over character."`
	Character int    `json:"character,omitempty" jsonschema:"description=Column, 1-based. Only needed when you already know the exact column, e.g. to reach the second occurrence of a name on one line."`
	Query     string `json:"query,omitempty" jsonschema:"description=Search query for workspaceSymbol. An empty string requests every symbol."`
}

func (x *lspExt) run(ctx context.Context, a lspArgs) (ai.ToolResult, error) {
	if !lsp.KnownOperation(a.Operation) {
		return ai.ToolErrorf("[unknown operation %q. options: %s]",
			a.Operation, strings.Join(lsp.Operations, ", ")), nil
	}
	root := x.workspace()

	// workspaceSymbol is about a whole project, so the path is only a server
	// selector and may be left out entirely.
	target := a.FilePath
	if target == "" {
		if a.Operation != "workspaceSymbol" {
			return ai.ToolErrorf("[%s needs filePath]", a.Operation), nil
		}
		target = "."
	}
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}

	column := a.Character
	if lsp.NeedsPosition(a.Operation) {
		if a.Line == 0 {
			return ai.ToolErrorf("[%s needs line, plus symbol (the name at that line) or character]", a.Operation), nil
		}
		if a.Symbol != "" {
			resolved, err := resolveColumn(abs, a.Line, a.Symbol)
			if err != nil {
				return ai.ToolError(err.Error()), nil
			}
			column = resolved
		}
		if column == 0 {
			return ai.ToolErrorf("[%s needs symbol (the name at line %d) or character (1-based column)]",
				a.Operation, a.Line), nil
		}
	}

	manager := x.manager(root)
	clients := manager.ClientsFor(ctx, abs)
	if len(clients) == 0 {
		ready, missing := manager.Available(abs)
		_ = ready
		if len(missing) > 0 {
			return ai.ToolErrorf(
				"[no language server running for %s. It is handled by %s — install one and it will be used.]",
				target, strings.Join(missing, ", ")), nil
		}
		return ai.ToolErrorf("[no language server available for %s]", target), nil
	}

	// A request only answers about an OPEN document, workspace/symbol
	// included: its index is built from the loaded project.
	for _, c := range clients {
		c.OpenDocument(abs, "")
	}

	in := lsp.Input{
		Operation: a.Operation, File: abs,
		Line: maxInt(a.Line, 1), Character: maxInt(column, 1), Query: a.Query,
	}

	var lines []string
	unsupported := 0
	for _, c := range clients {
		result := lsp.Run(ctx, c, in, root)
		if result.Unsupported {
			unsupported++
			continue
		}
		lines = append(lines, result.Lines...)
	}

	if len(lines) == 0 {
		if unsupported == len(clients) {
			return ai.ToolErrorf("[%s is not supported by the language server for this file]", a.Operation), nil
		}
		return ai.ToolTextf("No results found for %s", a.Operation), nil
	}
	// Several servers answering one file repeat each other.
	return ai.ToolText(strings.Join(unique(lines), "\n")), nil
}

func unique(lines []string) []string {
	seen := make(map[string]bool, len(lines))
	out := lines[:0:0]
	for _, l := range lines {
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// resolveColumn turns a symbol name into a 1-based column on a line.
//
// The model is handed line numbers by `read` and `grep` but never columns, so
// asking it for a character asks it to count into a line by eye. A wrong
// guess lands on whitespace and comes back "No results found", which reads
// like the tool failing rather than the position being off — and one of those
// is enough to send it back to grep for the rest of the session.
func resolveColumn(file string, line int, symbol string) (int, error) {
	body, err := os.ReadFile(file)
	if err != nil {
		return 0, fmt.Errorf("[cannot read %s to locate %q]", file, symbol)
	}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	if line < 1 || line > len(lines) {
		return 0, fmt.Errorf("[line %d is past the end of %s (%d lines)]", line, file, len(lines))
	}
	target := lines[line-1]

	// Word boundaries stop `run` matching inside `runTurn` — but only where
	// the symbol's own edges are word characters. Bolting \b onto `#private`
	// or `foo!` would make them match nothing at all.
	pattern := regexp.QuoteMeta(symbol)
	if isWordChar(rune(symbol[0])) {
		pattern = `\b` + pattern
	}
	if isWordChar(rune(symbol[len(symbol)-1])) {
		pattern += `\b`
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0, fmt.Errorf("[cannot search for %q]", symbol)
	}
	at := re.FindStringIndex(target)
	if at == nil {
		return 0, fmt.Errorf("[%q is not on line %d, which reads: %s]", symbol, line, strings.TrimSpace(target))
	}
	// Byte offset to a 1-based CHARACTER column: a line with a non-ASCII
	// character before the symbol would otherwise point the server past it.
	return len([]rune(target[:at[0]])) + 1, nil
}

func isWordChar(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

const operationHelp = `Answer a question about code exactly, from the compiler's own model of the
project. Use it whenever you need to be precisely right about a symbol rather
than approximately right:

- Where is this defined? -> goToDefinition
- Who uses this / what breaks if I change it? -> findReferences, incomingCalls
- What type is this, what does it do? -> hover (signature, type, doc comment)
- What implements this interface or abstract method? -> goToImplementation
- What does this function call? -> outgoingCalls
- What is in this file? -> documentSymbol (outline of every symbol, with lines)
- Where is the symbol named X, anywhere in the project? -> workspaceSymbol

Reach for it whenever precision is what the task needs: before renaming a
symbol, changing a function signature, deleting something that looks dead, or
tracing how a value flows. Every answer resolves the real symbol, so it covers
that symbol's re-exports and aliases and leaves out everything that merely
shares its spelling — and incomingCalls answers a question no text search can
express.

Positions: pass filePath, line (1-based, exactly as read and grep report it)
and symbol — the name at that line — and the column is resolved for you, so
never count characters. Pass character only when you already know the exact
1-based column; symbol wins if both are given.

documentSymbol needs only filePath. workspaceSymbol needs only query (an empty
string requests every symbol); its filePath merely picks which server answers
and may be a directory or omitted. prepareCallHierarchy just resolves the
callable at a position — incomingCalls and outgoingCalls do that step
themselves, so you rarely want it directly.

A language server must be installed for the file type; if none is available
the tool says so.`
