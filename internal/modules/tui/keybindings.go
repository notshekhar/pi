package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Configurable keybindings.
//
// Rebinding is a small feature with a large failure mode: a binding file that
// captures `enter` or `ctrl+c` can leave a terminal app with no way to submit
// or to quit, and the only recourse is editing the file from another window.
// So the ESSENTIAL keys cannot be rebound, and everything else can.

// Action is something a key can be bound to.
type Action string

const (
	ActionUndo       Action = "undo"
	ActionRedo       Action = "redo"
	ActionNav        Action = "transcript"
	ActionLineStart  Action = "lineStart"
	ActionLineEnd    Action = "lineEnd"
	ActionKillLine   Action = "killLine"
	ActionKillWord   Action = "killWord"
	ActionDeleteLine Action = "deleteLine"
)

// defaultBindings are the shipped keys.
var defaultBindings = map[Action]KeyKind{
	ActionUndo:       KeyCtrlZ,
	ActionRedo:       KeyCtrlY,
	ActionNav:        KeyCtrlE,
	ActionLineStart:  KeyCtrlA,
	ActionLineEnd:    KeyCtrlE,
	ActionKillLine:   KeyCtrlK,
	ActionKillWord:   KeyCtrlW,
	ActionDeleteLine: KeyCtrlU,
}

// bindableKeys are the key names a file may name. Deliberately excludes
// enter, escape, ctrl+c and ctrl+d: binding those away is how a user locks
// themselves out of their own session.
var bindableKeys = map[string]KeyKind{
	"ctrl+a": KeyCtrlA,
	"ctrl+e": KeyCtrlE,
	"ctrl+k": KeyCtrlK,
	"ctrl+u": KeyCtrlU,
	"ctrl+w": KeyCtrlW,
	"ctrl+y": KeyCtrlY,
	"ctrl+z": KeyCtrlZ,
}

// Bindings maps actions to keys.
type Bindings map[Action]KeyKind

// activeBindings is the resolved table, loaded once.
var activeBindings = loadBindings()

// KeyFor is the key bound to an action.
func KeyFor(a Action) KeyKind { return activeBindings[a] }

// BindingsPath is ~/.pi-agent/keybindings.json.
func BindingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi-agent", "keybindings.json"), nil
}

// loadBindings merges the file over the defaults.
//
// An unreadable file, an unknown action, or an unbindable key each fall back
// to the default for that entry alone — a typo costs one binding, never the
// keyboard.
func loadBindings() Bindings {
	out := Bindings{}
	for a, k := range defaultBindings {
		out[a] = k
	}
	path, err := BindingsPath()
	if err != nil {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return out
	}
	for action, keyName := range raw {
		a := Action(action)
		if _, known := defaultBindings[a]; !known {
			continue
		}
		if k, ok := bindableKeys[strings.ToLower(strings.TrimSpace(keyName))]; ok {
			out[a] = k
		}
	}
	return out
}

// BindingList is the resolved table, for display.
func BindingList() []struct {
	Action Action
	Key    string
} {
	names := map[KeyKind]string{}
	for name, kind := range bindableKeys {
		names[kind] = name
	}
	order := []Action{
		ActionUndo, ActionRedo, ActionNav, ActionLineStart,
		ActionLineEnd, ActionKillLine, ActionKillWord, ActionDeleteLine,
	}
	out := make([]struct {
		Action Action
		Key    string
	}, 0, len(order))
	for _, a := range order {
		out = append(out, struct {
			Action Action
			Key    string
		}{a, names[activeBindings[a]]})
	}
	return out
}
