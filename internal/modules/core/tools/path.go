package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// Resolve joins a user path onto cwd. Absolute paths and ~/ are honoured.
func Resolve(path, cwd string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			if path == "~" {
				path = home
			} else if strings.HasPrefix(path, "~/") {
				path = filepath.Join(home, path[2:])
			}
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

// Inside reports whether path is cwd or a descendant of it.
func Inside(path, cwd string) bool {
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
