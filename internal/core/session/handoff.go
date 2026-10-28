package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
)

// u16 slices s by UTF-16 code units, as JavaScript's slice does.
func u16(s string, start, end int) string {
	units := utf16.Encode([]rune(s))
	if end < 0 || end > len(units) {
		end = len(units)
	}
	if start < 0 {
		start = max(0, len(units)+start)
	}
	if start >= end {
		return ""
	}
	return string(utf16.Decode(units[start:end]))
}

func u16len(s string) int { return len(utf16.Encode([]rune(s))) }

// ConversationExcerpt is loop's conversationExcerpt (agent/
// conversation-excerpt.ts): the branch as model-readable text for /handoff
// and /recipe — the opening and the latest outcome, tool-heavy entries capped.
func ConversationExcerpt(entries []Entry) string {
	var parts []string
	capped := func(s string) string { return u16(s, 0, 8000) }
	for _, e := range entries {
		switch e.Type {
		case TypeMessage:
			var content string
			if json.Unmarshal(e.Content, &content) != nil {
				content = string(e.Content)
			}
			if u16len(content) > 8000 {
				content = u16(content, 0, 4000) + "\n[entry excerpted]\n" + u16(content, -4000, -1)
			}
			parts = append(parts, "["+e.Role+"]\n"+content)
		case TypeCompact:
			text := e.Handoff
			if text == "" {
				text = e.Summary
			}
			parts = append(parts, capped("[earlier context]\n"+text))
		case TypeBranchSummary:
			parts = append(parts, capped("[branch summary]\n"+e.Summary))
		case TypeSubagent:
			parts = append(parts, capped("[subagent result]\n"+e.Result))
		}
	}
	text := strings.Join(parts, "\n\n")
	if u16len(text) <= 60000 {
		return text
	}
	return u16(text, 0, 12000) + "\n\n[middle of conversation omitted]\n\n" + u16(text, -47000, -1)
}

// CreateHandoff is loop's SessionManager.createHandoff: a fresh session
// holding the reviewed brief, linked to its source, written atomically.
func CreateHandoff(source *Session, provider, model, cwd, brief string) (*Session, error) {
	if strings.TrimSpace(brief) == "" {
		return nil, fmt.Errorf("A handoff brief cannot be empty.")
	}
	now := time.Now().UnixMilli()
	info := Info{ID: NewULID(), CreatedAt: now, CWD: cwd, Provider: provider, Model: model, ParentSession: source.Path}
	label := source.Name()
	if label == "" {
		label = u16(source.ID, 0, 12)
	}
	name := u16("Handoff: "+label, 0, 100)
	messageID := newEntryID(func(id string) bool { return id == info.ID })
	nameID := newEntryID(func(id string) bool { return id == info.ID || id == messageID })
	content, _ := json.Marshal(brief)
	entries := []Entry{
		infoEntry(info),
		{Type: TypeMessage, TS: now, Role: "user", Content: content, ID: messageID, ParentID: info.ID},
		{Type: TypeSessionName, TS: now, Name: &name, ID: nameID, ParentID: messageID},
	}
	rowID, err := insertSessionWithEntries(info, entries)
	if err != nil {
		return nil, err
	}
	s := newSession(info, entries)
	s.rowID, s.seenOnDisk = rowID, len(entries)
	return s, nil
}
