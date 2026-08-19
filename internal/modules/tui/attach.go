package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File attachments dropped or pasted into the composer.
//
// Terminals do not hand a program an image. What they hand it is a PATH — a
// drag-and-drop writes the file's path into stdin, and a paste of an image
// gives nothing at all on most terminals. So attaching is path-based: the
// composer notices a path to a readable file and records it, and the turn
// sends its bytes.
//
// Paths arrive shell-quoted, which is the part that bites: a dropped file
// called `My Photo.png` arrives as `My\ Photo.png` or `'My Photo.png'`, and a
// naive reader gets a filename that does not exist.

// Attachment is a file to send with the next prompt.
type Attachment struct {
	Path      string
	MediaType string
	Data      []byte
}

// imageTypes maps an extension to its media type.
var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
	".svg": "image/svg+xml",
}

// maxAttachment caps a single file. Beyond this the request is the problem,
// not the image.
const maxAttachment = 8 << 20

// DetectAttachment reads a dropped or pasted line as a file path, returning
// the attachment when it names a readable file.
func DetectAttachment(text string) (Attachment, bool) {
	path := UnquotePath(text)
	if path == "" {
		return Attachment{}, false
	}
	media, ok := imageTypes[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return Attachment{}, false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return Attachment{}, false
	}
	if info.Size() > maxAttachment {
		return Attachment{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Attachment{}, false
	}
	return Attachment{Path: path, MediaType: media, Data: data}, true
}

// UnquotePath undoes the quoting a terminal applies to a dropped path.
//
// Three forms in the wild: backslash-escaped spaces (most terminals), single
// quotes (GNOME), and a file:// URL (some browsers and file managers).
func UnquotePath(text string) string {
	s := strings.TrimSpace(text)
	if s == "" {
		return ""
	}
	if after, ok := strings.CutPrefix(s, "file://"); ok {
		s = decodePercent(after)
	}
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	// Unescape `\ ` and friends only when there is no unescaped whitespace
	// left over — otherwise this is prose that happens to contain a slash.
	if strings.Contains(s, "\\ ") {
		s = strings.ReplaceAll(s, "\\ ", " ")
	}
	if strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			s = filepath.Join(home, s[2:])
		}
	}
	return s
}

// decodePercent undoes %XX escaping in a file:// URL.
func decodePercent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			var v int
			if _, err := fmt.Sscanf(s[i+1:i+3], "%02x", &v); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// DataURL renders the attachment for a provider that wants one.
func (a Attachment) DataURL() string {
	return "data:" + a.MediaType + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
}

// Label is how the attachment reads in the composer.
func (a Attachment) Label() string {
	return fmt.Sprintf("%s (%s, %s)", filepath.Base(a.Path), a.MediaType, humanSize(len(a.Data)))
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fkB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}
