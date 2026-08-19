package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/db"
)

// Persistence: SQLite, one row per session and one row per message, in the
// single database `core/db` owns.
//
// This replaced two files per session (`<id>.jsonl` + `<id>.meta.json`). The
// properties that design was chosen for are kept, because they are the ones
// that matter for a conversation:
//
//   - a turn APPENDS and nothing rewrites, so the cost of a turn does not
//     grow with the length of the conversation
//   - the message is stored as the same wire JSON the codec already produced,
//     so what can be resumed is exactly what could be resumed before —
//     including signed reasoning, which some providers reject a session
//     without
//   - the conversation is still exportable as JSONL, byte for byte the old
//     format, because that was a property of the codec and not of the files
//
// What the files could not do is answer a question across sessions without
// reading all of them: listing, usage totals, "what did I spend today",
// "which sessions ran in this repo". Those are one query now.
//
// Encode-then-write is preserved and is the subtle one: a message this codec
// cannot represent must fail BEFORE anything is written, or a half-written
// turn leaves a conversation the provider will reject on resume.

// Meta is a session's header: everything /sessions needs without reading the
// conversation itself.
type Meta struct {
	ID string `json:"id"`
	// Title is derived from the first prompt. Name is what the user set with
	// /name, and the two are kept apart because a picker shows them in
	// different places: the name identifies the session, the first prompt
	// says what it was about.
	Title   string    `json:"title,omitempty"`
	Name    string    `json:"name,omitempty"`
	Model   string    `json:"model,omitempty"`
	CWD     string    `json:"cwd,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// Messages is the count at last write, so a listing need not read the
	// conversation to show how big a session is.
	Messages int `json:"messages"`
	// Parent is the session this one was forked or cloned from, empty for a
	// session started fresh. It is what makes a session TREE possible: the
	// branches are not derivable from the conversations themselves, because
	// a fork's early messages are byte-identical to its parent's.
	Parent string `json:"parent,omitempty"`
	// ForkedAt is how many messages the parent had when this branched, so a
	// tree can say where the two diverged.
	ForkedAt int `json:"forkedAt,omitempty"`
	// Usage totals for this session, denormalised onto the row so `/cost
	// --all` is one query and never opens a conversation.
	InputTokens  int64   `json:"inputTokens,omitempty"`
	OutputTokens int64   `json:"outputTokens,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
}

// Label is the one-line description shown in a picker.
func (m Meta) Label() string {
	if m.Name != "" {
		return m.Name
	}
	if m.Title == "" {
		return "(untitled)"
	}
	return m.Title
}

// Detail is the dim second column of a picker row.
func (m Meta) Detail() string {
	parts := []string{m.Updated.Format("Jan 2 15:04")}
	if m.Messages > 0 {
		parts = append(parts, fmt.Sprintf("%d msgs", m.Messages))
	}
	if m.Model != "" {
		parts = append(parts, m.Model)
	}
	return strings.Join(parts, " · ")
}

// conn opens the database and runs the one-time import of any sessions left
// over from the JSONL era.
func conn() (*sql.DB, error) {
	h, err := db.Open()
	if err != nil {
		return nil, err
	}
	importLegacy(h)
	return h, nil
}

// New starts a session WITHOUT touching the disk.
//
// A session with nothing in it is not a session. Writing one at startup meant
// every launch — every `--help`, every look at the model picker, every
// abandoned window — left one behind, and the session list filled up with
// empty conversations nobody had. The row is claimed on the first message
// instead (see materialize), which is also the moment the session first has
// anything worth resuming.
func New(model, cwd string) *Session {
	now := time.Now()
	return &Session{Meta: Meta{Model: model, CWD: cwd, Created: now, Updated: now}}
}

// Saved reports whether a session has been written yet.
func (s *Session) Saved() bool { return s.ID != "" }

// materialize claims an id and inserts the row, if that has not happened.
func (s *Session) materialize() error {
	if s.ID != "" {
		return nil
	}
	created, err := Create(s.Meta.Model, s.Meta.CWD)
	if err != nil {
		return err
	}
	s.ID, s.Path = created.ID, created.Path
	s.Meta.ID, s.Meta.Created = created.ID, created.Meta.Created
	return nil
}

// Create starts a new persisted session.
//
// The id is claimed by INSERT against a unique index rather than merely
// timestamped: a timestamp alone collides whenever two sessions start inside
// the same millisecond, and a fork — which creates a session immediately
// after reading one — hits that window routinely. The database decides who
// won, which is the one arbitration that cannot race.
func Create(model, cwd string) (*Session, error) {
	h, err := conn()
	if err != nil {
		return nil, err
	}
	p, _ := db.Path()
	stamp := time.Now().UTC().Format("20060102T150405.000")
	for attempt := 0; ; attempt++ {
		id := stamp
		if attempt > 0 {
			id = fmt.Sprintf("%s-%d", stamp, attempt)
		}
		now := time.Now()
		_, err := h.Exec(
			`INSERT INTO sessions (pub_id, model, cwd, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?)`,
			id, model, cwd, now.UnixMilli(), now.UnixMilli(),
		)
		if err != nil {
			if isUniqueViolation(err) {
				if attempt > 1000 {
					return nil, fmt.Errorf("session: could not claim an id near %s", stamp)
				}
				continue
			}
			return nil, err
		}
		return &Session{
			ID:   id,
			Path: p,
			Meta: Meta{ID: id, Model: model, CWD: cwd, Created: now, Updated: now},
		}, nil
	}
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

// Load reads a session back.
func Load(id string) (*Session, error) {
	h, err := conn()
	if err != nil {
		return nil, err
	}
	rowID, meta, err := readSession(h, id)
	if err != nil {
		return nil, err
	}
	p, _ := db.Path()
	s := &Session{ID: id, Path: p, Meta: meta}

	rows, err := h.Query(`SELECT payload FROM entries WHERE session_id = ? ORDER BY seq`, rowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for i := 0; rows.Next(); i++ {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var w wireMessage
		if err := json.Unmarshal([]byte(payload), &w); err != nil {
			return nil, fmt.Errorf("session %s: message %d: %w", id, i+1, err)
		}
		msg, err := decodeMessage(w)
		if err != nil {
			return nil, fmt.Errorf("session %s: message %d: %w", id, i+1, err)
		}
		s.Messages = append(s.Messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.Meta.ID = id
	s.Meta.Messages = len(s.Messages)
	return s, nil
}

// readSession fetches the header row and its internal id.
func readSession(h *sql.DB, id string) (int64, Meta, error) {
	var (
		rowID            int64
		title, name      sql.NullString
		parent           sql.NullString
		modelStr, cwdStr string
		forkedAt         int
		created, updated int64
		inTok, outTok    int64
		costUSD          float64
		messages         int
	)
	err := h.QueryRow(
		`SELECT s.id, s.title, s.name, s.model, s.cwd, s.parent_pub, s.forked_at,
		        s.created_at, s.updated_at, s.input_tokens, s.output_tokens, s.cost_usd,
		        (SELECT COUNT(*) FROM entries e WHERE e.session_id = s.id)
		 FROM sessions s WHERE s.pub_id = ?`, id,
	).Scan(&rowID, &title, &name, &modelStr, &cwdStr, &parent, &forkedAt,
		&created, &updated, &inTok, &outTok, &costUSD, &messages)
	if err == sql.ErrNoRows {
		return 0, Meta{}, fmt.Errorf("session %s: not found", id)
	}
	if err != nil {
		return 0, Meta{}, err
	}
	return rowID, Meta{
		ID: id, Title: title.String, Name: name.String,
		Model: modelStr, CWD: cwdStr, Parent: parent.String, ForkedAt: forkedAt,
		Created: time.UnixMilli(created), Updated: time.UnixMilli(updated),
		Messages: messages, InputTokens: inTok, OutputTokens: outTok, CostUSD: costUSD,
	}, nil
}

// List returns every stored session, newest first.
//
// The row id breaks a tie on the timestamp. Two sessions created inside the
// same millisecond — a fork and its parent, always — would otherwise come
// back in whatever order the query planner felt like, and "newest first" is
// the one thing a session picker has to get right.
func List() ([]Meta, error) {
	h, err := conn()
	if err != nil {
		return nil, err
	}
	rows, err := h.Query(
		`SELECT s.pub_id, s.title, s.name, s.model, s.cwd, s.parent_pub, s.forked_at,
		        s.created_at, s.updated_at, s.input_tokens, s.output_tokens, s.cost_usd,
		        (SELECT COUNT(*) FROM entries e WHERE e.session_id = s.id)
		 FROM sessions s ORDER BY s.updated_at DESC, s.id DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Meta
	for rows.Next() {
		var (
			id               string
			title, name      sql.NullString
			parent           sql.NullString
			modelStr, cwdStr string
			forkedAt         int
			created, updated int64
			inTok, outTok    int64
			costUSD          float64
			messages         int
		)
		if err := rows.Scan(&id, &title, &name, &modelStr, &cwdStr, &parent, &forkedAt,
			&created, &updated, &inTok, &outTok, &costUSD, &messages); err != nil {
			return nil, err
		}
		out = append(out, Meta{
			ID: id, Title: title.String, Name: name.String,
			Model: modelStr, CWD: cwdStr, Parent: parent.String, ForkedAt: forkedAt,
			Created: time.UnixMilli(created), Updated: time.UnixMilli(updated),
			Messages: messages, InputTokens: inTok, OutputTokens: outTok, CostUSD: costUSD,
		})
	}
	return out, rows.Err()
}

// Fork copies a session into a new one, so a conversation can branch without
// disturbing the original.
func Fork(s *Session) (*Session, error) { return ForkAt(s, len(s.Messages)) }

// ForkAt branches a session, keeping only the first n messages.
//
// This is what makes a fork a BRANCH rather than a copy: rewinding to an
// earlier message and going a different way is the whole reason to fork, and
// a full duplicate is /clone.
func ForkAt(s *Session, n int) (*Session, error) {
	if n < 0 {
		n = 0
	}
	if n > len(s.Messages) {
		n = len(s.Messages)
	}
	forked, err := Create(s.Meta.Model, s.Meta.CWD)
	if err != nil {
		return nil, err
	}
	forked.Meta.Title = forkTitle(s.Meta.Title)
	forked.Meta.Parent = s.ID
	forked.Meta.ForkedAt = n
	forked.Messages = append([]provider.Message{}, s.Messages[:n]...)
	return forked, forked.rewrite()
}

func forkTitle(title string) string {
	if title == "" {
		return "fork"
	}
	return title + " (fork)"
}

// SetTitle renames a session — it sets the user's NAME, leaving the derived
// title alone so the picker can still say what the session was about.
func (s *Session) SetTitle(title string) error {
	s.Meta.Name = title
	return s.saveMeta()
}

// Delete removes a session and its messages. The entries go with it through
// the foreign key, which is why `foreign_keys` is on in the DSN — without it
// the delete silently orphans every message of the conversation.
func Delete(id string) error {
	h, err := conn()
	if err != nil {
		return err
	}
	_, err = h.Exec(`DELETE FROM sessions WHERE pub_id = ?`, id)
	return err
}

// appendWire appends messages. This is the hot path — one turn, one
// transaction, no rewrite.
func (s *Session) appendWire(messages []provider.Message) error {
	if len(messages) == 0 {
		return s.saveMeta()
	}
	// Encode everything BEFORE touching the database: a part this codec
	// cannot represent must fail without having half-written the turn.
	payloads, err := encodeAll(messages)
	if err != nil {
		return err
	}
	// The first message is what makes the session real.
	if err := s.materialize(); err != nil {
		return err
	}
	h, err := conn()
	if err != nil {
		return err
	}
	rowID, err := sessionRowID(h, s.ID)
	if err != nil {
		return err
	}
	// The turn lands whole or not at all. A conversation missing one half of
	// a tool-call/result pair is one the provider rejects outright.
	tx, err := h.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var next int64
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(seq), -1) + 1 FROM entries WHERE session_id = ?`, rowID,
	).Scan(&next); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for i, payload := range payloads {
		if _, err := tx.Exec(
			`INSERT INTO entries (session_id, seq, ts, role, payload) VALUES (?, ?, ?, ?, ?)`,
			rowID, next+int64(i), now, roleOf(messages[i]), payload,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.saveMeta()
}

// rewrite replaces the conversation wholesale. Only compaction and /new need
// it — everything else appends.
func (s *Session) rewrite() error {
	payloads, err := encodeAll(s.Messages)
	if err != nil {
		return err
	}
	if s.ID == "" {
		if len(s.Messages) == 0 {
			return s.saveMeta()
		}
		if err := s.materialize(); err != nil {
			return err
		}
	}
	h, err := conn()
	if err != nil {
		return err
	}
	rowID, err := sessionRowID(h, s.ID)
	if err != nil {
		return err
	}
	tx, err := h.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM entries WHERE session_id = ?`, rowID); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for i, payload := range payloads {
		if _, err := tx.Exec(
			`INSERT INTO entries (session_id, seq, ts, role, payload) VALUES (?, ?, ?, ?, ?)`,
			rowID, i, now, roleOf(s.Messages[i]), payload,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.saveMeta()
}

// saveMeta writes the header row, refreshing the timestamp and count.
func (s *Session) saveMeta() error {
	// An unsaved session has no row and no id yet — see New. Nothing to write
	// until it has something in it.
	if s.ID == "" && len(s.Messages) == 0 {
		s.Meta.Updated = time.Now()
		return nil
	}
	if err := s.materialize(); err != nil {
		return err
	}
	if s.Meta.ID == "" {
		s.Meta.ID = s.ID
	}
	if s.Meta.Created.IsZero() {
		s.Meta.Created = time.Now()
	}
	s.Meta.Updated = time.Now()
	s.Meta.Messages = len(s.Messages)

	h, err := conn()
	if err != nil {
		return err
	}
	_, err = h.Exec(
		`UPDATE sessions SET title = ?, name = ?, model = ?, cwd = ?, parent_pub = ?,
		        forked_at = ?, updated_at = ?, input_tokens = ?, output_tokens = ?, cost_usd = ?
		 WHERE pub_id = ?`,
		nullable(s.Meta.Title), nullable(s.Meta.Name), s.Meta.Model, s.Meta.CWD,
		nullable(s.Meta.Parent), s.Meta.ForkedAt, s.Meta.Updated.UnixMilli(),
		s.Meta.InputTokens, s.Meta.OutputTokens, s.Meta.CostUSD, s.ID,
	)
	return err
}

// AddUsage records a turn's usage against the session, and in the ledger.
//
// Both, deliberately: the session row answers "what did this conversation
// cost" without a scan, and the ledger keeps the per-turn rows a session
// total cannot reconstruct — spend by day, by model, by repo. A total is a
// fact you can derive from the rows; the rows are not derivable from the
// total.
func (s *Session) AddUsage(in, out int64, usd float64) error {
	s.Meta.InputTokens += in
	s.Meta.OutputTokens += out
	s.Meta.CostUSD += usd
	if err := s.saveMeta(); err != nil {
		return err
	}
	h, err := conn()
	if err != nil {
		return err
	}
	now := time.Now()
	_, err = h.Exec(
		`INSERT INTO cost_ledger (ts, day, session_pub, cwd, model, input_tokens, output_tokens, usd)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		now.UnixMilli(), now.Format("2006-01-02"), s.ID, s.Meta.CWD, s.Meta.Model, in, out, usd,
	)
	return err
}

// Usage totals every stored session.
type Usage struct {
	Sessions     int
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// TotalUsage sums usage across every session. One query, whatever the size of
// the history — the thing the header files could not do.
func TotalUsage() (Usage, error) {
	h, err := conn()
	if err != nil {
		return Usage{}, err
	}
	var u Usage
	err = h.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cost_usd), 0) FROM sessions`,
	).Scan(&u.Sessions, &u.InputTokens, &u.OutputTokens, &u.CostUSD)
	return u, err
}

// DaySpend is one day's totals from the ledger.
type DaySpend struct {
	Day          string
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// Spend is the last n days of the cost ledger, newest first. This is the
// question the old header files could not answer at all: they carried session
// totals with no per-turn timestamp, so "today" was not in the data.
func Spend(days int) ([]DaySpend, error) {
	h, err := conn()
	if err != nil {
		return nil, err
	}
	rows, err := h.Query(
		`SELECT day, SUM(input_tokens), SUM(output_tokens), SUM(usd)
		 FROM cost_ledger GROUP BY day ORDER BY day DESC LIMIT ?`, days,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DaySpend
	for rows.Next() {
		var d DaySpend
		if err := rows.Scan(&d.Day, &d.InputTokens, &d.OutputTokens, &d.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Buckets is what `/cost` shows: spend sliced the handful of ways the
// question is actually asked.
type Buckets struct {
	Today      float64
	Week       float64
	Month      float64
	Lifetime   float64
	Directory  float64
	ByProvider map[string]float64
}

// SpendBuckets answers all of it in one pass over the ledger.
//
// Over the LEDGER, which is the difference the database bought. The same
// numbers used to be derived from session totals, and that put a session's
// entire spend on the day it was last touched: a conversation resumed a week
// later moved its whole history into "today", and one that ran past midnight
// reported nothing for the day it started. A ledger row is stamped when the
// turn happened, so a bucket means what it says.
func SpendBuckets(cwd string) (Buckets, error) {
	h, err := conn()
	if err != nil {
		return Buckets{}, err
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	b := Buckets{ByProvider: map[string]float64{}}
	err = h.QueryRow(
		`SELECT COALESCE(SUM(usd), 0),
		        COALESCE(SUM(CASE WHEN ts >= ? THEN usd END), 0),
		        COALESCE(SUM(CASE WHEN ts >= ? THEN usd END), 0),
		        COALESCE(SUM(CASE WHEN ts >= ? THEN usd END), 0),
		        COALESCE(SUM(CASE WHEN cwd = ? THEN usd END), 0)
		 FROM cost_ledger`,
		today.UnixMilli(),
		today.AddDate(0, 0, -6).UnixMilli(),
		time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).UnixMilli(),
		cwd,
	).Scan(&b.Lifetime, &b.Today, &b.Week, &b.Month, &b.Directory)
	if err != nil {
		return Buckets{}, err
	}
	// The PROVIDER half of "provider/model": the full id would split one
	// vendor's spend across every model it serves, which is the opposite of
	// what this row is for.
	rows, err := h.Query(
		`SELECT model, SUM(usd) FROM cost_ledger WHERE usd > 0 GROUP BY model`)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var model string
		var usd float64
		if err := rows.Scan(&model, &usd); err != nil {
			return b, err
		}
		if p, _, found := strings.Cut(model, "/"); found && p != "" {
			b.ByProvider[p] += usd
		}
	}
	return b, rows.Err()
}

// JSONL renders the conversation in the on-disk format sessions used to have:
// one wire message per line. `/export foo.jsonl` used to copy the body file,
// which is what tied the export to the storage — this ties it to the codec
// instead, which is where it always belonged.
func JSONL(messages []provider.Message) ([]byte, error) {
	payloads, err := encodeAll(messages)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, p := range payloads {
		b.WriteString(p)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// encodeAll encodes every message, or fails having written nothing.
func encodeAll(messages []provider.Message) ([]string, error) {
	out := make([]string, 0, len(messages))
	for _, msg := range messages {
		wire, err := encodeMessage(msg)
		if err != nil {
			return nil, err
		}
		line, err := json.Marshal(wire)
		if err != nil {
			return nil, err
		}
		out = append(out, string(line))
	}
	return out, nil
}

// roleOf is a denormalised column, so a query can count what a conversation
// is made of without decoding every payload.
func roleOf(m provider.Message) string {
	switch m.(type) {
	case provider.UserMessage:
		return "user"
	case provider.AssistantMessage:
		return "assistant"
	case provider.ToolMessage:
		return "tool"
	case provider.SystemMessage:
		return "system"
	}
	return ""
}

func sessionRowID(h *sql.DB, pubID string) (int64, error) {
	var id int64
	err := h.QueryRow(`SELECT id FROM sessions WHERE pub_id = ?`, pubID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("session %s: not found", pubID)
	}
	return id, err
}

// nullable keeps empty strings out of the database as NULL, so "unset" and
// "set to empty" stay distinguishable in a query.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
