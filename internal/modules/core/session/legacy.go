package session

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Importing the conversations from before the database.
//
// Sessions used to be two files each — `<id>.jsonl` and `<id>.meta.json` —
// under ~/.pi-agent/sessions. Anyone who has been using this has real work in
// there, and a storage change that silently starts a fresh history is
// indistinguishable to them from one that deleted it.
//
// The rules this import follows, and each is deliberate:
//
//   - It runs ONCE, marked in `meta`, not once per process and not per
//     session file. A repeated import would either duplicate every
//     conversation or need per-row conflict handling on a path that only ever
//     runs on old installs.
//   - The files are LEFT ALONE. They are the only copy of the conversations
//     until this has been proven on real data, and moving them is one
//     unnecessary way to lose them. They cost nothing sitting there.
//   - A file that will not parse is SKIPPED, not fatal. One damaged session
//     from an old crash must not be able to block the import of the other
//     two hundred — the same rule the old List had, for the same reason.
//   - The tail of a body is tolerated: a torn final line is what a crash
//     mid-append looks like, and the rest of that conversation is good.

const importedKey = "imported_jsonl"

var (
	importMu sync.Mutex
	// importedFor is the handle the import has already been considered for.
	// Keyed on the HANDLE rather than a plain sync.Once because the database
	// can be repointed — a test does it every time, and a once that fired
	// against the first path would skip the import for every path after it.
	importedFor *sql.DB
)

// legacyDir is where the JSONL sessions live. Not created here: if it does
// not exist there is nothing to import, and making it would leave an empty
// directory behind on every fresh install forever.
func legacyDir() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	dir := filepath.Join(home, ".pi-agent", "sessions")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// importLegacy brings the file-era sessions into the database, once.
//
// Errors are swallowed on purpose. This runs on the way to opening a session,
// and an import that cannot finish must not be able to stop the agent from
// starting — the marker is only written on success, so a failed attempt is
// simply retried next launch.
func importLegacy(h *sql.DB) {
	importMu.Lock()
	defer importMu.Unlock()
	if importedFor == h {
		return
	}
	importedFor = h

	var done string
	_ = h.QueryRow(`SELECT value FROM meta WHERE key = ?`, importedKey).Scan(&done)
	if done == "1" {
		return
	}
	dir, ok := legacyDir()
	if !ok {
		markImported(h)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Retried next launch: the marker is only written on success.
		importedFor = nil
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".meta.json") {
			continue
		}
		// One damaged session from an old crash must not block the other two
		// hundred, which is the rule the file-era List had for the same
		// reason.
		_ = importOne(h, dir, strings.TrimSuffix(name, ".meta.json"))
	}
	markImported(h)
}

func markImported(h *sql.DB) {
	_, _ = h.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, '1')`, importedKey)
}

// importOne inserts one file-era session and its messages.
func importOne(h *sql.DB, dir, id string) error {
	meta, err := readLegacyMeta(filepath.Join(dir, id+".meta.json"), id)
	if err != nil {
		return err
	}
	payloads := readLegacyBody(filepath.Join(dir, id+".jsonl"))

	tx, err := h.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT OR IGNORE INTO sessions
		    (pub_id, title, name, model, cwd, parent_pub, forked_at, created_at, updated_at,
		     input_tokens, output_tokens, cost_usd)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, nullable(meta.Title), nullable(meta.Name), meta.Model, meta.CWD,
		nullable(meta.Parent), meta.ForkedAt,
		meta.Created.UnixMilli(), meta.Updated.UnixMilli(),
		meta.InputTokens, meta.OutputTokens, meta.CostUSD,
	)
	if err != nil {
		return err
	}
	rowID, err := res.LastInsertId()
	if err != nil || rowID == 0 {
		// Already present — an earlier partial import. Leave it as it is
		// rather than duplicating its messages.
		return nil
	}
	// The payload is stored EXACTLY as the file had it. It is already the
	// wire form this codec writes, so re-encoding would only add a chance of
	// changing it — and the signed reasoning inside some of these lines is
	// rejected by the provider if a single byte moves.
	for i, payload := range payloads {
		if _, err := tx.Exec(
			`INSERT INTO entries (session_id, seq, ts, role, payload) VALUES (?, ?, ?, ?, ?)`,
			rowID, i, meta.Updated.UnixMilli(), legacyRole(payload), payload,
		); err != nil {
			return err
		}
	}
	// One ledger row standing in for the session's whole spend, dated when it
	// was last touched.
	//
	// The file era had no per-turn record at all — a session carried one
	// total — so this is exactly as precise as the data ever was, and no
	// less. Without it every question the ledger answers would read $0 for
	// everything that happened before the database, which looks like the
	// migration lost the money rather than never having had the breakdown.
	if meta.CostUSD != 0 || meta.InputTokens != 0 || meta.OutputTokens != 0 {
		if _, err := tx.Exec(
			`INSERT INTO cost_ledger (ts, day, session_pub, cwd, model, input_tokens, output_tokens, usd)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			meta.Updated.UnixMilli(), meta.Updated.Format("2006-01-02"), id, meta.CWD, meta.Model,
			meta.InputTokens, meta.OutputTokens, meta.CostUSD,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func readLegacyMeta(path, id string) (Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, err
	}
	if m.ID == "" {
		m.ID = id
	}
	if m.Created.IsZero() {
		m.Created = time.Now()
	}
	if m.Updated.IsZero() {
		m.Updated = m.Created
	}
	return m, nil
}

// readLegacyBody returns the raw wire lines, dropping a torn tail.
func readLegacyBody(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var lines []string
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	// A line that will not parse is dropped only if it is the LAST one, which
	// is the signature of a crash mid-append. The same damage earlier is real
	// corruption, and importing the good prefix is still better than dropping
	// the conversation — but the bad line itself must not go in, because
	// nothing downstream could decode it.
	var out []string
	for _, line := range lines {
		var w wireMessage
		if json.Unmarshal([]byte(line), &w) != nil {
			continue
		}
		if _, err := decodeMessage(w); err != nil {
			continue
		}
		out = append(out, line)
	}
	return out
}

// legacyRole reads the role straight off the stored line, for the
// denormalised column. It is a hint for queries, so an unreadable one is
// simply empty.
func legacyRole(payload string) string {
	var w struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal([]byte(payload), &w)
	return w.Role
}
