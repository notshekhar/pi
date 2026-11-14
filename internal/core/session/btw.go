package session

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"path/filepath"
	"strings"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
)

// `/btw` — ask a side question without touching the conversation you are in
// (loop's sessions/btw.ts).
//
// The question runs in a FRESH session: nothing of the conversation it was
// asked from is copied, and the model is told nothing about it — it sees the
// question alone, as the first message of a new thread. Nothing flows back
// either, so the main turn can keep working while you ask.
//
// The one link kept is for the UI, never the model: the question's entry
// carries a `btw` tag (where it was asked) and the session records its
// parent, so every client can show "Side question from …" and a way back.

// btwNameChars is how much of the question a side thread's name keeps, and
// how much of a first prompt a source's title keeps — loop's NAME_CHARS.
const btwNameChars = 60

// btwNamePrefix starts every side thread's name; it is what marks one in a
// session list.
const btwNamePrefix = "btw: "

// btwKey is the entry key a side question's user message carries.
const btwKey = "btw"

// BtwMeta is where a side question was asked from — loop's BtwMeta, riding
// on the question's user entry under `btw`.
type BtwMeta struct {
	// ParentID is the session id of the conversation it was asked in.
	ParentID string `json:"parentId"`
	// ParentTitle is that conversation's title when it was asked.
	ParentTitle string `json:"parentTitle"`
}

// BtwThreadName is the name a side thread is listed under.
func BtwThreadName(question string) string {
	flat := strings.Join(strings.Fields(question), " ")
	if u16len(flat) > btwNameChars {
		flat = u16(flat, 0, btwNameChars-1) + "…"
	}
	return btwNamePrefix + flat
}

// Btw is the side-question tag a user message carries, if it is a usable
// one (loop's btwOf).
func (e Entry) Btw() (BtwMeta, bool) {
	raw, ok := e.Extra[btwKey]
	if !ok {
		return BtwMeta{}, false
	}
	var fields map[string]jsontext.Value
	if json.Unmarshal(raw, &fields, jsonopt.Read) != nil {
		return BtwMeta{}, false
	}
	var meta BtwMeta
	if json.Unmarshal(fields["parentId"], &meta.ParentID, jsonopt.Read) != nil ||
		json.Unmarshal(fields["parentTitle"], &meta.ParentTitle, jsonopt.Read) != nil {
		return BtwMeta{}, false
	}
	return meta, true
}

// SetBtw tags the entry as the question that opened a side thread.
func (e *Entry) SetBtw(meta BtwMeta) {
	if e.Extra == nil {
		e.Extra = map[string]jsontext.Value{}
	}
	e.Extra[btwKey] = jsontext.Value(mustJSON(meta))
}

// BtwParentIn is the session a side thread was asked from, read off its
// question's tag.
func BtwParentIn(branch []Entry) (BtwMeta, bool) {
	for _, e := range branch {
		if e.Type == TypeMessage && e.Role == "user" {
			if meta, ok := e.Btw(); ok {
				return meta, true
			}
		}
	}
	return BtwMeta{}, false
}

// BtwParentOf is the session a side thread was asked from, read off a
// session-list row's name and parent link — so a list can say so without
// opening every session. A forked or handed-off session has a parent too;
// the name is what marks a side thread, so one renamed away from `btw:`
// reads as a plain fork.
func BtwParentOf(name, parentSession string) (string, bool) {
	if !strings.HasPrefix(name, btwNamePrefix) || parentSession == "" {
		return "", false
	}
	return strings.TrimSuffix(filepath.Base(parentSession), ".jsonl"), true
}

// btwSourceTitle is the title a source conversation goes by in a side
// thread's tag: its name, else its first prompt's first line, else a phrase.
func btwSourceTitle(name string, branch []Entry) string {
	if named := strings.TrimSpace(name); named != "" {
		return named
	}
	for _, e := range branch {
		if e.Type != TypeMessage || e.Role != "user" {
			continue
		}
		if text := strings.TrimSpace(TextContent(e.Content)); text != "" {
			first, _, _ := strings.Cut(text, "\n")
			return u16(first, 0, btwNameChars)
		}
		break
	}
	return "this conversation"
}

// ErrEmptyBtw refuses a side question with nothing in it.
var ErrEmptyBtw = errors.New("A side question cannot be empty.")

// CreateBtwThread is a new, empty session for question in the source's
// folder and on its model, named after the question and linked to the
// source. The question itself is NOT added — run it as the new session's
// first turn with the returned meta, so it streams like any other turn.
//
// It read-locks source itself, so a turn appending to it meanwhile is safe;
// the caller must not already hold source's lock.
func CreateBtwThread(source *Session, question string) (*Session, BtwMeta, error) {
	if strings.TrimSpace(question) == "" {
		return nil, BtwMeta{}, ErrEmptyBtw
	}
	source.RLock()
	info := Info{CWD: source.Info.CWD, Provider: source.Info.Provider, Model: source.LastModel(), ParentSession: source.Path}
	meta := BtwMeta{ParentID: source.ID, ParentTitle: btwSourceTitle(source.Name(), source.CurrentBranch())}
	source.RUnlock()
	thread, err := createInfo(info)
	if err != nil {
		return nil, BtwMeta{}, err
	}
	if err := thread.SetName(BtwThreadName(question)); err != nil {
		return nil, BtwMeta{}, err
	}
	return thread, meta, nil
}
