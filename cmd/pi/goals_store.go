package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/schedule"
	"github.com/notshekhar/pi/internal/core/session"
)

// Goals — loop's goals.ts over the shared agent.db `goals` table: stored
// background tasks tied to a directory. "none" runs on demand, "once" at a
// timestamp, "cron" on an expression stored verbatim. Each run records its
// session and outcome on the row, so loop's /background manager shows pi's
// runs and the other way round.
//
// It sits in cmd/pi beside its only caller because internal/core is being
// edited in parallel; it belongs in its own internal/core/goals package.

// maxGoals is loop's cap, which keeps the manager list and the tick scan small.
const maxGoals = 20

// staleRunAge is how long a "running" status is believed: past it, the tick
// that set it crashed and the goal is runnable again.
const staleRunAge = 2 * time.Hour

// goalSchedule is "none", "once" (at) or "cron" (expr).
type goalSchedule struct {
	kind string
	at   time.Time
	expr string
}

// goalRun is a goal's last run.
type goalRun struct {
	at        time.Time
	sessionID string
	status    string // running | ok | error
	summary   string
}

type goal struct {
	id, text, cwd, model, agent string
	goalSchedule
	enabled   bool
	createdAt time.Time
	lastRun   *goalRun
}

const goalColumns = `pub_id, text, cwd, model, agent, kind, at, expr, enabled, created_at,
	last_run_at, last_run_session_id, last_run_status, last_run_summary`

// listGoals is every goal in creation order.
func listGoals() ([]goal, error) {
	h, err := db.Open()
	if err != nil {
		return nil, err
	}
	rows, err := h.Query("SELECT " + goalColumns + " FROM goals ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []goal
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func scanGoal(rows *sql.Rows) (goal, error) {
	var (
		g                                           goal
		model, agent, expr, runSession, status, sum sql.NullString
		at, lastAt                                  sql.NullInt64
		enabled                                     int
		created                                     int64
	)
	err := rows.Scan(&g.id, &g.text, &g.cwd, &model, &agent, &g.kind, &at, &expr, &enabled, &created,
		&lastAt, &runSession, &status, &sum)
	if err != nil {
		return g, err
	}
	g.model, g.agent, g.expr = model.String, agent.String, expr.String
	g.enabled, g.createdAt = enabled == 1, time.UnixMilli(created)
	if at.Valid {
		g.at = time.UnixMilli(at.Int64)
	}
	if lastAt.Valid && status.String != "" {
		g.lastRun = &goalRun{at: time.UnixMilli(lastAt.Int64), sessionID: runSession.String, status: status.String, summary: sum.String}
	}
	return g, nil
}

var errGoalLimit = fmt.Errorf("goal limit reached (max %d)", maxGoals)

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// addGoal stores a new enabled goal.
func addGoal(text, cwd string, s goalSchedule, model, agent string) (goal, error) {
	existing, err := listGoals()
	if err != nil {
		return goal{}, err
	}
	if len(existing) >= maxGoals {
		return goal{}, errGoalLimit
	}
	g := goal{id: session.NewULID(), text: text, cwd: cwd, model: model, agent: agent, goalSchedule: s, enabled: true, createdAt: time.Now()}
	var at, expr any
	switch s.kind {
	case "once":
		at = s.at.UnixMilli()
	case "cron":
		expr = s.expr
	}
	h, err := db.Open()
	if err != nil {
		return goal{}, err
	}
	_, err = h.Exec(`INSERT INTO goals (pub_id, text, cwd, model, agent, kind, at, expr, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		g.id, text, cwd, nullable(model), nullable(agent), s.kind, at, expr, g.createdAt.UnixMilli())
	return g, err
}

// goalExec runs one statement against the goals table.
func goalExec(query string, args ...any) error {
	h, err := db.Open()
	if err != nil {
		return err
	}
	_, err = h.Exec(query, args...)
	return err
}

func deleteGoal(id string) error { return goalExec("DELETE FROM goals WHERE pub_id = ?", id) }

func disableGoal(id string) error {
	return goalExec("UPDATE goals SET enabled = 0 WHERE pub_id = ?", id)
}

// recordGoalRunStart stamps a run as started, so a concurrent tick skips it.
func recordGoalRunStart(id, sessionID string) error {
	return goalExec(`UPDATE goals SET last_run_at = ?, last_run_session_id = ?, last_run_status = 'running',
		last_run_summary = NULL WHERE pub_id = ?`, time.Now().UnixMilli(), sessionID, id)
}

func recordGoalRunEnd(id, status, summary string) error {
	return goalExec("UPDATE goals SET last_run_status = ?, last_run_summary = ? WHERE pub_id = ?", status, summary, id)
}

// findGoalByPrefix is the one goal whose id starts with prefix (ids are
// upper-case ULIDs, so a typed prefix is upper-cased too).
func findGoalByPrefix(prefix string) (goal, error) {
	goals, err := listGoals()
	if err != nil {
		return goal{}, err
	}
	var matches []goal
	for _, g := range goals {
		if g.id == prefix || strings.HasPrefix(g.id, strings.ToUpper(prefix)) {
			matches = append(matches, g)
		}
	}
	switch len(matches) {
	case 0:
		return goal{}, fmt.Errorf("no goal matching %q", prefix)
	case 1:
		return matches[0], nil
	}
	return goal{}, errAmbiguousGoal{prefix, len(matches)}
}

type errAmbiguousGoal struct {
	prefix string
	n      int
}

func (e errAmbiguousGoal) Error() string {
	return fmt.Sprintf("ambiguous id prefix %q (%d matches)", e.prefix, e.n)
}

// isGoalDue is loop's isGoalDue: "once" when its time has passed and it
// never ran; "cron" when the next firing after the last run (or creation)
// is past. A goal mid-run is skipped unless that run is stale.
func isGoalDue(g goal, now time.Time) bool {
	if !g.enabled || g.kind == "none" {
		return false
	}
	if g.lastRun != nil && g.lastRun.status == "running" && now.Sub(g.lastRun.at) < staleRunAge {
		return false
	}
	if g.kind == "once" {
		return !g.at.After(now) && (g.lastRun == nil || g.lastRun.status == "running")
	}
	cron, err := schedule.Parse(g.expr)
	if err != nil {
		return false
	}
	after := g.createdAt
	if g.lastRun != nil {
		after = g.lastRun.at
	}
	next := cron.Next(after)
	return !next.IsZero() && !next.After(now)
}

func dueGoals(goals []goal, now time.Time) []goal {
	var out []goal
	for _, g := range goals {
		if isGoalDue(g, now) {
			out = append(out, g)
		}
	}
	return out
}

// isAmbiguous reports a prefix that matched more than one goal.
func isAmbiguous(err error) bool {
	var amb errAmbiguousGoal
	return errors.As(err, &amb)
}
