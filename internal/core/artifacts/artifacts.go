// Package artifacts is loop's artifacts (artifacts/index.ts): standalone
// pages the agent writes under ~/.loop/artifacts/<id>/, each folder holding
// its content file and a meta.json. The directory is the registry.
package artifacts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/home"
)

// Kinds and their content files.
var fileForKind = map[string]string{
	"html": "index.html", "markdown": "index.md", "svg": "index.svg",
	"json": "index.json", "csv": "index.csv", "text": "index.txt",
}

// Kinds is every kind.
var Kinds = []string{"html", "markdown", "svg", "json", "csv", "text"}

// Executes reports kinds whose content can run code.
var Executes = map[string]bool{"html": true, "svg": true}

// MaxBytes is the largest artifact the write path accepts.
const MaxBytes = 5 * 1024 * 1024

// Record is meta.json.
type Record struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Favicon     string `json:"favicon,omitempty"`
	Kind        string `json:"kind"`
	File        string `json:"file"`
	CreatedAt   int64  `json:"createdAt"`
	SessionID   string `json:"sessionId,omitempty"`
}

// Meta is a record plus facts read off the content file.
type Meta struct {
	Record
	UpdatedAt float64 `json:"updatedAt"`
	Size      int64   `json:"size"`
	Written   bool    `json:"written"`
}

var dirOverride string

// Dir is ~/.loop/artifacts.
func Dir() string {
	if dirOverride != "" {
		return dirOverride
	}
	return home.Path("artifacts")
}

// SetDirForTests points the store elsewhere ("" restores it).
func SetDirForTests(dir string) { dirOverride = dir }

var idRE = regexp.MustCompile(`^[a-f0-9]{12}$`)

// IsValidID reports a well-formed id.
func IsValidID(id string) bool { return idRE.MatchString(id) }

func dirOf(id string) (string, error) {
	if !IsValidID(id) {
		return "", fmt.Errorf("Invalid artifact id: %s", id)
	}
	return filepath.Join(Dir(), id), nil
}

// FilePath is the content file's absolute path.
func FilePath(r Record) string { return filepath.Join(Dir(), r.ID, r.File) }

func hydrate(r Record) Meta {
	m := Meta{Record: r, UpdatedAt: float64(r.CreatedAt)}
	if st, err := os.Stat(FilePath(r)); err == nil {
		// Node's mtimeMs: seconds·1e3 + nanoseconds/1e6.
		t := st.ModTime()
		m.UpdatedAt = float64(float64(t.Unix())*1e3) + float64(t.Nanosecond())/1e6 // no FMA
		m.Size, m.Written = st.Size(), true
	}
	return m
}

func readRecord(dir string) (Record, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return Record{}, false
	}
	var r Record
	if json.Unmarshal(raw, &r) != nil || r.ID != filepath.Base(dir) || r.File == "" {
		return Record{}, false
	}
	return r, true
}

func writeRecord(r Record) error {
	dir, err := dirOf(r.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(filepath.Join(dir, "meta.json"), append(raw, '\n'), 0o644)
}

// List is every artifact, most recently modified first.
func List() []Meta {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return []Meta{}
	}
	out := []Meta{}
	for _, e := range entries {
		if !IsValidID(e.Name()) {
			continue
		}
		if r, ok := readRecord(filepath.Join(Dir(), e.Name())); ok {
			out = append(out, hydrate(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get is one artifact.
func Get(id string) (Meta, bool) {
	if !IsValidID(id) {
		return Meta{}, false
	}
	r, ok := readRecord(filepath.Join(Dir(), id))
	if !ok {
		return Meta{}, false
	}
	return hydrate(r), true
}

// Read is the content written so far.
func Read(id string) (string, error) {
	m, ok := Get(id)
	if !ok {
		return "", fmt.Errorf("No such artifact: %s", id)
	}
	if !m.Written {
		return "", fmt.Errorf("Artifact %s has no content yet", id)
	}
	raw, err := os.ReadFile(FilePath(m.Record))
	return string(raw), err
}

// Delete removes an artifact.
func Delete(id string) bool {
	if !IsValidID(id) {
		return false
	}
	dir := filepath.Join(Dir(), id)
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	return os.RemoveAll(dir) == nil
}

// CreateInput is loop's CreateArtifactInput.
type CreateInput struct {
	Title, Kind, Description, Favicon, SessionID string
}

// Create reserves an artifact (meta only) and returns where to write it.
func Create(in CreateInput) (Meta, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Meta{}, errors.New("An artifact needs a title")
	}
	file, ok := fileForKind[in.Kind]
	if !ok {
		return Meta{}, fmt.Errorf("Unknown artifact kind: %s", in.Kind)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	r := Record{ID: hex.EncodeToString(b[:]), Title: title, Description: strings.TrimSpace(in.Description),
		Favicon: strings.TrimSpace(in.Favicon), Kind: in.Kind, File: file, CreatedAt: time.Now().UnixMilli(), SessionID: in.SessionID}
	if err := writeRecord(r); err != nil {
		return Meta{}, err
	}
	return hydrate(r), nil
}

// UpdateMeta changes title, description or favicon (nil leaves a field).
func UpdateMeta(id string, title, description, favicon *string) (Meta, error) {
	m, ok := Get(id)
	if !ok {
		return Meta{}, fmt.Errorf("No such artifact: %s", id)
	}
	r := m.Record
	if title != nil {
		t := strings.TrimSpace(*title)
		if t == "" {
			return Meta{}, errors.New("An artifact needs a title")
		}
		r.Title = t
	}
	if description != nil {
		r.Description = strings.TrimSpace(*description)
	}
	if favicon != nil {
		r.Favicon = strings.TrimSpace(*favicon)
	}
	if err := writeRecord(r); err != nil {
		return Meta{}, err
	}
	return hydrate(r), nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// FileName is the human filename an export uses.
func FileName(r Record) string {
	ext := strings.TrimPrefix(fileForKind[r.Kind], "index")
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(r.Title), "-"), "-")
	if len(slug) > 60 {
		slug = slug[:60]
	}
	if slug == "" {
		slug = r.ID
	}
	return slug + ext
}

// Export copies an artifact into dest (~/Downloads by default), never
// overwriting, and returns where it landed.
func Export(id, dest string) (string, error) {
	m, ok := Get(id)
	if !ok {
		return "", fmt.Errorf("No such artifact: %s", id)
	}
	if !m.Written {
		return "", fmt.Errorf("Artifact %s has no content yet", id)
	}
	if dest == "" {
		h, _ := os.UserHomeDir()
		dest = filepath.Join(h, "Downloads")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	name := FileName(m.Record)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	target := filepath.Join(dest, name)
	for n := 2; ; n++ {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		if n > 1000 {
			return "", fmt.Errorf("Could not find a free filename in %s", dest)
		}
		target = filepath.Join(dest, fmt.Sprintf("%s-%d%s", stem, n, ext))
	}
	src, err := os.Open(FilePath(m.Record))
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := os.Create(target)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return target, err
}
