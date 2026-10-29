package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// /update and `pi upgrade` — loop's updateApp/runUpgrade, pointed at pi's own
// releases and installer.

const repoSlug = "notshekhar/pi"

var (
	upgradeURL    = "https://raw.githubusercontent.com/" + repoSlug + "/main/install.sh"
	upgradeURLPS1 = "https://raw.githubusercontent.com/" + repoSlug + "/main/install.ps1"
	tagRE         = regexp.MustCompile(`^v\d`)
)

// fetchLatestTag follows the releases/latest redirect (not rate limited the
// way the anonymous API is), then falls back to the API.
func fetchLatestTag(ctx context.Context) string {
	client := &http.Client{Timeout: 10 * time.Second}
	if req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://github.com/"+repoSlug+"/releases/latest", nil); err == nil {
		if r, err := client.Do(req); err == nil {
			r.Body.Close()
			if tag := path.Base(r.Request.URL.Path); tagRE.MatchString(tag) {
				return tag
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repoSlug+"/releases/latest", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	r, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return ""
	}
	var j struct {
		TagName string `json:"tag_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&j)
	return j.TagName
}

// semverGt reports a > b for vX.Y.Z[-pre] tags; a release outranks its own
// prerelease. Unparseable input is never newer.
func semverGt(a, b string) bool {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			return pa.n[i] > pb.n[i]
		}
	}
	switch {
	case pa.pre == pb.pre:
		return false
	case pa.pre == "":
		return true
	case pb.pre == "":
		return false
	}
	return comparePre(pa.pre, pb.pre) > 0
}

type semver struct {
	n   [3]int
	pre string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var v semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		v.n[i] = n
	}
	v.pre = pre
	return v, true
}

func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, ex := strconv.Atoi(as[i])
		y, ey := strconv.Atoi(bs[i])
		switch {
		case ex == nil && ey == nil:
			if x != y {
				return x - y
			}
		case ex == nil:
			return -1
		case ey == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return len(as) - len(bs)
}

// resolveAvailableUpdate is the newer tag, or "".
func resolveAvailableUpdate(ctx context.Context, current string) string {
	if latest := fetchLatestTag(ctx); latest != "" && semverGt(latest, "v"+current) {
		return latest
	}
	return ""
}

// installCommand runs the platform installer.
func installCommand(force bool) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "irm "+upgradeURLPS1+" | iex")
	} else {
		cmd = exec.Command("bash", "-c", "curl -fsSL "+upgradeURL+" | bash")
	}
	cmd.Env = os.Environ()
	if force {
		cmd.Env = append(cmd.Env, "PI_FORCE=1")
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

// upgradeCommand is `pi upgrade [--force]`.
func upgradeCommand(args []string) {
	force := len(args) > 0 && (args[0] == "--force" || args[0] == "-f")
	fmt.Printf("▶ Checking for updates (current v%s)…\n", version)
	latest := fetchLatestTag(context.Background())
	switch {
	case !force && latest != "":
		if !semverGt(latest, "v"+version) {
			fmt.Printf("Up to date (latest %s)\n", latest)
			return
		}
		fmt.Printf("▶ Upgrading %s → %s\n", version, latest)
	case latest == "":
		fmt.Println("▶ Could not query latest release; running installer anyway.")
	}
	cmd := installCommand(force)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		os.Exit(1)
	}
}

// updateApp is /update: check, then hand the terminal to the installer.
func (t *repl) updateApp(ctx context.Context) {
	if t.busy() {
		t.dim("busy; finish or abort current turn first")
		return
	}
	go func() {
		t.do(func() { t.app.LoaderStart("Checking for updates") })
		latest := resolveAvailableUpdate(ctx, version)
		t.do(t.app.LoaderStop)
		if latest == "" {
			t.dim("already up to date (v%s)", version)
			return
		}
		t.dim("updating v%s → %s…", version, latest)
		var runErr error
		t.do(func() {
			t.app.Suspend(func() { runErr = installCommand(false).Run() })
		})
		if runErr != nil {
			t.fail("update failed: %s", runErr)
			return
		}
		t.dim("updated to %s — restart pi to use it", latest)
	}()
}
