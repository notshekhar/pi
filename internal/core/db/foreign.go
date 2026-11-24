package db

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
)

// OpenForeign opens ANOTHER program's SQLite database for reading without
// leaving a trace — loop's withForeignSqlite (chat-import/files.ts).
//
// A database in WAL mode cannot be opened read-only cleanly: SQLite creates a
// `-shm` file beside it (a write into the other program's folder), and fails
// outright when it cannot — which is how Cursor leaves most of its chat
// stores. So a WAL database is read through a copy of itself and its WAL (the
// WAL holds the newest rows); any other database opens read-only in place.
// close releases the handle and the copy.
func OpenForeign(path string) (h *sql.DB, close func(), err error) {
	if !isWAL(path) {
		h, err = sql.Open("sqlite3", "file:"+path+"?mode=ro")
		if err != nil {
			return nil, nil, err
		}
		return pinged(h, func() { h.Close() })
	}
	dir, err := os.MkdirTemp("", "pi-import-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	copyPath := filepath.Join(dir, "chat.db")
	if err := copyFile(path, copyPath); err != nil {
		cleanup()
		return nil, nil, err
	}
	if _, statErr := os.Stat(path + "-wal"); statErr == nil {
		if err := copyFile(path+"-wal", copyPath+"-wal"); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	h, err = sql.Open("sqlite3", "file:"+copyPath)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return pinged(h, func() { h.Close(); cleanup() })
}

// pinged checks the handle really opened (sql.Open is lazy), releasing it if not.
func pinged(h *sql.DB, close func()) (*sql.DB, func(), error) {
	if err := h.Ping(); err != nil {
		close()
		return nil, nil, err
	}
	return h, close, nil
}

// isWAL reads bytes 18 and 19 of the SQLite header, which are 2 in WAL mode.
func isWAL(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, 20)
	if _, err := io.ReadFull(f, header); err != nil {
		return false
	}
	return header[18] == 2 && header[19] == 2
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
