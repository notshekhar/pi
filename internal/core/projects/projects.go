// Package projects is loop's `projects` table (sessions/projects.ts) and the
// trust decisions kept in it (agent/trust.ts): per-directory trust, the last
// model picked in a folder, the per-provider model map, and bash
// "always allow" grants. Shared with loop through agent.db, so a folder
// trusted in one program is trusted in the other.
package projects

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/home"
)

func upsert(dir string, trust *bool, model string) error {
	h, err := db.Open()
	if err != nil {
		return err
	}
	var t, m any
	if trust != nil {
		if *trust {
			t = 1
		} else {
			t = 0
		}
	}
	if model != "" {
		m = model
	}
	_, err = h.Exec(`INSERT INTO projects (dir, trust, model, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(dir) DO UPDATE SET
			trust = COALESCE(excluded.trust, projects.trust),
			model = COALESCE(excluded.model, projects.model),
			updated_at = excluded.updated_at`, dir, t, m, time.Now().UnixMilli())
	return err
}

// Canonical is the form rows are keyed by: absolute, symlinks resolved.
func Canonical(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// ─── trust ───────────────────────────────────────────────────────────────

var (
	sessionMu    sync.Mutex
	sessionTrust = map[string]bool{}
)

func trustRows() map[string]bool {
	out := map[string]bool{}
	h, err := db.Open()
	if err != nil {
		return out
	}
	rows, err := h.Query("SELECT dir, trust FROM projects WHERE trust IS NOT NULL")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var dir string
		var t int
		if rows.Scan(&dir, &t) == nil {
			out[dir] = t == 1
		}
	}
	return out
}

// Decision is the nearest ancestor's trust decision; ok=false when none.
func Decision(cwd string) (trusted, ok bool) {
	canon := Canonical(cwd)
	sessionMu.Lock()
	if sessionTrust[canon] {
		sessionMu.Unlock()
		return true, true
	}
	sessionMu.Unlock()
	data := trustRows()
	for dir := canon; ; {
		if v, found := data[dir]; found {
			return v, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, false
		}
		dir = parent
	}
}

// IsTrusted: project resources load only on an explicit yes.
func IsTrusted(cwd string) bool {
	t, ok := Decision(cwd)
	return ok && t
}

// SetTrust persists a decision.
func SetTrust(cwd string, decision bool) error { return upsert(Canonical(cwd), &decision, "") }

// TrustForSession trusts a folder for this process only.
func TrustForSession(cwd string) {
	sessionMu.Lock()
	sessionTrust[Canonical(cwd)] = true
	sessionMu.Unlock()
}

// HasTrustInputs reports a folder (or ancestor) shipping .loop or .claude.
func HasTrustInputs(cwd string) bool {
	for dir := Canonical(cwd); ; {
		for _, name := range []string{home.DirName, ".claude"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// TrustOption is one choice the trust prompt offers.
type TrustOption struct {
	Label    string
	Trusted  bool
	Remember bool
	SavePath string
}

// Options are loop's getTrustOptions.
func Options(cwd string) []TrustOption {
	path := Canonical(cwd)
	parent := filepath.Dir(path)
	opts := []TrustOption{{Label: "Trust this folder", Trusted: true, Remember: true, SavePath: path}}
	if parent != path {
		opts = append(opts, TrustOption{Label: "Trust parent folder (" + parent + ")", Trusted: true, Remember: true, SavePath: parent})
	}
	return append(opts,
		TrustOption{Label: "Trust for this session only", Trusted: true, SavePath: path},
		TrustOption{Label: "Don't trust", Remember: true, SavePath: path})
}

// ─── per-project model memory ────────────────────────────────────────────

// Model is the last model picked in a folder.
func Model(cwd string) string {
	h, err := db.Open()
	if err != nil {
		return ""
	}
	var m sql.NullString
	_ = h.QueryRow("SELECT model FROM projects WHERE dir = ?", cwd).Scan(&m)
	return m.String
}

func providerOf(modelID string) string {
	start := 0
	if strings.HasPrefix(modelID, "custom:") {
		start = len("custom:")
	}
	if i := strings.IndexByte(modelID[start:], '/'); i > 0 {
		return modelID[:start+i]
	}
	return ""
}

func providerModels(cwd string) map[string]string {
	out := map[string]string{}
	h, err := db.Open()
	if err != nil {
		return out
	}
	var raw sql.NullString
	_ = h.QueryRow("SELECT provider_models FROM projects WHERE dir = ?", cwd).Scan(&raw)
	_ = json.Unmarshal([]byte(raw.String), &out)
	return out
}

// SetModel remembers a pick for the folder and for its provider there.
func SetModel(cwd, modelID string) error {
	if err := upsert(cwd, nil, modelID); err != nil {
		return err
	}
	prov := providerOf(modelID)
	if prov == "" {
		return nil
	}
	pm := providerModels(cwd)
	pm[prov] = modelID
	raw, _ := json.Marshal(pm)
	h, err := db.Open()
	if err != nil {
		return err
	}
	_, err = h.Exec("UPDATE projects SET provider_models = ? WHERE dir = ?", string(raw), cwd)
	return err
}

// ProviderModel is the model last used with a provider in this folder.
func ProviderModel(cwd, provider string) string { return providerModels(cwd)[provider] }

// ─── bash grants ─────────────────────────────────────────────────────────

// BashAllow is the folder's always-allow grants.
func BashAllow(cwd string) []string {
	h, err := db.Open()
	if err != nil {
		return nil
	}
	var raw sql.NullString
	_ = h.QueryRow("SELECT bash_allow FROM projects WHERE dir = ?", cwd).Scan(&raw)
	var list []string
	_ = json.Unmarshal([]byte(raw.String), &list)
	out := list[:0]
	for _, p := range list {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SetBashAllow replaces the folder's grants.
func SetBashAllow(cwd string, patterns []string) error {
	if err := upsert(cwd, nil, ""); err != nil {
		return err
	}
	raw, _ := json.Marshal(patterns)
	h, err := db.Open()
	if err != nil {
		return err
	}
	_, err = h.Exec("UPDATE projects SET bash_allow = ?, updated_at = ? WHERE dir = ?", string(raw), time.Now().UnixMilli(), cwd)
	return err
}

// AddBashAllow appends grants, deduped.
func AddBashAllow(cwd string, patterns []string) error {
	cur := BashAllow(cwd)
	seen := map[string]bool{}
	for _, p := range cur {
		seen[p] = true
	}
	next := append([]string{}, cur...)
	for _, p := range patterns {
		if !seen[p] {
			next = append(next, p)
			seen[p] = true
		}
	}
	if len(next) == len(cur) {
		return nil
	}
	return SetBashAllow(cwd, next)
}
