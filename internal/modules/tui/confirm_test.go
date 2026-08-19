package tui

import (
	"strings"
	"testing"
)

func newConfirm(subject string) (*Confirm, chan bool) {
	result := make(chan bool, 1)
	return &Confirm{Theme: testTheme(), Tool: "bash", Subject: subject, Result: result}, result
}

// The default answer is NO, so a stray Enter declines rather than approves.
func TestConfirmDefaultsToDeclining(t *testing.T) {
	c, result := newConfirm("rm -rf build")
	if c.Approved {
		t.Fatal("a fresh prompt should default to no")
	}
	if !c.Handle(Key{Kind: KeyEnter}) {
		t.Fatal("enter should close the prompt")
	}
	if <-result {
		t.Error("a bare enter approved the call")
	}
}

func TestConfirmKeys(t *testing.T) {
	cases := []struct {
		name string
		keys []Key
		want bool
	}{
		{"y approves", []Key{{Kind: KeyRune, Rune: 'y'}}, true},
		{"n declines", []Key{{Kind: KeyRune, Rune: 'n'}}, false},
		{"esc declines", []Key{{Kind: KeyEsc}}, false},
		{"ctrl+c declines", []Key{{Kind: KeyCtrlC}}, false},
		{"toggle then enter approves", []Key{{Kind: KeyRight}, {Kind: KeyEnter}}, true},
		{"toggle twice then enter declines", []Key{{Kind: KeyRight}, {Kind: KeyLeft}, {Kind: KeyEnter}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prompt, result := newConfirm("git push")
			for _, k := range c.keys {
				prompt.Handle(k)
			}
			select {
			case got := <-result:
				if got != c.want {
					t.Errorf("got %v, want %v", got, c.want)
				}
			default:
				t.Error("prompt never answered")
			}
		})
	}
}

// A command you approve on the strength of its first forty characters is a
// command you did not read, so the subject wraps rather than truncating.
func TestConfirmShowsTheWholeSubject(t *testing.T) {
	long := "for f in $(find . -name '*.go'); do sed -i '' 's/old/new/g' $f; done"
	c, _ := newConfirm(long)
	got := stripANSI(strings.Join(c.View(60), "\n"))

	flat := strings.Join(strings.Fields(got), " ")
	for _, fragment := range strings.Fields(long) {
		if !strings.Contains(flat, fragment) {
			t.Errorf("subject fragment %q missing from:\n%s", fragment, got)
		}
	}
}

func TestConfirmRowsFitTheWidth(t *testing.T) {
	c, _ := newConfirm(strings.Repeat("some very long command ", 20))
	c.Reason = "writes outside the working directory"
	for _, width := range []int{24, 40, 80} {
		for _, line := range c.View(width) {
			if w := visibleWidth(line); w > width {
				t.Errorf("width %d: %d-cell line %q", width, w, line)
			}
		}
	}
}

func TestConfirmShowsTheReason(t *testing.T) {
	c, _ := newConfirm("write /etc/hosts")
	c.Reason = "writes outside the working directory"
	got := stripANSI(strings.Join(c.View(70), "\n"))
	if !strings.Contains(got, "outside the working directory") {
		t.Errorf("reason missing:\n%s", got)
	}
}
