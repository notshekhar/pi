package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/db"
)

// The statement layer over loop's session tables — a port of
// sessions/sqlite-store.ts. Sessions and entries keep their public ids
// (session ULID, 8-hex entry id) in pub_id columns; the integer keys never
// leave this file. An entry's payload is the full entry JSON and the single
// source of truth; the usage/model columns are derived at insert for
// aggregate queries and never read back into an Entry.

// Scope is which side of the archive a listing wants.
type Scope int

const (
	// Active is the working set (the default).
	Active Scope = iota
	// Archived is only what was put away.
	Archived
	// AllScopes is both.
	AllScopes
)

// Record is a session row.
type Record struct {
	RowID      int64
	Info       Info
	Name       string
	UpdatedAt  int64
	ArchivedAt int64
	// updatedRaw is updated_at as stored (loop writes fractional ms).
	updatedRaw, createdRaw float64
	// FirstUserPayload and LastModel are filled by List.
	FirstUserPayload string
	LastModel        string
}

const sessionColumns = `s.id, s.pub_id, s.cwd, s.provider, s.model, s.name, s.parent_pub,
	s.created_at, s.updated_at, s.archived_at`

// Timestamps are scanned as floats: loop has written fractional
// milliseconds into these columns, and an integer scan rejects the row.
func scanRecord(row interface{ Scan(...any) error }, extra ...any) (Record, error) {
	var r Record
	var name, parent sql.NullString
	var created, updated, archived sql.NullFloat64
	dest := append([]any{&r.RowID, &r.Info.ID, &r.Info.CWD, &r.Info.Provider, &r.Info.Model,
		&name, &parent, &created, &updated, &archived}, extra...)
	if err := row.Scan(dest...); err != nil {
		return Record{}, err
	}
	r.Name, r.Info.ParentSession = name.String, parent.String
	r.Info.CreatedAt, r.UpdatedAt = int64(created.Float64), int64(updated.Float64)
	r.updatedRaw, r.createdRaw = updated.Float64, created.Float64
	if archived.Valid {
		r.ArchivedAt = int64(archived.Float64)
	}
	return r, nil
}

// EnsureSession inserts the session row if it is missing and returns its id.
func EnsureSession(info Info) (int64, error) {
	h, err := db.Open()
	if err != nil {
		return 0, err
	}
	return ensureSession(h, info)
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
	QueryRow(string, ...any) *sql.Row
}

func ensureSession(h execer, info Info) (int64, error) {
	var parent any
	if info.ParentSession != "" {
		parent = info.ParentSession
	}
	if _, err := h.Exec(`INSERT OR IGNORE INTO sessions (pub_id, cwd, provider, model, parent_pub, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		info.ID, info.CWD, info.Provider, info.Model, parent, info.CreatedAt, time.Now().UnixMilli()); err != nil {
		return 0, err
	}
	var id int64
	if err := h.QueryRow("SELECT id FROM sessions WHERE pub_id = ?", info.ID).Scan(&id); err != nil {
		return 0, fmt.Errorf("session row vanished for %s: %w", info.ID, err)
	}
	return id, nil
}

// GetRecord is the session row for a public id, or nil.
func GetRecord(pubID string) (*Record, error) {
	h, err := db.Open()
	if err != nil {
		return nil, err
	}
	r, err := scanRecord(h.QueryRow(`SELECT `+sessionColumns+` FROM sessions AS s WHERE pub_id = ?`, pubID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

var safeID = regexp.MustCompile(`^[0-9A-Za-z_-]{1,64}$`)

// ListRecords lists sessions newest first: in cwd when it is non-empty, on
// one side of the archive per scope. Archived listings sort by when they
// were put away, like loop's.
func ListRecords(cwd string, scope Scope, limit, offset int) ([]Record, error) {
	h, err := db.Open()
	if err != nil {
		return nil, err
	}
	var where []string
	var args []any
	if cwd != "" {
		where = append(where, "s.cwd = ?")
		args = append(args, cwd)
	}
	switch scope {
	case Archived:
		where = append(where, "s.archived_at IS NOT NULL")
	case Active:
		where = append(where, "s.archived_at IS NULL")
	}
	order := "s.updated_at"
	if scope == Archived {
		order = "s.archived_at"
	}
	q := `SELECT ` + sessionColumns + `,
		(SELECT e.payload FROM entries e WHERE e.session_id = s.id AND e.type = 'message' AND e.role = 'user'
		 ORDER BY e.id LIMIT 1),
		(SELECT e.model FROM entries e WHERE e.session_id = s.id AND e.model IS NOT NULL AND e.model <> ''
		 ORDER BY e.id DESC LIMIT 1)
		FROM sessions s`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY " + order + " DESC, s.id DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
		if offset > 0 {
			q += fmt.Sprintf(" OFFSET %d", offset)
		}
	} else if offset > 0 {
		q += fmt.Sprintf(" LIMIT -1 OFFSET %d", offset)
	}
	rows, err := h.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var first, last sql.NullString
		r, err := scanRecord(rows, &first, &last)
		if err != nil {
			return nil, err
		}
		r.FirstUserPayload, r.LastModel = first.String, last.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetArchived puts a session away or takes it back; false for an unknown id.
// No entry is written: archiving is a property of the row, not a turn.
func SetArchived(pubID string, archived bool) (bool, error) {
	h, err := db.Open()
	if err != nil {
		return false, err
	}
	var at any
	if archived {
		at = time.Now().UnixMilli()
	}
	res, err := h.Exec("UPDATE sessions SET archived_at = ? WHERE pub_id = ?", at, pubID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Delete removes a session and its entries; false when there was no such id.
// The cost ledger keeps its rows (ON DELETE SET NULL, session_pub retained):
// deleting a conversation never rewrites what was spent.
func Delete(pubID string) (bool, error) {
	h, err := db.Open()
	if err != nil {
		return false, err
	}
	res, err := h.Exec("DELETE FROM sessions WHERE pub_id = ?", pubID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UpdateCwd re-homes a session (/cd).
func UpdateCwd(pubID, cwd string) error {
	h, err := db.Open()
	if err != nil {
		return err
	}
	_, err = h.Exec("UPDATE sessions SET cwd = ?, updated_at = ? WHERE pub_id = ?", cwd, time.Now().UnixMilli(), pubID)
	return err
}

// appendEntries writes one batch in a single transaction.
func appendEntries(rowID int64, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	h, err := db.Open()
	if err != nil {
		return err
	}
	tx, err := h.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range entries {
		if err := insertEntry(tx, rowID, e); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("UPDATE sessions SET updated_at = ? WHERE id = ?", time.Now().UnixMilli(), rowID); err != nil {
		return err
	}
	if err := applyNameChanges(tx, rowID, entries); err != nil {
		return err
	}
	return tx.Commit()
}

// countEntries is how many rows the session has, written by any process.
func countEntries(rowID int64) (int, error) {
	h, err := db.Open()
	if err != nil {
		return 0, err
	}
	var n int
	err = h.QueryRow("SELECT COUNT(*) FROM entries WHERE session_id = ?", rowID).Scan(&n)
	return n, err
}

// loadEntries reads a session's rows in insertion order. A payload that does
// not parse is skipped, not fatal — loop's adapter drops it the same way.
func loadEntries(rowID int64) ([]Entry, int, error) {
	h, err := db.Open()
	if err != nil {
		return nil, 0, err
	}
	rows, err := h.Query("SELECT payload FROM entries WHERE session_id = ? ORDER BY id", rowID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Entry
	seen := 0
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, 0, err
		}
		seen++
		var e Entry
		if json.Unmarshal([]byte(payload), &e) == nil && e.Type != "" {
			out = append(out, e)
		}
	}
	return out, seen, rows.Err()
}

// replaceEntries rewrites a session's rows wholesale (the legacy-upgrade path).
func replaceEntries(rowID int64, entries []Entry) error {
	h, err := db.Open()
	if err != nil {
		return err
	}
	tx, err := h.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM entries WHERE session_id = ?", rowID); err != nil {
		return err
	}
	for _, e := range entries {
		if err := insertEntry(tx, rowID, e); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// insertSessionWithEntries creates a session and its rows atomically (fork).
func insertSessionWithEntries(info Info, entries []Entry) (int64, error) {
	h, err := db.Open()
	if err != nil {
		return 0, err
	}
	tx, err := h.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rowID, err := ensureSession(tx, info)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if err := insertEntry(tx, rowID, e); err != nil {
			return 0, err
		}
	}
	if err := applyNameChanges(tx, rowID, entries); err != nil {
		return 0, err
	}
	return rowID, tx.Commit()
}

// DailyTokens is input+output tokens per local calendar day across every
// session — the /steak heatmap. Estimated rows are excluded, like /cost.
func DailyTokens() (map[string]int64, error) {
	h, err := db.Open()
	if err != nil {
		return nil, err
	}
	rows, err := h.Query(`SELECT date(ts / 1000, 'unixepoch', 'localtime') AS day,
			SUM(COALESCE(usage_input, 0) + COALESCE(usage_output, 0)) AS toks
		FROM entries
		WHERE ((type = 'message' AND role = 'assistant') OR type = 'subagent')
		  AND COALESCE(usage_input, 0) + COALESCE(usage_output, 0) > 0
		  AND COALESCE(usage_estimated, 0) = 0
		GROUP BY day`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var day string
		var toks int64
		if err := rows.Scan(&day, &toks); err != nil {
			return nil, err
		}
		out[day] = toks
	}
	return out, rows.Err()
}

func nullable[T comparable](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func insertEntry(tx *sql.Tx, rowID int64, e Entry) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var (
		role, parent, model any
		u                   NormalizedUsage
		hasUsage            bool
	)
	if e.Type == TypeMessage {
		role = e.Role
	}
	if e.ParentID != "" {
		parent = e.ParentID
	}
	if e.Usage != nil && owns(e.Type, "usage") {
		u, hasUsage = e.Usage.Normalize(), true
	}
	if e.Model != "" && owns(e.Type, "model") {
		model = e.Model
	}
	var estimated any
	if hasUsage {
		estimated = 0
		if u.Estimated {
			estimated = 1
		}
	}
	_, err = tx.Exec(`INSERT INTO entries (
			session_id, pub_id, parent_pub_id, ts, type, role, payload,
			usage_input, usage_output, usage_total, usage_no_cache,
			usage_cache_read, usage_cache_write, usage_text,
			usage_reasoning, usage_estimated, usage_usd, model
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rowID, e.ID, parent, e.TS, e.Type, role, string(payload),
		nullable(u.Input), nullable(u.Output), nullable(u.Total), nullable(u.NoCache),
		nullable(u.CacheRead), nullable(u.CacheWrite), nullable(u.Text),
		nullable(u.Reasoning), estimated, nullable(u.USD), model)
	return err
}

// applyNameChanges denormalises the latest session-name in a batch onto the
// session row.
func applyNameChanges(tx *sql.Tx, rowID int64, entries []Entry) error {
	seen := false
	var name any
	for _, e := range entries {
		if e.Type != TypeSessionName {
			continue
		}
		seen = true
		name = nil
		if e.Name != nil {
			if n := strings.TrimSpace(*e.Name); n != "" {
				name = n
			}
		}
	}
	if !seen {
		return nil
	}
	_, err := tx.Exec("UPDATE sessions SET name = ? WHERE id = ?", name, rowID)
	return err
}
