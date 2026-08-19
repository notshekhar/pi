package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnquotePath(t *testing.T) {
	cases := []struct{ in, want string }{
		// The forms terminals actually deliver on a drag-and-drop.
		{`/tmp/a.png`, "/tmp/a.png"},
		{`/tmp/My\ Photo.png`, "/tmp/My Photo.png"},
		{`'/tmp/My Photo.png'`, "/tmp/My Photo.png"},
		{`"/tmp/My Photo.png"`, "/tmp/My Photo.png"},
		{`file:///tmp/My%20Photo.png`, "/tmp/My Photo.png"},
		{`  /tmp/a.png  `, "/tmp/a.png"},
		{``, ""},
	}
	for _, c := range cases {
		if got := UnquotePath(c.in); got != c.want {
			t.Errorf("UnquotePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDetectAttachment(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(img, []byte("\x89PNG fake"), 0o600); err != nil {
		t.Fatal(err)
	}

	att, ok := DetectAttachment(img)
	if !ok {
		t.Fatal("a real png was not detected")
	}
	if att.MediaType != "image/png" || len(att.Data) == 0 {
		t.Errorf("attachment = %+v", att)
	}
	// A quoted path with a space is the case that bites.
	spaced := filepath.Join(dir, "my shot.png")
	if err := os.WriteFile(spaced, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := DetectAttachment(`'` + spaced + `'`); !ok {
		t.Error("a quoted path with a space was not detected")
	}
}

func TestDetectAttachmentRejectsNonFiles(t *testing.T) {
	dir := t.TempDir()
	for _, in := range []string{
		"just some prose",
		filepath.Join(dir, "missing.png"),
		dir,                             // a directory
		filepath.Join(dir, "notes.txt"), // not an image
	} {
		if _, ok := DetectAttachment(in); ok {
			t.Errorf("%q should not attach", in)
		}
	}
}

func TestAttachmentDataURL(t *testing.T) {
	a := Attachment{MediaType: "image/png", Data: []byte("abc")}
	if got := a.DataURL(); got != "data:image/png;base64,YWJj" {
		t.Errorf("DataURL = %q", got)
	}
}
