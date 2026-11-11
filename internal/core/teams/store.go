// Package teams is loop's thread teams (teams/*.ts): an agent splits a job
// across threads that run in parallel, know about each other, message each
// other, and report back to the thread that started them. Behind the
// `threadTeams` setting, off by default.
//
//	store.go    who is in which team, their states, the mail (SQLite)
//	runtime.go  who runs a member's turns, delivery, change notices
//	tools.go    spawn_threads, send_message, team_board, wait_for_team, report
//	prompts.go  what the model is told
//	budget.go   the spend cap that stops a team
//
// The tables are loop's, in the shared agent.db, read and written in exactly
// loop's shapes: a team pi starts is one loop's desktop can show, and back.
package teams

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/session"
)

// Role is a member's place in its team.
type Role string

const (
	RoleLead   Role = "lead"
	RoleMember Role = "member"
)

// State is what a member is doing, as every client shows it.
//
//	starting  created, its first turn not begun yet
//	running   a turn is going
//	waiting   inside wait_for_team
//	idle      its last turn ended without a report
//	done      it reported (until a message wakes it again)
//	failed    its last turn ended on an error
//	stopped   the team was stopped
type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateWaiting  State = "waiting"
	StateIdle     State = "idle"
	StateDone     State = "done"
	StateFailed   State = "failed"
	StateStopped  State = "stopped"
)

// Finished is whether a member has no turn going or coming.
func (s State) Finished() bool {
	return s == StateIdle || s == StateDone || s == StateFailed || s == StateStopped
}

func (s State) busy() bool { return s == StateRunning || s == StateStarting || s == StateWaiting }

// HeartbeatInterval is how often a turn in a team touches its row: a long
// tool call or a wait_for_team moves nothing else, and the row must still
// look alive to every process reading it.
const HeartbeatInterval = 60 * time.Second

// staleAfter is three missed heartbeats. A process that quit mid-team leaves
// its threads "running" on disk for good; past this nothing is running them,
// whatever the row says.
const staleAfter = 3 * HeartbeatInterval

// Membership is a session's place in a team.
type Membership struct {
	TeamID    string
	SessionID string
	Role      Role
	State     State
	// Activity is what it is doing right now, while it works.
	Activity  string
	LeadID    string
	CreatedAt int64
	UpdatedAt int64
}

// Effective is what a member is really doing: its row's state, unless that
// is a busy state gone stale.
func (m Membership) Effective(now time.Time) State {
	if m.State.busy() && now.UnixMilli()-m.UpdatedAt > staleAfter.Milliseconds() {
		return StateIdle
	}
	return m.State
}

// Member is a roster row: a membership plus the session's title and cwd.
type Member struct {
	Membership
	Title string
	CWD   string
}

// Kind is a piece of team mail's kind.
//
//	message  send_message — one row per recipient
//	report   a member's report to its lead
//	update   a note from the harness: joined, ended without a report, stopped
//	board    a team_board note; To is empty and BoardKey names it
type Kind string

const (
	KindMessage Kind = "message"
	KindReport  Kind = "report"
	KindUpdate  Kind = "update"
	KindBoard   Kind = "board"
)

// Message is one piece of team mail.
type Message struct {
	ID          int64
	TeamID      string
	From        string
	To          string
	Kind        Kind
	BoardKey    string
	Body        string
	TS          int64
	DeliveredAt int64
}

// BoardNote is the board's current note under one key.
type BoardNote struct {
	Key  string
	Body string
	From string
	TS   int64
}

func now() int64 { return time.Now().UnixMilli() }

// withTx runs f in one transaction.
func withTx(f func(*sql.Tx) error) error {
	h, err := db.Open()
	if err != nil {
		return err
	}
	tx, err := h.Begin()
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func exec(query string, args ...any) error {
	h, err := db.Open()
	if err != nil {
		return err
	}
	_, err = h.Exec(query, args...)
	return err
}

// CreateTeam is the team led by leadID — a new one, with the lead as its
// first member, unless it already leads one: two tools in one step may both
// ask, and a second team would orphan the first.
func CreateTeam(leadID string) (string, error) {
	if current, ok := TeamOf(leadID); ok && current.Role == RoleLead {
		return current.TeamID, nil
	}
	id := "tm_" + session.NewULID()
	ts := now()
	err := withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO teams (id, lead_pub, created_at) VALUES (?, ?, ?)", id, leadID, ts); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO team_members (session_pub, team_id, role, state, created_at, updated_at)
			VALUES (?, ?, 'lead', 'running', ?, ?)`, leadID, id, ts, ts)
		return err
	})
	return id, err
}

// AddMember puts a session in a team.
func AddMember(teamID, sessionID string, state State) error {
	ts := now()
	return exec(`INSERT OR REPLACE INTO team_members (session_pub, team_id, role, state, created_at, updated_at)
		VALUES (?, ?, 'member', ?, ?, ?)`, sessionID, teamID, string(state), ts, ts)
}

const memberSelect = `SELECT m.session_pub, m.team_id, m.role, m.state, m.activity, m.created_at, m.updated_at,
	t.lead_pub, s.name, s.cwd
	FROM team_members m
	JOIN teams t ON t.id = m.team_id
	LEFT JOIN sessions s ON s.pub_id = m.session_pub`

func scanMember(rows interface{ Scan(...any) error }) (Member, error) {
	var m Member
	var role, state string
	var activity, name, cwd sql.NullString
	err := rows.Scan(&m.SessionID, &m.TeamID, &role, &state, &activity, &m.CreatedAt, &m.UpdatedAt, &m.LeadID, &name, &cwd)
	m.Role, m.State, m.Activity, m.CWD = RoleMember, State(state), activity.String, cwd.String
	if role == string(RoleLead) {
		m.Role = RoleLead
	}
	m.Title = strings.TrimSpace(name.String)
	if m.Title == "" {
		m.Title = map[Role]string{RoleLead: "Lead", RoleMember: "Thread"}[m.Role]
	}
	return m, err
}

// TeamOf is the team a session belongs to, and its place in it.
func TeamOf(sessionID string) (Membership, bool) {
	h, err := db.Open()
	if err != nil {
		return Membership{}, false
	}
	m, err := scanMember(h.QueryRow(memberSelect+" WHERE m.session_pub = ?", sessionID))
	if err != nil {
		return Membership{}, false
	}
	return m.Membership, true
}

// ListTeam is everyone in the team, the lead first, then members in the
// order they joined.
func ListTeam(teamID string) ([]Member, error) {
	h, err := db.Open()
	if err != nil {
		return nil, err
	}
	rows, err := h.Query(memberSelect+` WHERE m.team_id = ?
		ORDER BY CASE m.role WHEN 'lead' THEN 0 ELSE 1 END, m.created_at, m.rowid`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// maxTitleRunes caps a title taken from a session's first message.
const maxTitleRunes = 60

// SessionTitle is a session's title for team cards and the model: its name,
// else what it was first asked, else its id's tail.
func SessionTitle(sessionID string) string {
	h, err := db.Open()
	if err == nil {
		var name, first sql.NullString
		err = h.QueryRow(`SELECT s.name, (
				SELECT e.payload FROM entries e
				WHERE e.session_id = s.id AND e.type = 'message' AND e.role = 'user'
				ORDER BY e.id LIMIT 1
			) FROM sessions s WHERE s.pub_id = ?`, sessionID).Scan(&name, &first)
		if err == nil {
			if title := strings.TrimSpace(name.String); title != "" {
				return title
			}
			if line := firstLine(first.String); line != "" {
				return line
			}
		}
	}
	return "thread " + sessionID[max(0, len(sessionID)-6):]
}

// firstLine is a user message payload's first line, whitespace collapsed
// and capped.
func firstLine(payload string) string {
	var msg struct {
		Content any `json:"content"`
	}
	if json.Unmarshal([]byte(payload), &msg) != nil {
		return ""
	}
	text, _ := msg.Content.(string)
	if text = strings.TrimSpace(text); text == "" {
		return ""
	}
	line := strings.Join(strings.Fields(strings.SplitN(text, "\n", 2)[0]), " ")
	if r := []rune(line); len(r) > maxTitleRunes {
		return string(r[:maxTitleRunes-1]) + "…"
	}
	return line
}

// IsStopped is whether the team was stopped: no new turns start in it.
func IsStopped(teamID string) bool {
	h, err := db.Open()
	if err != nil {
		return false
	}
	var stopped sql.NullInt64
	if h.QueryRow("SELECT stopped_at FROM teams WHERE id = ?", teamID).Scan(&stopped) != nil {
		return false
	}
	return stopped.Valid && stopped.Int64 != 0
}

// MarkStopped stops or restarts a team.
func MarkStopped(teamID string, stopped bool) error {
	var at any
	if stopped {
		at = now()
	}
	return exec("UPDATE teams SET stopped_at = ? WHERE id = ?", at, teamID)
}

// KeepActivity, passed to SetState, keeps a working member's activity and
// clears a finished one's.
const KeepActivity = "\x00keep"

// SetState moves a member to state; activity replaces what it is doing (""
// clears it, KeepActivity keeps it while working). Reports whether anything
// changed, so callers announce only real moves.
func SetState(sessionID string, state State, activity string) bool {
	current, ok := TeamOf(sessionID)
	if !ok {
		return false
	}
	if activity == KeepActivity {
		activity = current.Activity
		if state.Finished() {
			activity = ""
		}
	}
	if current.State == state && current.Activity == activity {
		return false
	}
	return exec("UPDATE team_members SET state = ?, activity = ?, updated_at = ? WHERE session_pub = ?",
		string(state), nullable(activity), now(), sessionID) == nil
}

// SetActivity records what a member is doing right now. Reports whether it
// changed.
func SetActivity(sessionID, activity string) bool {
	current, ok := TeamOf(sessionID)
	if !ok || current.Activity == activity {
		return false
	}
	return exec("UPDATE team_members SET activity = ?, updated_at = ? WHERE session_pub = ?",
		nullable(activity), now(), sessionID) == nil
}

// Touch keeps a member's row alive while its turn runs.
func Touch(sessionID string) {
	_ = exec("UPDATE team_members SET updated_at = ? WHERE session_pub = ?", now(), sessionID)
}

// RemoveTeam undoes a team that never got going: its rows and all its mail.
func RemoveTeam(teamID string) error {
	return withTx(func(tx *sql.Tx) error {
		for _, q := range []string{
			"DELETE FROM team_member_runs WHERE session_pub IN (SELECT session_pub FROM team_members WHERE team_id = ?)",
			"DELETE FROM team_members WHERE team_id = ?",
			"DELETE FROM team_messages WHERE team_id = ?",
			"DELETE FROM teams WHERE id = ?",
		} {
			if _, err := tx.Exec(q, teamID); err != nil {
				return err
			}
		}
		return nil
	})
}

// RunConfig is how a thread runs its turns, as the lead started it —
// whichever process runs them.
type RunConfig struct {
	Agent    string
	Thinking string
}

// SetRunConfig records how a member runs.
func SetRunConfig(sessionID string, c RunConfig) error {
	return exec("INSERT OR REPLACE INTO team_member_runs (session_pub, agent, thinking, created_at) VALUES (?, ?, ?, ?)",
		sessionID, nullable(c.Agent), nullable(c.Thinking), now())
}

// MemberRunConfig is how a member runs; ok is false when none was recorded.
func MemberRunConfig(sessionID string) (RunConfig, bool) {
	h, err := db.Open()
	if err != nil {
		return RunConfig{}, false
	}
	var agent, thinking sql.NullString
	if h.QueryRow("SELECT agent, thinking FROM team_member_runs WHERE session_pub = ?", sessionID).Scan(&agent, &thinking) != nil {
		return RunConfig{}, false
	}
	return RunConfig{Agent: agent.String, Thinking: thinking.String}, true
}

// PostMessages mails one message to each recipient. One row each, so every
// inbox is read — and marked read — on its own.
func PostMessages(teamID, from string, to []string, kind Kind, body string) ([]Message, error) {
	if kind == KindBoard {
		return nil, errors.New("teams: board notes go through PostBoardNote")
	}
	ts := now()
	var out []Message
	err := withTx(func(tx *sql.Tx) error {
		for _, recipient := range to {
			res, err := tx.Exec("INSERT INTO team_messages (team_id, from_pub, to_pub, kind, body, ts) VALUES (?, ?, ?, ?, ?, ?)",
				teamID, from, recipient, string(kind), body, ts)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			out = append(out, Message{ID: id, TeamID: teamID, From: from, To: recipient, Kind: kind, Body: body, TS: ts})
		}
		return nil
	})
	return out, err
}

func scanMessage(rows *sql.Rows) (Message, error) {
	var m Message
	var to, key sql.NullString
	var delivered sql.NullInt64
	var kind string
	err := rows.Scan(&m.ID, &m.TeamID, &m.From, &to, &kind, &key, &m.Body, &m.TS, &delivered)
	m.To, m.Kind, m.BoardKey, m.DeliveredAt = to.String, Kind(kind), key.String, delivered.Int64
	return m, err
}

const messageColumns = "id, team_id, from_pub, to_pub, kind, board_key, body, ts, delivered_at"

// TakeInbox is a session's unread mail, oldest first, marked read as it is
// taken.
func TakeInbox(sessionID string) ([]Message, error) {
	var taken []Message
	err := withTx(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT "+messageColumns+" FROM team_messages WHERE to_pub = ? AND delivered_at IS NULL ORDER BY id", sessionID)
		if err != nil {
			return err
		}
		for rows.Next() {
			m, err := scanMessage(rows)
			if err != nil {
				rows.Close()
				return err
			}
			taken = append(taken, m)
		}
		rows.Close()
		ts := now()
		for i := range taken {
			taken[i].DeliveredAt = ts
			if _, err := tx.Exec("UPDATE team_messages SET delivered_at = ? WHERE id = ?", ts, taken[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
	return taken, err
}

// ReturnToInbox marks taken mail unread again — the turn meant to read it
// never ran.
func ReturnToInbox(ids []int64) error {
	return withTx(func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec("UPDATE team_messages SET delivered_at = NULL WHERE id = ?", id); err != nil {
				return err
			}
		}
		return nil
	})
}

func count(query string, args ...any) int {
	h, err := db.Open()
	if err != nil {
		return 0
	}
	var n int
	_ = h.QueryRow(query, args...).Scan(&n)
	return n
}

// HasMailedSince is whether from has written to to since ts, read or not.
func HasMailedSince(from, to string, since int64) bool {
	return count("SELECT COUNT(*) FROM team_messages WHERE from_pub = ? AND to_pub = ? AND ts >= ?", from, to, since) > 0
}

// InboxCount is how much unread mail a session has.
func InboxCount(sessionID string) int {
	return count("SELECT COUNT(*) FROM team_messages WHERE to_pub = ? AND delivered_at IS NULL", sessionID)
}

// PostBoardNote adds or replaces a note on the team's board.
func PostBoardNote(teamID, from, key, body string) (BoardNote, error) {
	ts := now()
	err := exec("INSERT INTO team_messages (team_id, from_pub, to_pub, kind, board_key, body, ts) VALUES (?, ?, NULL, 'board', ?, ?, ?)",
		teamID, from, key, body, ts)
	return BoardNote{Key: key, Body: body, From: from, TS: ts}, err
}

// ReadBoard is the board as it stands: the latest note under each key,
// newest first. An empty body removes a key.
func ReadBoard(teamID string) ([]BoardNote, error) {
	h, err := db.Open()
	if err != nil {
		return nil, err
	}
	rows, err := h.Query(`SELECT `+messageColumns+` FROM team_messages m
		WHERE m.team_id = ? AND m.kind = 'board' AND m.id = (
			SELECT MAX(id) FROM team_messages x WHERE x.team_id = ? AND x.kind = 'board' AND x.board_key = m.board_key
		)
		ORDER BY m.id DESC`, teamID, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BoardNote
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(m.Body) != "" {
			out = append(out, BoardNote{Key: m.BoardKey, Body: m.Body, From: m.From, TS: m.TS})
		}
	}
	return out, rows.Err()
}

// CostShare is what one thread has cost.
type CostShare struct {
	USD          float64
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	// TeamUSD is, of USD, what turns the team itself started cost (ledger
	// source "team"); the rest came from the user typing into the thread.
	TeamUSD float64
}

// Cost is what the whole team has cost.
type Cost struct {
	CostShare
	Estimated bool
	BySession map[string]CostShare
}

// TeamCost sums every ledger row billed to the lead or any member, whoever
// started the turn. Summed when asked and never copied, so the daily and
// lifetime totals still count each dollar once.
func TeamCost(teamID string) (Cost, error) {
	out := Cost{BySession: map[string]CostShare{}}
	h, err := db.Open()
	if err != nil {
		return out, err
	}
	rows, err := h.Query(`SELECT l.session_pub,
			COALESCE(SUM(l.usd), 0), COALESCE(SUM(l.input_tokens), 0), COALESCE(SUM(l.output_tokens), 0),
			COALESCE(SUM(COALESCE(l.cache_read_tokens, 0)), 0),
			COALESCE(SUM(CASE WHEN l.source = 'team' THEN l.usd ELSE 0 END), 0),
			COALESCE(MAX(l.estimated), 0)
		FROM cost_ledger l
		WHERE l.session_pub IN (SELECT session_pub FROM team_members WHERE team_id = ?)
		GROUP BY l.session_pub`, teamID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var s CostShare
		var est int
		if err := rows.Scan(&id, &s.USD, &s.InputTokens, &s.OutputTokens, &s.CachedTokens, &s.TeamUSD, &est); err != nil {
			return out, err
		}
		out.BySession[id] = s
		out.USD += s.USD
		out.InputTokens += s.InputTokens
		out.OutputTokens += s.OutputTokens
		out.CachedTokens += s.CachedTokens
		out.TeamUSD += s.TeamUSD
		out.Estimated = out.Estimated || est == 1
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
