package extension

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/skills"
)

// anatomy is Ryan's anatomy skill (github.com/wheresryan22/anatomy), loop's
// builtin of the same name: it explains an idea by building it as a real,
// working object — crafted, interactive isometric figures in SVG, WebGL and
// 3D.
//
// A COMMAND, never a prompt fragment: nothing reaches the system prompt, and
// the skill runs only when /anatomy is typed. That is also why it is the one
// builtin on by default — switched on, it costs nothing until it is used.
//
// The skill is a working kit (node scripts the model runs, references it
// reads, example figures it opens first), about 27 MB, so it is downloaded on
// the first /anatomy from a pinned commit into ~/.loop/extensions/anatomy —
// the directory loop uses, so the two share one copy.
type anatomy struct {
	// root holds the unpacked skill; ~/.loop/extensions/anatomy unless a
	// test points it elsewhere.
	root func() string
}

// anatomyCommit is the upstream commit both programs ship; bump it (and
// loop's ANATOMY_COMMIT) to take a newer skill.
const anatomyCommit = "1482e8469b0a9e0d8cc18ea2740a01423d29ae80"

// anatomyArchiveURL is a var so a test can serve the archive itself.
var anatomyArchiveURL = "https://codeload.github.com/wheresryan22/anatomy/tar.gz/" + anatomyCommit

const anatomyDownloadTimeout = 2 * time.Minute

func init() {
	Register(anatomy{root: func() string { return home.Path("extensions", "anatomy") }})
}

func (anatomy) Name() string { return "anatomy" }
func (anatomy) About() string {
	return "explain an idea as a crafted, interactive isometric figure — /anatomy <idea>"
}
func (anatomy) DefaultEnabled() bool { return true }

func (a anatomy) Commands() []Command {
	return []Command{{
		Name:  "anatomy",
		About: "Explain an idea as a crafted, interactive isometric figure — /anatomy <idea>",
		Run: func(ctx context.Context, _, rest string) (string, string, error) {
			fresh := !a.installed()
			dir, err := a.ensure(ctx)
			if err != nil {
				return "", "", fmt.Errorf("anatomy: could not download the skill: %w", err)
			}
			skill, ok := skills.LoadFile(filepath.Join(dir, skills.FileName))
			if !ok {
				return "", "", errors.New("anatomy: the downloaded skill has no readable SKILL.md")
			}
			note := ""
			if fresh {
				note = "anatomy: downloaded the skill (about 27 MB, once)"
			}
			return note, skill.Invocation(rest), nil
		},
	}}
}

// dir is the unpacked skill, named by commit so a new pin never mixes with an
// old copy.
func (a anatomy) dir() string { return filepath.Join(a.root(), anatomyCommit[:12]) }

func (a anatomy) installed() bool {
	_, err := os.Stat(filepath.Join(a.dir(), skills.FileName))
	return err == nil
}

// anatomyInstall serializes downloads, so two /anatomy at once fetch once.
var anatomyInstall sync.Mutex

func (a anatomy) ensure(ctx context.Context) (string, error) {
	anatomyInstall.Lock()
	defer anatomyInstall.Unlock()
	if a.installed() {
		return a.dir(), nil
	}
	root, dir := a.root(), a.dir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(root, ".partial-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if err := downloadAnatomy(ctx, staging); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(staging, skills.FileName)); err != nil {
		return "", errors.New("the download has no SKILL.md")
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.Rename(staging, dir); err != nil {
		return "", err
	}
	removeOtherVersions(root, dir)
	return dir, nil
}

// downloadAnatomy unpacks the pinned archive into dir, dropping its one
// top-level <repo>-<commit> folder so SKILL.md lands at the top.
func downloadAnatomy(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, anatomyDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anatomyArchiveURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gz.Close()
	return untarStripped(tar.NewReader(gz), dir)
}

func untarStripped(r *tar.Reader, dir string) error {
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		_, rel, nested := strings.Cut(h.Name, "/")
		if !nested || rel == "" {
			continue // the top-level folder itself, or a pax header
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		// An entry may not climb out of the directory it is unpacked into.
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(filepath.Separator)) {
			return fmt.Errorf("archive entry %q escapes the skill directory", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeEntry(r, target, os.FileMode(h.Mode).Perm()); err != nil {
				return err
			}
		}
	}
}

func writeEntry(r io.Reader, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// removeOtherVersions drops copies left by an older pin; 27 MB each is worth
// reclaiming.
func removeOtherVersions(root, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if path := filepath.Join(root, e.Name()); path != keep {
			os.RemoveAll(path)
		}
	}
}
