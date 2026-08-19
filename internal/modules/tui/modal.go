package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The picker family.
//
// loop has three of these and they are not interchangeable, so this has three
// too — as one component with a mode, because they differ only in the header,
// the hint, and what a printable key does:
//
//	ModalSelect  a short, known list. No filter box: typing would be noise
//	             when every option is already on screen.
//	ModalSearch  a long list. Printable keys build a query.
//	ModalToggle  a set. Space flips a row, a `done` row confirms the set.
//
// Offering a filter box on a six-row menu was the wrong default: it advertises
// an interaction that cannot pay off, and it costs a line of chrome on every
// menu in the app.

// ModalMode selects which of the three pickers this is.
type ModalMode int

const (
	// ModalSelect is a plain list — no filtering.
	ModalSelect ModalMode = iota
	// ModalSearch adds a type-to-filter query.
	ModalSearch
	// ModalToggle is a multi-select over a set of values.
	ModalToggle
)

// Modal is the in-app picker, owning the key stream while open.
type Modal struct {
	Theme *Theme
	Mode  ModalMode
	// Current is the value already in force, annotated in the list.
	Current string
	Title   string
	All     []Item
	Shown   []Item
	Query   string
	Cursor  int
	Result  chan *Item

	// Toggle state, used only by ModalToggle.
	//
	// selected is the live set; values is the underlying value list in its
	// original order, so the result comes back ordered rather than in the
	// order the user happened to click.
	selected map[string]bool
	values   []string
	// ToggleResult carries the confirmed set. Result still carries the
	// cancel (nil), so one close path serves both.
	ToggleResult chan []string
}

// modalVisible caps the list at loop's ten rows. More than that and the modal
// owns the screen; fewer and a long catalog turns into scrolling.
const modalVisible = 10

// The primary column is a FIXED width, not the widest label in view.
//
// Sizing it to the content makes the description column jump left and right
// as the filter narrows the list, which reads as the whole modal twitching.
// A fixed column means only the rows change.
const (
	primaryColumnWidth   = 32
	primaryColumnGap     = 2
	minDescriptionWidth  = 10
	descriptionMinScreen = 40
)

// toggleDone is the sentinel value of the confirm row in a toggle modal.
const toggleDone = "\x00done"

// NewToggleModal builds the multi-select over values, pre-checking initial.
func NewToggleModal(theme *Theme, title string, values []string, initial map[string]bool) *Modal {
	selected := make(map[string]bool, len(initial))
	for v, on := range initial {
		if on {
			selected[v] = true
		}
	}
	m := &Modal{
		Theme: theme, Mode: ModalToggle, Title: title,
		values: values, selected: selected,
		Result:       make(chan *Item, 1),
		ToggleResult: make(chan []string, 1),
	}
	m.rebuildToggleItems()
	return m
}

// rebuildToggleItems regenerates the rows from the live selection.
func (m *Modal) rebuildToggleItems() {
	items := make([]Item, 0, len(m.values)+1)
	items = append(items, m.doneItem())
	for _, v := range m.values {
		box := "[ ]"
		if m.selected[v] {
			box = "[x]"
		}
		items = append(items, Item{Value: v, Label: box + " " + v})
	}
	m.All = items
	m.Refilter()
}

// doneItem is the confirm row, which doubles as the count readout.
func (m *Modal) doneItem() Item {
	var picked []string
	for _, v := range m.values {
		if m.selected[v] {
			picked = append(picked, v)
		}
	}
	desc := strings.Join(picked, ", ")
	switch {
	case len(picked) == len(m.values) && len(m.values) > 0:
		desc = "all"
	case desc == "":
		desc = "none — pick at least one"
	}
	return Item{
		Value:       toggleDone,
		Label:       fmt.Sprintf("done (%d/%d)", len(picked), len(m.values)),
		Description: desc,
	}
}

// Refilter applies the query to All.
//
// A toggle modal filters only the value rows: `done` is the way out, and a
// query that hid it would strand the user in a list with no confirm.
func (m *Modal) Refilter() {
	switch m.Mode {
	case ModalSelect:
		m.Shown = append([]Item{}, m.All...)
	case ModalToggle:
		if m.Query == "" {
			m.Shown = append([]Item{}, m.All...)
		} else {
			m.Shown = append([]Item{m.All[0]}, matchItems(m.All[1:], m.Query)...)
		}
	default:
		m.Shown = matchItems(m.All, m.Query)
	}
	if m.Cursor >= len(m.Shown) {
		m.Cursor = max(0, len(m.Shown)-1)
	}
}

// Handle routes one key; it returns true when the modal closed.
func (m *Modal) Handle(k Key, keys *KeyDecoder) (done bool) {
	switch k.Kind {
	case KeyUp:
		if n := len(m.Shown); n > 0 {
			m.Cursor = (m.Cursor - 1 + n) % n
		}
	case KeyDown:
		if n := len(m.Shown); n > 0 {
			m.Cursor = (m.Cursor + 1) % n
		}
	case KeyEnter:
		return m.activate()
	case KeyEsc, KeyCtrlC:
		m.cancel()
		return true
	case KeyBackspace:
		if m.Mode != ModalSelect && m.Query != "" {
			_, size := utf8.DecodeLastRuneInString(m.Query)
			m.Query = m.Query[:len(m.Query)-size]
			m.Refilter()
		}
	case KeyRune:
		// A select modal takes no query: printable keys are ignored rather
		// than silently building a filter that is never shown.
		if m.Mode == ModalSelect {
			return false
		}
		// Space is a toggle, not a character — no value contains one.
		if m.Mode == ModalToggle && k.Rune == ' ' {
			m.flip()
			return false
		}
		m.Query += string(k.Rune)
		m.Refilter()
	}
	return false
}

// activate is Enter: confirm in a select/search modal, flip-or-confirm in a
// toggle one.
func (m *Modal) activate() (done bool) {
	if m.Cursor < 0 || m.Cursor >= len(m.Shown) {
		if m.Mode == ModalToggle {
			return false
		}
		m.Result <- nil
		return true
	}
	if m.Mode == ModalToggle {
		if m.Shown[m.Cursor].Value != toggleDone {
			m.flip()
			return false
		}
		picked := m.picked()
		if len(picked) == 0 {
			// Nothing chosen is not an answer — stay open rather than
			// confirming an empty set the caller cannot use.
			return false
		}
		m.ToggleResult <- picked
		return true
	}
	v := m.Shown[m.Cursor]
	m.Result <- &v
	return true
}

// flip toggles the highlighted value, keeping the cursor where it is.
func (m *Modal) flip() {
	if m.Cursor < 0 || m.Cursor >= len(m.Shown) {
		return
	}
	value := m.Shown[m.Cursor].Value
	if value == toggleDone {
		return
	}
	m.selected[value] = !m.selected[value]
	// Rebuilding resets the window, so the cursor is restored by value: the
	// row you just flipped must still be the row under the cursor.
	cursor, query := m.Cursor, m.Query
	m.rebuildToggleItems()
	m.Query = query
	m.Refilter()
	m.Cursor = min(cursor, max(0, len(m.Shown)-1))
}

// picked is the confirmed set, in the caller's original order.
func (m *Modal) picked() []string {
	var out []string
	for _, v := range m.values {
		if m.selected[v] {
			out = append(out, v)
		}
	}
	return out
}

func (m *Modal) cancel() {
	if m.Mode == ModalToggle {
		m.ToggleResult <- nil
		return
	}
	m.Result <- nil
}

// hint is the key legend under the list.
func (m *Modal) hint() string {
	switch m.Mode {
	case ModalSelect:
		return " ↑↓ navigate · Enter select · Esc cancel"
	case ModalToggle:
		return " type to filter · ↑↓ navigate · Enter/Space toggle · done confirms · Esc cancel"
	default:
		return " type to filter · ↑↓ navigate · Enter select · Esc cancel"
	}
}

// header is the title row, with the live query on the two filtering modes.
func (m *Modal) header(width int) string {
	t := m.Theme
	title := " " + t.Fg(SlotAccent, t.Bold(m.Title))
	if m.Mode == ModalSelect {
		return fitRow(title, width)
	}
	query := t.Fg(SlotDim, "(type to filter)")
	if m.Query != "" {
		query = t.Fg(SlotText, t.Bold(m.Query))
	}
	return fitRow(title+t.Fg(SlotDim, "  search: ")+query, width)
}

// View renders the picker: a title, the list between two full-width rules,
// and a hint line.
//
// The rules are what make it read as a surface laid over the transcript
// rather than more transcript. A list with only a heading above it looks like
// output the agent produced.
func (m *Modal) View(width int) []string {
	t := m.Theme
	if t == nil {
		t = NewTheme(NightPalette)
		m.Theme = t
	}
	rule := t.Fg(SlotBorder, strings.Repeat("─", max(width, 0)))
	out := []string{m.header(width), rule}

	if len(m.Shown) == 0 {
		out = append(out, t.Fg(SlotMuted, "  No matching commands"))
		return append(out, rule, t.Fg(SlotDim, m.hint()))
	}

	// The window follows the selection rather than paging, so arrowing never
	// jumps the list out from under the eye.
	start := max(0, min(m.Cursor-modalVisible/2, len(m.Shown)-modalVisible))
	end := min(start+modalVisible, len(m.Shown))

	for i := start; i < end; i++ {
		out = append(out, m.row(m.Shown[i], i == m.Cursor, width))
	}
	if start > 0 || end < len(m.Shown) {
		out = append(out, t.Fg(SlotMuted, fmt.Sprintf("  (%d/%d)", m.Cursor+1, len(m.Shown))))
	}
	return append(out, rule, t.Fg(SlotDim, m.hint()))
}

// row renders one entry: a marker, the label in a fixed column, then the
// description in what is left.
func (m *Modal) row(it Item, selected bool, width int) string {
	t := m.Theme
	marker := "  "
	if selected {
		marker = "→ "
	}
	description := singleLine(it.Description)
	// The entry already in force is marked in its description rather than
	// right-aligned on the row: right-aligned, it drifts away from the label
	// it belongs to as the terminal gets wider.
	if m.Current != "" && it.Value == m.Current {
		if description == "" {
			description = "(current)"
		} else {
			description += "  (current)"
		}
	}

	if description != "" && width > descriptionMinScreen {
		column := max(1, min(primaryColumnWidth, width-len(marker)-4))
		label := truncate(it.Label, max(1, column-primaryColumnGap))
		spacing := strings.Repeat(" ", max(1, column-visibleWidth(label)))
		remaining := width - (len(marker) + visibleWidth(label) + len(spacing)) - 2
		if remaining > minDescriptionWidth {
			desc := truncate(description, remaining)
			if selected {
				return fitRow(t.Fg(SlotAccent, marker+label+spacing+desc), width)
			}
			return fitRow(marker+t.Fg(SlotText, label)+t.Fg(SlotMuted, spacing+desc), width)
		}
	}

	label := truncate(it.Label, max(1, width-len(marker)-2))
	if selected {
		return fitRow(t.Fg(SlotAccent, marker+label), width)
	}
	return fitRow(marker+t.Fg(SlotText, label), width)
}

// singleLine flattens a description: a row is one line, and a newline in a
// description would push the whole list down by one.
func singleLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == '\r' || r == '\n'
	}), " "))
}
