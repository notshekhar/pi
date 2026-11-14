package session

import (
	"encoding/json/jsontext"
	"reflect"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
)

const btwCWD = "/tmp/btw-project"

// sourceChat is a named chat with one exchange in it.
func sourceChat(t *testing.T) *Session {
	t.Helper()
	source, err := CreateWith("anthropic", "anthropic/m", btwCWD)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SetName("Fix the parser"); err != nil {
		t.Fatal(err)
	}
	if err := source.Add(user("fix the parser"), assistant("on it")); err != nil {
		t.Fatal(err)
	}
	return source
}

// askedThread is a side thread with its tagged question saved.
func askedThread(t *testing.T, source *Session, question string) (*Session, BtwMeta) {
	t.Helper()
	thread, meta, err := CreateBtwThread(source, question)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := MessageEntry(user(question))
	if err != nil {
		t.Fatal(err)
	}
	entry.SetBtw(meta)
	if err := thread.AppendAll(entry); err != nil {
		t.Fatal(err)
	}
	return thread, meta
}

func TestBtwThreadNameCollapsesWhitespaceAndClips(t *testing.T) {
	if got := BtwThreadName("why   is the lexer\nso slow?"); got != "btw: why is the lexer so slow?" {
		t.Errorf("name = %q", got)
	}
	long := BtwThreadName(strings.Repeat("a", 70))
	if want := "btw: " + strings.Repeat("a", 59) + "…"; long != want {
		t.Errorf("long name = %q, want %q", long, want)
	}
}

// A side thread is fresh and named after its question; the source is not
// touched.
func TestCreateBtwThreadStartsAFreshNamedThreadThatCarriesNothingOfTheSource(t *testing.T) {
	withSessionHome(t)
	source := sourceChat(t)
	before := mustJSON(source.Entries())

	thread, meta, err := CreateBtwThread(source, "why   is the lexer\nso slow?")
	if err != nil {
		t.Fatal(err)
	}
	if mustJSON(source.Entries()) != before {
		t.Error("asking a side question changed the source session")
	}
	if meta != (BtwMeta{ParentID: source.ID, ParentTitle: "Fix the parser"}) {
		t.Errorf("meta = %+v", meta)
	}
	reopened, err := Load(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Name() != "btw: why is the lexer so slow?" || reopened.Info.CWD != btwCWD {
		t.Errorf("thread = %q in %q", reopened.Name(), reopened.Info.CWD)
	}
	if n := len(reopened.BranchMessages()); n != 0 {
		t.Errorf("thread starts with %d messages, want none", n)
	}
}

// The model sees the question alone, exactly as typed.
func TestBtwModelSeesTheQuestionAloneAsTyped(t *testing.T) {
	withSessionHome(t)
	thread, _ := askedThread(t, sourceChat(t), "what is a monad?")
	want := []provider.Message{user("what is a monad?")}
	if got := thread.ModelMessages(); !reflect.DeepEqual(got, want) {
		t.Errorf("sent %s, want %s", mustJSON(got), mustJSON(want))
	}
}

func TestBtwQuestionKeepsItsTagAfterAReload(t *testing.T) {
	withSessionHome(t)
	thread, meta := askedThread(t, sourceChat(t), "why?")
	reopened, err := Load(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	question, _ := reopened.Leaf()
	if got, ok := question.Btw(); !ok || got != meta {
		t.Errorf("reloaded tag = %+v %v, want %+v", got, ok, meta)
	}
}

func TestCreateBtwThreadRefusesAnEmptyQuestion(t *testing.T) {
	withSessionHome(t)
	if _, _, err := CreateBtwThread(sourceChat(t), "  "); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want the empty-question refusal", err)
	}
}

// A side thread names the session it was asked from — in its own branch,
// and in a session-list row.
func TestBtwThreadNamesTheSessionItWasAskedFrom(t *testing.T) {
	withSessionHome(t)
	source := sourceChat(t)
	thread, _ := askedThread(t, source, "why?")

	if parent, ok := BtwParentIn(thread.CurrentBranch()); !ok || parent.ParentID != source.ID {
		t.Errorf("parent in branch = %+v %v", parent, ok)
	}
	rows, err := Rows(btwCWD, true, Active, Page{Limit: -1})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID != thread.ID {
			continue
		}
		if parent, ok := BtwParentOf(*row.Name, row.ParentSession); !ok || parent != source.ID {
			t.Errorf("parent from the row = %q %v, want %s", parent, ok, source.ID)
		}
		return
	}
	t.Fatal("the side thread is not listed")
}

// A plain fork, or a renamed side thread, is not one.
func TestAForkOrARenamedSideThreadIsNotOne(t *testing.T) {
	if _, ok := BtwParentOf("Fix it", "/x/01ABC.jsonl"); ok {
		t.Error("a plain fork read as a side thread")
	}
	if _, ok := BtwParentOf("btw: why?", ""); ok {
		t.Error("a btw name with no parent read as a side thread")
	}
	plain := Entry{Type: TypeMessage, Role: "user", Content: jsontext.Value(`"hi"`), ID: "u1"}
	if _, ok := BtwParentIn([]Entry{plain}); ok {
		t.Error("an untagged chat read as a side thread")
	}
}
