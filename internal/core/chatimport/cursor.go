package chatimport

import "sort"

// Cursor, as one source: the CLI's chats and the IDE's side by side in one
// picker (each row says which), because to the user both are "my Cursor
// chats in this folder". loop's cursor.ts.

type cursorSource struct{ parts []Source }

// Cursor reads both Cursor CLI and Cursor IDE chats under home.
func Cursor(home string) Source {
	return cursorSource{parts: []Source{CursorCLI(home), CursorIDE(home)}}
}

func (cursorSource) Label() string { return "Cursor" }

func (c cursorSource) List(cwd string) []Summary {
	var out []Summary
	for _, p := range c.parts {
		out = append(out, p.List(cwd)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func (c cursorSource) Load(id, cwd string) (*Chat, error) {
	for _, p := range c.parts {
		if chat, err := p.Load(id, cwd); chat != nil || err != nil {
			return chat, err
		}
	}
	return nil, nil
}
