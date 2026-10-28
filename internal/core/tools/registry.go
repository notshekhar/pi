package tools

import (
	"fmt"
	"os"
	"sync"
)

// lineRange is an inclusive 1-indexed span the agent has seen.
type lineRange struct{ start, end int }

type readRecord struct {
	mtime   int64
	covered []lineRange
	sawEOF  bool
}

// Registry enforces read-before-edit. It is session-scoped and in-memory.
type Registry struct {
	mu    sync.Mutex
	files map[string]readRecord
}

// NewRegistry returns an empty slate.
func NewRegistry() *Registry {
	return &Registry{files: map[string]readRecord{}}
}

// RecordRead notes that the agent saw a file. A missing range means the
// whole file. mtime must be sampled before the content is read.
func (r *Registry) RecordRead(path string, mtime int64, start, end int, sawEOF bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev, ok := r.files[path]
	if !ok || prev.mtime != mtime {
		prev = readRecord{mtime: mtime}
	}
	if start > 0 && end >= start {
		prev.covered = mergeRange(prev.covered, lineRange{start, end})
	} else {
		prev.covered = mergeRange(prev.covered, lineRange{1, 1 << 30})
		sawEOF = true
	}
	prev.sawEOF = prev.sawEOF || sawEOF
	prev.mtime = mtime
	r.files[path] = prev
}

// RecordModified marks a file as fully known after a successful write/edit.
func (r *Registry) RecordModified(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[path] = readRecord{
		mtime:   info.ModTime().UnixNano(),
		covered: []lineRange{{1, 1 << 30}},
		sawEOF:  true,
	}
}

// CheckEdit returns an error message when the file must not be edited.
func (r *Registry) CheckEdit(path, display string) string {
	return r.check(path, display, "edit", "editing", false)
}

// CheckWrite returns an error message when an existing file must not be
// overwritten. New files pass.
func (r *Registry) CheckWrite(path, display string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return ""
	}
	return r.check(path, display, "write", "writing", true)
}

func (r *Registry) check(path, display, verb, gerund string, requireComplete bool) string {
	r.mu.Lock()
	rec, ok := r.files[path]
	r.mu.Unlock()
	if !ok {
		return fmt.Sprintf("File has not been read in this session: %s. Read it with the read tool first, then %s.", display, verb)
	}
	info, err := os.Stat(path)
	if err == nil && info.ModTime().UnixNano() > rec.mtime {
		return fmt.Sprintf("File changed on disk since it was read: %s. Read it again before %s.", display, gerund)
	}
	if requireComplete && !isComplete(rec) {
		return fmt.Sprintf("Only part of %s has been read. Overwriting it wholesale needs the whole file: re-read it (continue with offset= until the end), or use edit for a targeted change.", display)
	}
	return ""
}

func isComplete(r readRecord) bool {
	return r.sawEOF && len(r.covered) == 1 && r.covered[0].start == 1
}

func mergeRange(covered []lineRange, add lineRange) []lineRange {
	all := append(append([]lineRange{}, covered...), add)
	// insertion sort by start
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].start < all[j-1].start; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	out := make([]lineRange, 0, len(all))
	for _, r := range all {
		if len(out) == 0 {
			out = append(out, r)
			continue
		}
		last := &out[len(out)-1]
		if r.start <= last.end+1 {
			if r.end > last.end {
				last.end = r.end
			}
			continue
		}
		out = append(out, r)
	}
	return out
}
