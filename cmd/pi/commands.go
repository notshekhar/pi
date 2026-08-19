package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/modules/core/agent"
	"github.com/notshekhar/pi/internal/modules/core/catalog"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/hooks"
	"github.com/notshekhar/pi/internal/modules/core/session"
	"github.com/notshekhar/pi/internal/modules/core/tools"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// The slash-command surface beyond model/provider selection.

// clear wipes the visible transcript. The session is untouched: this is a
// screen command, and /new is the one that makes the model forget.
// clear wipes the screen AND starts a new session.
//
// Both, not just the screen. `/clear` is the gesture for "I am done with
// that, start again", and clearing only the display left the agent still
// carrying — and still paying for — a conversation the user could no longer
// see.
//
// What separates it from `/new` is the SCROLLBACK. Both start a fresh session
// and redraw the header; `/clear` also erases what the terminal has kept,
// so the record goes with the session.
func (t *repl) clear() {
	if t.busy() {
		return
	}
	t.app.Do(t.app.Wipe)
	t.newSession()
}

// clearScreen is ctrl+l: wipe the display and redraw the header, KEEPING the
// session.
//
// The session is deliberately untouched: ctrl+l is a reflex people use to get
// a clean screen mid-task, and losing the conversation to it would be a very
// expensive surprise.
func (t *repl) clearScreen() {
	t.app.Do(func() {
		t.app.Wipe()
		t.redrawHeader()
	})
}

// showCostAll totals every stored session.
func (t *repl) showCostAll() {
	usage, err := session.TotalUsage()
	if err != nil {
		t.fail("cost: %s", err)
		return
	}
	t.app.Do(func() {
		th := t.app.Theme()
		t.app.Print(
			th.Fg(tui.SlotMuted, "sessions ")+th.Fg(tui.SlotText, fmt.Sprint(usage.Sessions)),
			th.Fg(tui.SlotMuted, "input    ")+th.Fg(tui.SlotText, commas(int(usage.InputTokens))),
			th.Fg(tui.SlotMuted, "output   ")+th.Fg(tui.SlotText, commas(int(usage.OutputTokens))),
			th.Fg(tui.SlotMuted, "cost     ")+th.Fg(tui.SlotSuccess, fmt.Sprintf("$%.4f", usage.CostUSD)))
	})
}

// showCost is `/cost`: the usage wall, then what it cost.
//
// Buckets rather than one number, because "what am I spending" is really
// several questions — this session, this project, this month — and a single
// lifetime total answers none of them. The per-provider split is at the
// bottom because it is the one that explains a surprising month.
func (t *repl) showCost() {
	buckets, bucketErr := session.SpendBuckets(t.cfg.CWD)
	home, _ := os.UserHomeDir()

	t.app.Do(func() {
		th := t.app.Theme()
		lines := t.steakLines("")
		lines = append(lines, "", th.Fg(tui.SlotAccent, th.Bold("cost")))

		row := func(label string, usd float64, extra string) string {
			out := "  " + th.Fg(tui.SlotDim, padRight(label, 14)) +
				th.Fg(tui.SlotAccent, padLeft(fmt.Sprintf("$%.4f", usd), 10))
			if extra != "" {
				out += "   " + th.Fg(tui.SlotDim, extra)
			}
			return out
		}

		in, out, _, cost := t.app.Stats()
		lines = append(lines, row("session", cost,
			fmt.Sprintf("in:%s out:%s", formatTokens(int(in)), formatTokens(int(out)))))

		if bucketErr != nil {
			lines = append(lines, th.Fg(tui.SlotError, "  totals unavailable: "+bucketErr.Error()))
			t.app.Print(lines...)
			return
		}

		shown := t.cfg.CWD
		if home != "" && strings.HasPrefix(shown, home) {
			shown = "~" + shown[len(home):]
		}
		lines = append(lines,
			row("directory", buckets.Directory, shown),
			row("today", buckets.Today, ""),
			row("last 7 days", buckets.Week, ""),
			row("this month", buckets.Month, ""),
			row("lifetime", buckets.Lifetime, ""))

		byProvider := buckets.ByProvider
		providers := make([]string, 0, len(byProvider))
		for p, v := range byProvider {
			if v > 0 {
				providers = append(providers, p)
			}
		}
		sort.Slice(providers, func(i, j int) bool {
			return byProvider[providers[i]] > byProvider[providers[j]]
		})
		for _, p := range providers {
			lines = append(lines, row("  "+p, byProvider[p], ""))
		}
		t.app.Print(lines...)
	})
}

// padLeft right-aligns a value in a column, so a column of money lines up on
// the decimal point rather than on the dollar sign.
func padLeft(s string, n int) string {
	if pad := n - tui.VisibleWidth(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}

// showContext reports how much of the window the conversation is using.
// showContext is `/context`: where the window is actually going.
//
// A grid, not a percentage. Each cell is half a percent of the window, and
// the colours are the categories — so "the transcript is the problem" and
// "the tool definitions are the problem" look different at a glance, which a
// single bar cannot show. The layout is the reference's: the wall on the
// left, the legend pinned beside it.
func (t *repl) showContext() {
	if t.busy() {
		return
	}
	window := t.contextWindow()
	report := agent.BuildContextReport(t.run, window)
	// The headline prefers the provider's own count from the last turn: the
	// categories are chars/4 estimates, and quoting an estimate as though it
	// were measured is how people end up surprised by a compaction.
	headline := t.lastContextTokens
	if headline <= 0 {
		headline = report.TotalTokens
	}

	t.app.Do(func() {
		th := t.app.Theme()

		if window <= 0 {
			lines := []string{th.Fg(tui.SlotAccent, th.Bold("context"))}
			for _, c := range report.Categories {
				lines = append(lines, "  "+th.Fg(tui.SlotText, padRight(c.Label, 18))+
					th.Fg(tui.SlotMuted, commas(c.Tokens)+" tokens"))
			}
			lines = append(lines, th.Fg(tui.SlotDim, "  (unknown context window for this model — no percentages)"))
			t.app.Print(lines...)
			return
		}

		const rows, cols = 10, 20
		const cells = rows * cols

		// Floor at one cell for any non-empty category: a category that costs
		// real tokens but rounds to nothing would be invisible in the grid
		// while still appearing in the legend, which reads as a bug.
		painted := make([]string, 0, cells)
		for i, c := range report.Categories {
			if c.Tokens <= 0 {
				continue
			}
			n := max(1, int(float64(c.Tokens)/float64(window)*cells+0.5))
			for k := 0; k < n && len(painted) < cells; k++ {
				painted = append(painted, th.Series(i, "■"))
			}
		}
		for len(painted) < cells {
			painted = append(painted, th.Fg(tui.SlotDim, "·"))
		}

		pct := func(n int) string {
			p := float64(n) / float64(window) * 100
			if n > 0 && p < 0.1 {
				return fmt.Sprintf("%.2f%%", p)
			}
			return fmt.Sprintf("%.1f%%", p)
		}

		// The column beside the grid: what model, how full, then the legend.
		side := make([]string, rows)
		side[0] = th.Fg(tui.SlotText, th.Bold(t.modelName()))
		side[1] = th.Fg(tui.SlotDim, t.cfg.FullID())
		side[2] = th.Fg(tui.SlotText, fmt.Sprintf("%s/%s tokens (%d%%)",
			formatTokens(headline), formatTokens(window), int(float64(headline)/float64(window)*100+0.5)))
		side[4] = th.Fg(tui.SlotAccent, th.Bold("Estimated usage by category"))

		legend := make([]string, 0, len(report.Categories)+2)
		for i, c := range report.Categories {
			legend = append(legend, th.Series(i, "■")+th.Fg(tui.SlotText,
				fmt.Sprintf(" %s: %s tokens (%s)", c.Label, formatTokens(c.Tokens), pct(c.Tokens))))
		}
		legend = append(legend, th.Fg(tui.SlotDim, "·")+th.Fg(tui.SlotText,
			fmt.Sprintf(" Free space: %s (%s)", formatTokens(report.FreeTokens), pct(report.FreeTokens))))
		if report.AutoCompactThreshold > 0 {
			legend = append(legend, th.Fg(tui.SlotDim, fmt.Sprintf("auto-compact at %d%% (%s)",
				int(report.AutoCompactThreshold*100+0.5),
				formatTokens(int(float64(window)*report.AutoCompactThreshold)))))
		}

		// Legend lines that do not fit beside the grid fall below it rather
		// than being dropped — the ones that overflow are the last
		// categories, which on a long session are the biggest.
		var overflow []string
		for i, line := range legend {
			if 5+i < rows {
				side[5+i] = line
				continue
			}
			overflow = append(overflow, line)
		}

		lines := []string{th.Fg(tui.SlotAccent, th.Bold("Context usage"))}
		for r := 0; r < rows; r++ {
			row := strings.Join(painted[r*cols:(r+1)*cols], " ")
			if side[r] != "" {
				row += "   " + side[r]
			}
			lines = append(lines, row)
		}
		lines = append(lines, overflow...)

		if len(report.Skills) > 0 {
			lines = append(lines, "", th.Fg(tui.SlotAccent, th.Bold("Skills")))
			for i, s := range report.Skills {
				branch := "├"
				if i == len(report.Skills)-1 {
					branch = "└"
				}
				lines = append(lines, th.Fg(tui.SlotDim, branch)+" "+th.Fg(tui.SlotText, s.Name)+": "+
					th.Fg(tui.SlotDim, fmt.Sprintf("~%d tokens", s.Tokens)))
			}
		}
		if t.lastContextTokens > 0 {
			lines = append(lines, "", th.Fg(tui.SlotDim, fmt.Sprintf(
				"categories are chars/4 estimates (%s total); headline uses the last provider-reported context size",
				formatTokens(report.TotalTokens))))
		}
		t.app.Print(lines...)
	})
}

// modelName is the catalog's display name for the active model, falling back
// to the id when the catalog does not carry it.
func (t *repl) modelName() string {
	if m, ok := catalog.Lookup(t.cfg.Provider, t.cfg.ModelID, config.APIKey(t.cfg.Provider)); ok && m.Name != "" {
		return m.Name
	}
	return t.cfg.FullID()
}

// formatTokens abbreviates a token count the way the status line does, so the
// same number reads the same in both places.
func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprint(n)
}

// meter draws a proportion as a bar of block characters.
func meter(pct, width int) string {
	filled := pct * width / 100
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// compact replaces the history with a model-written summary of it.
//
// It claims the turn lock for its whole run: compaction is a model call that
// rewrites the session, so letting a prompt start alongside it would have two
// writers on the same history.
func (t *repl) compact(parent context.Context) {
	t.mu.Lock()
	if t.turning {
		t.mu.Unlock()
		t.dim("(turn in progress — Esc to interrupt)")
		return
	}
	t.turning = true
	ctx, cancel := context.WithCancel(parent)
	t.cancel = cancel
	t.mu.Unlock()

	t.dim("compacting…")
	t.app.Do(func() { t.app.LoaderStart() })
	go func() {
		defer func() {
			t.mu.Lock()
			t.turning = false
			t.cancel = nil
			t.mu.Unlock()
			t.app.Do(t.app.LoaderStop)
		}()

		t.fireHook(ctx, hooks.Context{Event: hooks.PreCompact})
		before, after, err := agent.Compact(ctx, t.run)
		if err != nil {
			t.fail("compact: %s", err)
			return
		}
		saved := 0
		if before > 0 {
			saved = 100 * (before - after) / before
		}
		t.app.Do(func() {
			th := t.app.Theme()
			t.app.Print(th.Fg(tui.SlotSuccess, fmt.Sprintf(
				"compacted ~%s → ~%s tokens (%d%% smaller)",
				commas(agent.EstimateTokens(before)), commas(agent.EstimateTokens(after)), saved)))
		})
	}()
}

// busy reports whether a turn owns the session right now. Commands that read
// or write the history check it first: mid-turn the answer would be a torn
// read of a conversation the turn goroutine is still appending to.
func (t *repl) busy() bool {
	t.mu.Lock()
	turning := t.turning
	t.mu.Unlock()
	// Report outside the lock: t.dim sends on the render channel, and holding
	// a mutex across a blocking send is how a deadlock starts.
	if turning {
		t.dim("(turn in progress — Esc to interrupt)")
	}
	return turning
}

// copyLast puts the most recent assistant reply on the system clipboard.
func (t *repl) copyLast() {
	if t.busy() {
		return
	}
	text := t.run.Session.LastAssistant()
	if text == "" {
		t.dim("nothing to copy")
		return
	}
	if err := clipboardWrite(text); err != nil {
		t.fail("copy: %s", err)
		return
	}
	t.dim("copied %d chars", len(text))
}

// clipboardWrite pipes text to the platform's clipboard tool.
func clipboardWrite(text string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "pbcopy"
	case "windows":
		name, args = "clip", nil
	default:
		// Wayland first: on a Wayland session xclip may exist but write to an
		// X server nothing is reading.
		if _, err := exec.LookPath("wl-copy"); err == nil {
			name = "wl-copy"
		} else {
			name, args = "xclip", []string{"-selection", "clipboard"}
		}
	}
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s not found", name)
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// export writes the conversation out. The EXTENSION picks the format:
// .md (default), .jsonl, or .html.
//
// .jsonl is the session's own on-disk form, which is what makes /export and
// /import a round trip rather than two unrelated features.
func (t *repl) export(rest string) {
	if t.busy() {
		return
	}
	if t.run.Session.Text() == "" {
		t.dim("nothing to export")
		return
	}
	path, err := t.exportTo(rest)
	if err != nil {
		t.fail("export: %s", err)
		return
	}
	t.dim("exported to %s", path)
}

// exportTo writes the session and returns the path, so /share can reuse it.
func (t *repl) exportTo(rest string) (string, error) {
	path := strings.TrimSpace(rest)
	if path == "" {
		path = filepath.Join(t.cfg.CWD, fmt.Sprintf("pi-agent-%s.md", time.Now().Format("20060102-150405")))
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(t.cfg.CWD, path)
	}

	var data []byte
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl":
		// Through the codec, which is where this always belonged: the export
		// format is a property of the wire encoding, not of how the messages
		// happen to be stored. It used to copy the session's body file, and
		// that is exactly the coupling that had to be unpicked when the
		// storage moved into the database.
		raw, err := session.JSONL(t.run.Session.Messages)
		if err != nil {
			return "", err
		}
		data = raw
	case ".html":
		data = []byte(exportHTML(t.cfg.FullID(), t.run.Session.Text()))
	default:
		header := fmt.Sprintf("# pi-agent session\n\n%s · %s\n\n", t.cfg.FullID(), time.Now().Format(time.RFC3339))
		data = []byte(header + t.run.Session.Text())
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// exportHTML wraps the transcript in a self-contained page.
//
// The text goes in a <pre> and is ESCAPED: a transcript is full of angle
// brackets and ampersands from code, and pasting it raw into HTML would both
// break the page and let a session's contents inject markup into it.
func exportHTML(model, text string) string {
	return `<!doctype html>
<meta charset="utf-8">
<title>pi-agent session</title>
<style>
  body { background:#161618; color:#e4e4e7; font:14px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace; margin:0; padding:2rem; }
  h1 { font-size:1rem; font-weight:600; color:#a0a0a8; margin:0 0 .25rem; }
  .meta { color:#6b6b73; font-size:.8rem; margin-bottom:1.5rem; }
  pre { white-space:pre-wrap; word-wrap:break-word; margin:0; }
</style>
<h1>pi-agent session</h1>
<div class="meta">` + htmlEscape(model) + ` · ` + time.Now().Format(time.RFC3339) + `</div>
<pre>` + htmlEscape(text) + `</pre>
`
}

func htmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	).Replace(s)
}

// changeDir moves the working directory the tools operate in.
func (t *repl) changeDir(rest string) {
	if rest != "" && t.busy() {
		return
	}
	if rest == "" {
		t.dim("%s", t.cfg.CWD)
		return
	}
	path := rest
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.cfg.CWD, path)
	}
	// Resolve symlinks so the path the tools see matches the one shown, and
	// so a summary's cwd-prefix trimming actually matches.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.fail("cd: %s", err)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		t.fail("cd: %s is not a directory", resolved)
		return
	}
	t.cfg.CWD = resolved
	t.run.Config.CWD = resolved
	t.run.Tools.CWD = resolved
	t.app.Do(func() { t.app.SetCWD(resolved) })
	t.dim("%s", resolved)
}

// initProject asks the model to write an AGENTS.md for the repository.
func (t *repl) initProject(parent context.Context) {
	const prompt = `Look around this repository and write an AGENTS.md at its root.

Keep it short and specific to THIS codebase — the things a new contributor
would get wrong. Cover how to build, test and run it, the layout, and any
conventions or traps that are not obvious from reading the code. Do not
restate what the code already says.

If an AGENTS.md already exists, read it first and improve it in place.`
	t.startTurn(parent, prompt)
}

// commas groups a number for reading.
func commas(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

// todoItems converts the agent's plan into the panel's shape. The two enums
// are separate so `tui` need not import `core`.
func todoItems(items []tools.Todo) []tui.TodoItem {
	out := make([]tui.TodoItem, 0, len(items))
	for _, item := range items {
		out = append(out, tui.TodoItem{
			Content:    item.Content,
			ActiveForm: item.ActiveForm,
			Status:     tui.TodoStatus(item.Status),
		})
	}
	return out
}

// retireTodos takes the pinned plan down at the end of the turn that owned
// it — finished, interrupted or abandoned — leaving one line in the
// transcript saying how it ended.
//
// loop's rule, and the reason is that the panel is CHROME: it is pinned above
// the composer precisely because it belongs to the work in flight, and a plan
// that outlives its turn sits there describing a job nobody is doing while
// the next question is being typed underneath it. The line it leaves behind
// is what keeps the record — "todos: 3 of 7 open" is exactly the thing to
// know about a turn that stopped partway.
//
// The agent's own list is NOT cleared: it stays in the tool's state, so a
// follow-up turn can resume the same plan and the panel comes back the moment
// the agent touches it.
func (t *repl) retireTodos() {
	items := t.app.Todos()
	if len(items) == 0 {
		return
	}
	t.app.Print(t.app.Theme().Fg(tui.SlotDim, tui.TodoRetireLine(items)))
	t.app.SetTodos(nil)
	// A plan with nothing left on it is finished with for good; one with work
	// left stays in the tool so the next turn resumes it rather than replans.
	if t.run.Tools.Todos != nil && t.run.Tools.Todos.Done() {
		t.run.Tools.Todos.Clear()
	}
}
