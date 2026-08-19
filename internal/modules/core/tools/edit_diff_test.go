package tools

import (
	"strings"
	"testing"
)

func TestExactEdit(t *testing.T) {
	base, next, err := applyEdits("hello world\n", []Replacement{{OldText: "world", NewText: "there"}}, "f.go")
	if err != nil {
		t.Fatal(err)
	}
	if base != "hello world\n" {
		t.Fatalf("base mutated: %q", base)
	}
	if next != "hello there\n" {
		t.Errorf("next = %q", next)
	}
}

func TestFuzzyEditDoesNotRewriteUntouchedQuotes(t *testing.T) {
	// The file uses a curly quote the model will not type. A naive
	// "normalize the whole file, then write it back" would straighten
	// every quote. The match must land on the real span only.
	content := "title = “keep me”\nhello “world”\n"
	_, next, err := applyEdits(content, []Replacement{{OldText: `hello "world"`, NewText: "hello there"}}, "f.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "“keep me”") {
		t.Fatalf("untouched curly quotes were rewritten:\n%s", next)
	}
	if !strings.Contains(next, "hello there") {
		t.Fatalf("replacement missing:\n%s", next)
	}
}

func TestDuplicateOldTextRejected(t *testing.T) {
	_, _, err := applyEdits("foo\nfoo\n", []Replacement{{OldText: "foo", NewText: "bar"}}, "f.go")
	if err == nil || !strings.Contains(err.Error(), "2 occurrences") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingOldTextRejected(t *testing.T) {
	_, _, err := applyEdits("abc\n", []Replacement{{OldText: "zzz", NewText: "y"}}, "f.go")
	if err == nil {
		t.Fatal("expected not-found")
	}
}

func TestOverlappingEditsRejected(t *testing.T) {
	_, _, err := applyEdits("abcdef\n", []Replacement{
		{OldText: "abcd", NewText: "AB"},
		{OldText: "cdef", NewText: "CD"},
	}, "f.go")
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyOldTextRejected(t *testing.T) {
	_, _, err := applyEdits("abc\n", []Replacement{{OldText: "", NewText: "x"}}, "f.go")
	if err == nil {
		t.Fatal("expected empty oldText error")
	}
}

func TestNoChangeRejected(t *testing.T) {
	_, _, err := applyEdits("same\n", []Replacement{{OldText: "same", NewText: "same"}}, "f.go")
	if err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("err = %v", err)
	}
}

func TestMultipleDisjointEdits(t *testing.T) {
	_, next, err := applyEdits("one two three\n", []Replacement{
		{OldText: "one", NewText: "1"},
		{OldText: "three", NewText: "3"},
	}, "f.go")
	if err != nil {
		t.Fatal(err)
	}
	if next != "1 two 3\n" {
		t.Errorf("next = %q", next)
	}
}
