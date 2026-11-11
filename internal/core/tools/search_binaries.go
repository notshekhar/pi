package tools

import (
	"os"
	"os/exec"

	"github.com/notshekhar/pi/internal/core/home"
)

// loop downloads ripgrep and fd into ~/.loop/bin on first use, and pi shares
// that home, so the copies loop fetched are found here first. pi does not
// download them itself: find and grep fall back to walking the tree in Go,
// which works everywhere and only loses .gitignore handling.

// searchBinary finds an installed search tool by any of its names.
func searchBinary(names ...string) (string, bool) {
	for _, name := range names {
		if path := home.Path("bin", name); isExecutable(path) {
			return path, true
		}
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, true
		}
	}
	return "", false
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// skipDir is the set never worth walking when there is no .gitignore-aware
// tool to do the search.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "venv", "__pycache__",
		"dist", "build", "target", ".next", ".cache", ".idea":
		return true
	}
	return false
}
