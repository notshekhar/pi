package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/home"
)

// Session list rows and project folders as loop's SessionManager returns
// them (sessions/manager.ts toSessionInfo, sqlite-store.ts listSessions /
// listSessionFolders) — the JSON every client's session list reads.

// Row is loop's SessionInfo.
type Row struct {
	ID               string  `json:"id"`
	CreatedAt        float64 `json:"createdAt"`
	CWD              string  `json:"cwd"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	ParentSession    string  `json:"parentSession,omitempty"`
	Path             string  `json:"path"`
	Mtime            float64 `json:"mtime"`
	FirstUserMessage *string `json:"firstUserMessage,omitempty"`
	Name             *string `json:"name,omitempty"`
	LastModel        string  `json:"lastModel,omitempty"`
	ArchivedAt       int64   `json:"archivedAt,omitempty"`
}

// Page bounds a listing; a negative Limit means none.
type Page struct {
	Limit  int
	Offset int
	// IDs restricts the listing to these sessions; nil means all.
	IDs []string
}

var validID = regexp.MustCompile(`^[0-9A-Za-z_-]{1,64}$`)

// rowFromRecord is loop's toSessionInfo.
func rowFromRecord(r Record) Row {
	row := Row{ID: r.Info.ID, CreatedAt: r.createdRaw, CWD: r.Info.CWD, Provider: r.Info.Provider,
		Model: r.Info.Model, ParentSession: r.Info.ParentSession, Path: home.TranscriptPath(r.Info.CWD, r.Info.ID),
		Mtime: r.updatedRaw, LastModel: r.LastModel, ArchivedAt: r.ArchivedAt}
	if r.Name != "" {
		name := r.Name
		row.Name = &name
	}
	if r.FirstUserPayload != "" {
		var m struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal([]byte(r.FirstUserPayload), &m) == nil {
			var text string
			if json.Unmarshal(m.Content, &text) == nil {
				text = StripHookContext(text)
			} else {
				text = string(m.Content)
				if len(text) > 120 {
					text = text[:120]
				}
			}
			row.FirstUserMessage = &text
		}
	}
	return row
}

// Rows lists sessions newest first. hasCwd distinguishes "this folder" from
// "every folder" (an empty cwd is a real filter in loop).
func Rows(cwd string, hasCwd bool, scope Scope, page Page) ([]Row, error) {
	h, err := db.Open()
	if err != nil {
		return nil, err
	}
	var where []string
	var args []any
	if hasCwd {
		where = append(where, "s.cwd = ?")
		args = append(args, cwd)
	}
	if page.IDs != nil {
		var ok []string
		for _, id := range page.IDs {
			if validID.MatchString(id) {
				ok = append(ok, "'"+id+"'")
			}
		}
		if len(ok) == 0 {
			where = append(where, "0")
		} else {
			where = append(where, "s.pub_id IN ("+strings.Join(ok, ",")+")")
		}
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
	switch {
	case page.Limit >= 0:
		q += fmt.Sprintf(" LIMIT %d", page.Limit)
		if page.Offset > 0 {
			q += fmt.Sprintf(" OFFSET %d", page.Offset)
		}
	case page.Offset > 0:
		q += fmt.Sprintf(" LIMIT -1 OFFSET %d", page.Offset)
	}
	rows, err := h.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var first, last sql.NullString
		r, err := scanRecord(rows, &first, &last)
		if err != nil {
			return nil, err
		}
		r.FirstUserPayload, r.LastModel = first.String, last.String
		out = append(out, rowFromRecord(r))
	}
	return out, rows.Err()
}

// Folder is loop's SessionFolder.
type Folder struct {
	CWD       string  `json:"cwd"`
	Count     int     `json:"count"`
	CreatedAt float64 `json:"createdAt"`
	UpdatedAt float64 `json:"updatedAt"`
}

// Folders is every folder with a session in scope, newest first.
func Folders(scope Scope) ([]Folder, error) {
	h, err := db.Open()
	if err != nil {
		return nil, err
	}
	where := ""
	switch scope {
	case Archived:
		where = "WHERE archived_at IS NOT NULL"
	case Active:
		where = "WHERE archived_at IS NULL"
	}
	rows, err := h.Query(`SELECT cwd, COUNT(*) AS count, MIN(created_at) AS createdAt, MAX(updated_at) AS updatedAt
		FROM sessions ` + where + ` GROUP BY cwd ORDER BY updatedAt DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Folder{}
	for rows.Next() {
		var f Folder
		var created, updated sql.NullFloat64
		if err := rows.Scan(&f.CWD, &f.Count, &created, &updated); err != nil {
			return nil, err
		}
		f.CreatedAt, f.UpdatedAt = created.Float64, updated.Float64
		out = append(out, f)
	}
	return out, rows.Err()
}

// CreateWith is loop's SessionManager.create: provider and model given
// separately (a session may start before a model is chosen), written at once.
func CreateWith(provider, model, cwd string) (*Session, error) {
	info := Info{ID: NewULID(), CreatedAt: time.Now().UnixMilli(), CWD: cwd, Provider: provider, Model: model}
	s := newSession(info, nil)
	return s, s.ensureStarted()
}
