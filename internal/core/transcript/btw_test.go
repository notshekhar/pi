package transcript

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/session"
)

var askedFrom = session.BtwMeta{ParentID: "s1", ParentTitle: "Fix the parser"}

// A saved side question carries where it was asked.
func TestSavedSideQuestionCarriesWhereItWasAsked(t *testing.T) {
	e := session.Entry{Type: session.TypeMessage, Role: "user", Content: jsontext.Value(`"why?"`), TS: 1, ID: "u1"}
	e.SetBtw(askedFrom)
	got := FromEntries([]session.Entry{e}, false, nil)
	if b := got.Messages[0].Metadata.Btw; b == nil || *b != askedFrom {
		t.Fatalf("btw = %+v", b)
	}
}

// The live user-message event carries it too.
func TestLiveSideQuestionCarriesWhereItWasAsked(t *testing.T) {
	tr := Empty()
	tr.Apply(Event{Type: "user-message", Data: agent.UserMessageEvent{ID: "u1", Text: "why?", TS: 1, Btw: &askedFrom}}, 1)
	if b := tr.Messages[0].Metadata.Btw; b == nil || *b != askedFrom {
		t.Fatalf("btw = %+v", b)
	}
}
