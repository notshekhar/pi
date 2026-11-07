package main

import (
	"reflect"
	"testing"
)

func TestWithoutScriptExtensions(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		// What cmux's pi wrapper actually runs.
		{[]string{"-e", "/tmp/cmux-pi-extensions/x/cmux-session.ts", "upgrade"}, []string{"upgrade"}},
		{[]string{"-e", "/tmp/cmux-session.ts"}, []string{}},
		{[]string{"--extension=/a/b.js", "run", "hi"}, []string{"run", "hi"}},
		{[]string{"-model", "x/y", "-e", "a.ts", "-session", "01"}, []string{"-model", "x/y", "-session", "01"}},
		// Anything that is not a script extension is left for the flag parser.
		{[]string{"-e", "notes.txt"}, []string{"-e", "notes.txt"}},
		{[]string{"run", "explain", "main.ts"}, []string{"run", "explain", "main.ts"}},
		{[]string{"-e"}, []string{"-e"}},
	}
	for _, c := range cases {
		if got := withoutScriptExtensions(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q → %q, want %q", c.in, got, c.want)
		}
	}
}
