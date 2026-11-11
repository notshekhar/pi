package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A word that is not a command is refused with a suggestion, instead of
// opening a chat — loop's #1 complaint before spec.ts existed.
func TestUnknownCommandIsRefusedWithASuggestion(t *testing.T) {
	r := newPiHome(t).mustRun(t, 1, "sesions")
	if !strings.Contains(r.stderr, "unknown command \"sesions\" — did you mean `sessions`?") {
		t.Fatalf("stderr: %q", r.stderr)
	}
}

// Each command accepts only its own flags; a real flag on the wrong command
// says so rather than being ignored.
func TestFlagsAreCheckedPerCommand(t *testing.T) {
	h := newPiHome(t)
	if r := h.mustRun(t, 1, "run", "--all", "hi"); !strings.Contains(r.stderr, "--all is not a flag of `run`") {
		t.Fatalf("stderr: %q", r.stderr)
	}
	if r := h.mustRun(t, 1, "run", "--output-format", "xml", "hi"); !strings.Contains(r.stderr, `--output-format must be one of: text, json, stream-json (got "xml")`) {
		t.Fatalf("stderr: %q", r.stderr)
	}
}

func TestCompletionScriptsOfferEveryCommand(t *testing.T) {
	h := newPiHome(t)
	for _, shell := range completionShells {
		r := h.mustRun(t, 0, "completion", shell)
		for _, c := range commandSpecs {
			if !strings.Contains(r.stdout, c.name) {
				t.Errorf("%s completion misses %q", shell, c.name)
			}
		}
		// The hint goes to stderr so a redirect yields a loadable file.
		if strings.Contains(r.stdout, "# write it") || !strings.HasPrefix(r.stderr, "# ") {
			t.Errorf("%s: install hint on the wrong stream\nstdout: %s\nstderr: %s", shell, r.stdout, r.stderr)
		}
	}
	if r := h.mustRun(t, 1, "completion", "tcsh"); r.stderr != "Usage: pi completion <bash|zsh|fish>\n" {
		t.Fatalf("stderr: %q", r.stderr)
	}
}

func TestManPathPrintsTheRoffSource(t *testing.T) {
	r := newPiHome(t).mustRun(t, 0, "man", "--path")
	if !strings.HasPrefix(r.stdout, ".TH PI 1 ") || !strings.Contains(r.stdout, `.B \-\-output\-format \fIvalue\fR`) {
		t.Fatalf("man page:\n%s", r.stdout)
	}
}

func TestManInstallWritesToTheUserManpath(t *testing.T) {
	h := newPiHome(t)
	h.mustRun(t, 0, "man", "--install")
	if _, err := os.Stat(filepath.Join(h.home, ".local", "share", "man", "man1", "pi.1")); err != nil {
		t.Fatal(err)
	}
}

func TestModelsListsTheCatalogTabSeparated(t *testing.T) {
	r := newPiHome(t).mustRun(t, 0, "models")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) < 10 {
		t.Fatalf("only %d models", len(lines))
	}
	for _, l := range lines {
		if f := strings.Split(l, "\t"); len(f) != 3 || !strings.HasPrefix(f[1], "ctx:") || !strings.HasPrefix(f[2], "$") {
			t.Fatalf("malformed line %q", l)
		}
	}
}

func TestWhoamiAndLogoutOnAnEmptyHome(t *testing.T) {
	h := newPiHome(t)
	if r := h.mustRun(t, 0, "whoami"); r.stdout != "Active provider: none\nAuthorized: none\n" {
		t.Fatalf("whoami: %q", r.stdout)
	}
	// Nothing stored is already logged out, as loop reports it.
	if r := h.mustRun(t, 0, "logout", "openai"); r.stdout != "Logged out of openai.\n" {
		t.Fatalf("logout: %q", r.stdout)
	}
}

func TestLoginStoresAKeyReadFromStdin(t *testing.T) {
	h := newPiHome(t)
	r := h.run(t, "sk-test\n", "login", "openai")
	if r.code != 0 || !strings.HasSuffix(r.stdout, "openai API key saved.\n") {
		t.Fatalf("login: %+v", r)
	}
	if r := h.mustRun(t, 0, "whoami"); r.stdout != "Active provider: openai\nAuthorized: openai\n" {
		t.Fatalf("whoami: %q", r.stdout)
	}
}

func TestMCPAddListGetRemove(t *testing.T) {
	h := newPiHome(t)
	h.mustRun(t, 0, "mcp", "add", "--transport", "http", "docs", "https://example.com/mcp",
		"--header", "Authorization: Bearer secret-token")
	h.mustRun(t, 0, "mcp", "add", "fs", "--", "npx", "-y", "server-fs")
	if r := h.mustRun(t, 0, "mcp", "list"); r.stdout != "docs  —  http https://example.com/mcp (header auth)  [user]\nfs  —  stdio npx -y server-fs  [user]\n" {
		t.Fatalf("list: %q", r.stdout)
	}
	// A credential is never printed back.
	if r := h.mustRun(t, 0, "mcp", "get", "docs"); strings.Contains(r.stdout, "secret-token") || !strings.Contains(r.stdout, `"Authorization": "<redacted>"`) {
		t.Fatalf("get: %s", r.stdout)
	}
	h.mustRun(t, 0, "mcp", "disable", "fs")
	if r := h.mustRun(t, 0, "mcp", "list"); !strings.Contains(r.stdout, "[user]  (disabled)") {
		t.Fatalf("list after disable: %q", r.stdout)
	}
	if r := h.mustRun(t, 0, "mcp", "remove", "fs"); r.stdout != "Removed MCP server \"fs\" [scope: user]\n" {
		t.Fatalf("remove: %q", r.stdout)
	}
	if r := h.mustRun(t, 1, "mcp", "remove", "fs"); r.stderr != "error: no MCP server named \"fs\"\n" {
		t.Fatalf("second remove: %q", r.stderr)
	}
	if r := h.mustRun(t, 1, "mcp", "add", "x"); !strings.Contains(r.stderr, "a command is required for stdio transport") {
		t.Fatalf("add without command: %q", r.stderr)
	}
}

func TestGoalsAddListRemove(t *testing.T) {
	h := newPiHome(t)
	if r := h.mustRun(t, 0, "goals", "list"); r.stdout != "no goals — add one with: pi goals add \"<text>\"\n" {
		t.Fatalf("empty list: %q", r.stdout)
	}
	add := h.mustRun(t, 0, "goals", "add", "check the build", "--every", "30m")
	if !strings.HasSuffix(add.stdout, " (cron */30 * * * *)\n") {
		t.Fatalf("add: %q", add.stdout)
	}
	id := strings.Fields(add.stdout)[2]
	list := h.mustRun(t, 0, "goals", "list")
	if !strings.Contains(list.stdout, id+"  cron */30 * * * *  never ran") || !strings.Contains(list.stdout, "check the build  ["+h.cwd+"]") {
		t.Fatalf("list: %q", list.stdout)
	}
	if r := h.mustRun(t, 1, "goals", "add", "x", "--every", "90m"); !strings.Contains(r.stderr, "cannot express --every 90m") {
		t.Fatalf("90m: %q", r.stderr)
	}
	if r := h.mustRun(t, 0, "goals", "rm", id); r.stdout != "deleted: check the build\n" {
		t.Fatalf("rm: %q", r.stdout)
	}
	h.mustRun(t, 1, "goals", "rm", id)
}

func TestEverySugarMatchesLoop(t *testing.T) {
	cases := map[string]string{
		"30m": "*/30 * * * *", "45": "*/45 * * * *", "120m": "0 */2 * * *", "90m": "",
		"2h": "0 */2 * * *", "48h": "0 0 */2 * *", "1d": "0 0 */1 * *", "0m": "", "soon": "",
	}
	for in, want := range cases {
		if got := everyToCron(in); got != want {
			t.Errorf("everyToCron(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuiltinExtensionsToggleFromTheShell(t *testing.T) {
	h := newPiHome(t)
	if r := h.mustRun(t, 0, "enable", "caveman"); r.stdout != "Enabled caveman (built-in)\n" {
		t.Fatalf("enable: %q", r.stdout)
	}
	if r := h.mustRun(t, 0, "extensions"); !strings.Contains(r.stdout, "  [on ] caveman  — ") {
		t.Fatalf("extensions: %s", r.stdout)
	}
	if r := h.mustRun(t, 0, "disable", "nope"); r.stdout != "Not installed: nope\n" {
		t.Fatalf("disable unknown: %q", r.stdout)
	}
}

// runJSON parses a json-format run's single stdout line.
func runJSON(t *testing.T, r piResult) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || strings.Count(r.stdout, "\n") != 1 {
		t.Fatalf("stdout is not one JSON line: %q (%v)", r.stdout, err)
	}
	return out
}

func TestRunJSONReportsTheReplyAndSession(t *testing.T) {
	h := newPiHome(t)
	h.withGateway(t, fakeAnthropic(t, http.StatusOK, anthropicReply("done <ok>")))
	r := h.mustRun(t, 0, "run", "--model", "gw/claude-x", "--output-format", "json", "hi")
	// The bytes JSON.stringify writes: < and > are not escaped.
	if !strings.Contains(r.stdout, `"result":"done <ok>"`) {
		t.Fatalf("stdout: %s", r.stdout)
	}
	out := runJSON(t, r)
	if out["type"] != "result" || out["is_error"] != false || out["result"] != "done <ok>" || out["steps"] != float64(1) {
		t.Fatalf("result: %v", out)
	}
	id, _ := out["session_id"].(string)
	if id == "" || out["model"] != "custom:gw/claude-x" {
		t.Fatalf("session/model: %v", out)
	}
	// The id it reports is one a follow-up can resume.
	follow := runJSON(t, h.mustRun(t, 0, "run", "--session", id, "--output-format", "json", "again"))
	if follow["session_id"] != id {
		t.Fatalf("follow-up ran in %v, not %s", follow["session_id"], id)
	}
}

func TestRunStreamJSONIsOneEventPerLine(t *testing.T) {
	h := newPiHome(t)
	h.withGateway(t, fakeAnthropic(t, http.StatusOK, anthropicReply("streamed")))
	r := h.mustRun(t, 0, "run", "--model", "gw/claude-x", "--output-format", "stream-json", "hi")
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		types = append(types, ev["type"].(string))
	}
	if types[0] != "init" || types[len(types)-1] != "result" || !strings.Contains(strings.Join(types, ","), "text-delta") {
		t.Fatalf("event types: %v", types)
	}
}

func TestRunTextPutsTheReplyOnStdout(t *testing.T) {
	h := newPiHome(t)
	h.withGateway(t, fakeAnthropic(t, http.StatusOK, anthropicReply("plain reply")))
	r := h.mustRun(t, 0, "run", "--model", "gw/claude-x", "hi")
	if r.stdout != "plain reply\n" {
		t.Fatalf("stdout: %q", r.stdout)
	}
	if !strings.HasPrefix(r.stderr, "[session] ") || !strings.Contains(r.stderr, " · in:10 out:2 cache:0") {
		t.Fatalf("stderr: %q", r.stderr)
	}
}

// A turn the provider refuses exits nonzero, so CI notices.
func TestRunExitsNonzeroWhenTheTurnFails(t *testing.T) {
	h := newPiHome(t)
	h.withGateway(t, fakeAnthropic(t, http.StatusBadRequest, ""))
	out := runJSON(t, h.mustRun(t, 1, "run", "--model", "gw/claude-x", "--output-format", "json", "hi"))
	if out["is_error"] != true || out["errors"] == nil {
		t.Fatalf("result: %v", out)
	}
}

// A run that cannot start still answers in its format.
func TestRunWithNoModelFailsInItsOwnFormat(t *testing.T) {
	h := newPiHome(t)
	out := runJSON(t, h.mustRun(t, 1, "run", "--output-format", "json", "hi"))
	errs, _ := out["errors"].([]any)
	if out["is_error"] != true || len(errs) != 1 || !strings.HasPrefix(errs[0].(string), "No model selected.") {
		t.Fatalf("result: %v", out)
	}
	if r := h.mustRun(t, 1, "run", "--session", "NOPE", "hi"); r.stderr != "Session not found: NOPE (see `pi sessions`)\n" {
		t.Fatalf("stderr: %q", r.stderr)
	}
}

func TestRunReadsThePromptFromStdin(t *testing.T) {
	h := newPiHome(t)
	h.withGateway(t, fakeAnthropic(t, http.StatusOK, anthropicReply("from stdin")))
	r := h.run(t, "piped prompt\n", "run", "--model", "gw/claude-x", "--output-format", "json", "-")
	if r.code != 0 || runJSON(t, r)["result"] != "from stdin" {
		t.Fatalf("%+v", r)
	}
}

// A Go extension installs as a built binary, links as `go run` of its
// source, and removing it leaves neither the entry nor a binary behind.
func TestGoExtensionInstallLinkRemove(t *testing.T) {
	h := newPiHome(t)
	src := filepath.Join(h.home, "myext")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "go.mod"), []byte("module myext\ngo 1.22\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	bin := filepath.Join(h.home, ".loop", "extensions", "myext")

	if r := h.mustRun(t, 0, "install", src); r.stdout != "Installed myext\n" {
		t.Fatalf("install: %q", r.stdout)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("no binary: %v", err)
	}
	if r := h.mustRun(t, 0, "extensions"); !strings.Contains(r.stdout, "  [on ] myext  — "+bin+"\n") {
		t.Fatalf("extensions after install:\n%s", r.stdout)
	}
	if r := h.mustRun(t, 0, "link", src); r.stdout != "Linked myext (dev mode)\n" {
		t.Fatalf("link: %q", r.stdout)
	}
	if r := h.mustRun(t, 0, "extensions"); !strings.Contains(r.stdout, "  [on ] myext (linked)  — "+src+"\n") {
		t.Fatalf("extensions after link:\n%s", r.stdout)
	}
	h.mustRun(t, 0, "disable", "myext")
	if r := h.mustRun(t, 0, "extensions"); !strings.Contains(r.stdout, "  [off] myext (linked)") {
		t.Fatalf("extensions after disable:\n%s", r.stdout)
	}
	if r := h.mustRun(t, 0, "remove", "myext"); r.stdout != "Removed myext\n" {
		t.Fatalf("remove: %q", r.stdout)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatalf("binary left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "main.go")); err != nil {
		t.Fatalf("linked source was touched: %v", err)
	}
	if r := h.mustRun(t, 0, "remove", "myext"); r.stdout != "Not installed: myext\n" {
		t.Fatalf("second remove: %q", r.stdout)
	}
}
