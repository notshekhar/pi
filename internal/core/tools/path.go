package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Path resolution, loop's tools/utils/path-utils.ts.

// unicodeSpaces are the space look-alikes a pasted path can carry.
var unicodeSpaces = regexp.MustCompile(`[\x{00A0}\x{2000}-\x{200A}\x{202F}\x{205F}\x{3000}]`)

const narrowNoBreakSpace = " "

// expandPath drops a leading @ (how a path is typed in the composer), folds
// unicode spaces to plain ones, and expands ~.
func expandPath(path string) string {
	path = unicodeSpaces.ReplaceAllString(strings.TrimPrefix(path, "@"), " ")
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
	}
	return path
}

// Resolve joins a user path onto cwd. Absolute paths and ~/ are honoured.
func Resolve(path, cwd string) string {
	if path == "" {
		path = "."
	}
	path = expandPath(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

var amPmSuffix = regexp.MustCompile(`(?i) (AM|PM)\.`)

// ResolveRead resolves a path to read, trying the spellings macOS gives
// screenshot names when the typed one does not exist: a narrow no-break space
// before AM/PM, and a curly apostrophe.
//
// loop also tries the NFD form of the name. Go's standard library has no
// unicode normalisation and pi takes no dependency for it, so that one
// variant is not tried here.
func ResolveRead(path, cwd string) string {
	resolved := Resolve(path, cwd)
	if exists(resolved) {
		return resolved
	}
	amPm := amPmSuffix.ReplaceAllString(resolved, narrowNoBreakSpace+"$1.")
	curly := strings.ReplaceAll(resolved, "'", "’")
	for _, variant := range []string{amPm, curly} {
		if variant != resolved && exists(variant) {
			return variant
		}
	}
	return resolved
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// Inside reports whether path is cwd or a descendant of it.
func Inside(path, cwd string) bool {
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
