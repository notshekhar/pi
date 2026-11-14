package transcript

import "github.com/notshekhar/pi/internal/core/session"

// btwOf is a `/btw` question's tag as a live user-message event carries it —
// nil when unusable (loop's transcript/btw.ts). Saved entries read theirs
// with session.Entry.Btw, which applies the same rule.
func btwOf(meta any) *session.BtwMeta {
	m, ok := meta.(map[string]any)
	if !ok {
		return nil
	}
	parentID, okID := m["parentId"].(string)
	parentTitle, okTitle := m["parentTitle"].(string)
	if !okID || !okTitle {
		return nil
	}
	return &session.BtwMeta{ParentID: parentID, ParentTitle: parentTitle}
}

// entryBtw is a saved user entry's tag, nil when it has none.
func entryBtw(e session.Entry) *session.BtwMeta {
	if meta, ok := e.Btw(); ok {
		return &meta
	}
	return nil
}
