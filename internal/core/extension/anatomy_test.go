package extension

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const anatomySkillMD = "---\nname: anatomy\ndescription: test\n---\n\n# Anatomy\n\nThe object is the explanation.\n"

// anatomyArchive is GitHub-shaped: one top-level <repo>-<commit> folder.
func anatomyArchive(t *testing.T, extra map[string]string) []byte {
	t.Helper()
	files := map[string]string{"SKILL.md": anatomySkillMD, "kit/audit.mjs": "export {};\n"}
	for k, v := range extra {
		files[k] = v
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	top := "anatomy-" + anatomyCommit + "/"
	tw.WriteHeader(&tar.Header{Name: top, Typeflag: tar.TypeDir, Mode: 0o755})
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: top + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// serveAnatomy points the download at a local server and the skill at a temp
// root, so nothing touches the network or the real ~/.loop.
func serveAnatomy(t *testing.T, status int, body []byte) (anatomy, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	prev := anatomyArchiveURL
	anatomyArchiveURL = srv.URL + "/" + anatomyCommit
	t.Cleanup(func() { anatomyArchiveURL = prev })
	root := t.TempDir()
	return anatomy{root: func() string { return root }}, &hits
}

func TestAnatomyDownloadsOnceAndSubmitsTheSkill(t *testing.T) {
	a, hits := serveAnatomy(t, http.StatusOK, anatomyArchive(t, nil))
	run := a.Commands()[0].Run

	out, prompt, err := run(context.Background(), "", "how a hash map resolves collisions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "downloaded") {
		t.Errorf("first use did not say it downloaded: %q", out)
	}
	wantHead := `<skill name="anatomy" location="` + filepath.Join(a.dir(), "SKILL.md") + `">` + "\nReferences are relative to " + a.dir() + "."
	if !strings.HasPrefix(prompt, wantHead) {
		t.Errorf("prompt head = %.200q", prompt)
	}
	if !strings.Contains(prompt, "The object is the explanation.") || strings.Contains(prompt, "description: test") {
		t.Errorf("body wrong or frontmatter kept: %q", prompt)
	}
	if !strings.HasSuffix(prompt, "</skill>\n\nhow a hash map resolves collisions") {
		t.Errorf("idea not appended: %.80q", prompt[len(prompt)-80:])
	}
	if _, err := os.Stat(filepath.Join(a.dir(), "kit", "audit.mjs")); err != nil {
		t.Errorf("kit not unpacked: %v", err)
	}

	out, _, err = run(context.Background(), "", "")
	if err != nil || out != "" {
		t.Errorf("second use: out=%q err=%v", out, err)
	}
	if hits.Load() != 1 {
		t.Errorf("downloads = %d, want 1", hits.Load())
	}
}

func TestAnatomyConcurrentFirstUseDownloadsOnceAndDropsOldPins(t *testing.T) {
	a, hits := serveAnatomy(t, http.StatusOK, anatomyArchive(t, nil))
	stale := filepath.Join(a.root(), "0000000000ab")
	os.MkdirAll(stale, 0o755)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.ensure(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Errorf("downloads = %d, want 1", hits.Load())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("an older pin was left behind")
	}
}

func TestAnatomyFailedDownloadLeavesNothing(t *testing.T) {
	a, _ := serveAnatomy(t, http.StatusNotFound, []byte("nope"))
	_, prompt, err := a.Commands()[0].Run(context.Background(), "", "a lock")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") || prompt != "" {
		t.Fatalf("err=%v prompt=%q", err, prompt)
	}
	if entries, _ := os.ReadDir(a.root()); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}

func TestAnatomyRefusesAnEntryThatEscapes(t *testing.T) {
	a, _ := serveAnatomy(t, http.StatusOK, anatomyArchive(t, map[string]string{"../../evil.txt": "x"}))
	if _, err := a.ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(a.root()), "evil.txt")); !os.IsNotExist(err) {
		t.Error("an escaping entry was written")
	}
}

// Command-only: it is on by default precisely because it adds no prompt
// fragment and no tools.
func TestAnatomyIsOnByDefaultAndOnlyACommand(t *testing.T) {
	e, ok := Find("anatomy")
	if !ok || !IsEnabled(e, nil) {
		t.Fatal("anatomy is not a default-on builtin")
	}
	if _, adds := e.(PromptProvider); adds {
		t.Error("anatomy contributes to the system prompt")
	}
	if _, adds := e.(ToolProvider); adds {
		t.Error("anatomy contributes tools")
	}
}
