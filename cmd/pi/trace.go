package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/trace"
)

func init() {
	trace.Pricer = func(model string, u session.Usage) (float64, bool) {
		usd, _, ok := agent.PriceUsage(model, u)
		return usd, ok
	}
}

// /trace — loop's trace-handlers.ts: the session's timing-and-cost trace as
// one HTML file. An explicit path is an export (written, not opened);
// otherwise a fresh file under the temp dir, opened in the browser. The path
// is printed either way — over SSH nothing opens.
func (t *repl) traceCmd(target string) {
	target = strings.TrimSpace(target)
	sess := t.run.Session
	if !sess.Saved() {
		t.dim("nothing to trace yet — send a message first")
		return
	}
	m := trace.FromSession(sess)
	html := trace.HTML(m)
	path := ""
	if target != "" {
		path = target
		if strings.HasPrefix(path, "~/") {
			h, _ := os.UserHomeDir()
			path = filepath.Join(h, path[2:])
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(t.cfg.CWD, path)
		}
	} else {
		stamp := strings.ReplaceAll(time.Now().UTC().Format("2006-01-02T15:04:05"), ":", "-")
		path = filepath.Join(os.TempDir(), "loop-trace", sess.ID+"-"+stamp+".html")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		err = os.WriteFile(path, []byte(html), 0o644)
		if err != nil {
			t.fail("could not write the trace: %s", err)
			return
		}
	} else {
		t.fail("could not write the trace: %s", err)
		return
	}
	lines := []string{trace.Headline(m)}
	if target != "" {
		lines = append(lines, "trace written to "+path)
	} else {
		u := (&url.URL{Scheme: "file", Path: path}).String()
		if openBrowser(u) {
			lines = append(lines, "opening the trace — "+u)
		} else {
			lines = append(lines, "open it yourself: "+u)
		}
	}
	t.do(func() { t.app.Print(lines...) })
	if c := m.Coverage; c.Derived+c.None > 0 && c.Recorded == 0 {
		t.dim("steps recorded before this version have wall time only; new turns get model/tool bars")
	}
}
