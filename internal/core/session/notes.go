package session

import (
	"time"

	"github.com/notshekhar/pi/internal/core/db"
)

// Notes a session keeps for later — loop's sessions/notes.ts, same table.
// Every note belongs to exactly one session; there are no global notes. A
// note is either submitted (sent as the next message, which takes it off the
// list) or deleted.

// Note is one saved note.
type Note struct {
	ID        string
	Text      string
	CreatedAt time.Time
}

// ListNotes is the session's notes, oldest first.
func ListNotes(sessionID string) ([]Note, error) {
	h, err := db.Open()
	if err != nil {
		return nil, err
	}
	rows, err := h.Query("SELECT pub_id, text, created_at FROM session_notes WHERE session_pub = ? ORDER BY id", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		var n Note
		var ms int64
		if err := rows.Scan(&n.ID, &n.Text, &ms); err != nil {
			return nil, err
		}
		n.CreatedAt = time.UnixMilli(ms)
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

// AddNote saves a note in the session.
func AddNote(sessionID, text string) (Note, error) {
	h, err := db.Open()
	if err != nil {
		return Note{}, err
	}
	n := Note{ID: NewULID(), Text: text, CreatedAt: time.Now()}
	_, err = h.Exec("INSERT INTO session_notes (pub_id, session_pub, text, created_at) VALUES (?, ?, ?, ?)",
		n.ID, sessionID, n.Text, n.CreatedAt.UnixMilli())
	return n, err
}

// TakeNote takes a note off the session's list and returns it; ok is false
// when it is already gone. Submitting uses this, so a note is sent at most
// once even if two windows on the same session pick it together.
func TakeNote(sessionID, noteID string) (n Note, ok bool, err error) {
	h, err := db.Open()
	if err != nil {
		return Note{}, false, err
	}
	rows, err := h.Query("DELETE FROM session_notes WHERE session_pub = ? AND pub_id = ? RETURNING pub_id, text, created_at", sessionID, noteID)
	if err != nil {
		return Note{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Note{}, false, rows.Err()
	}
	var ms int64
	if err := rows.Scan(&n.ID, &n.Text, &ms); err != nil {
		return Note{}, false, err
	}
	n.CreatedAt = time.UnixMilli(ms)
	return n, true, nil
}

// ClearNotes removes every note in the session and reports how many there were.
func ClearNotes(sessionID string) (int, error) {
	h, err := db.Open()
	if err != nil {
		return 0, err
	}
	res, err := h.Exec("DELETE FROM session_notes WHERE session_pub = ?", sessionID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
