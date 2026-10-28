package main

import (
	"os"
	"testing"
)

// Tests never chime: loop's suites set LOOP_SOUND=0 the same way.
func TestMain(m *testing.M) {
	_ = os.Setenv("LOOP_SOUND", "0")
	os.Exit(m.Run())
}

func TestSemverGt(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.1.5", "v0.1.4", true}, {"v0.1.4", "v0.1.4", false}, {"v1.0.0", "v1.0.0-beta.1", true},
		{"v1.0.0-beta.2", "v1.0.0-beta.10", false}, {"v0.10.0", "v0.9.9", true}, {"garbage", "v0.1.0", false},
	}
	for _, c := range cases {
		if got := semverGt(c.a, c.b); got != c.want {
			t.Errorf("%s > %s = %v", c.a, c.b, got)
		}
	}
}
