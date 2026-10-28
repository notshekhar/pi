package shells

import (
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestStartReadKill(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetForTests()
	info, err := Start(StartOptions{SessionID: "s1", Command: "echo hello; echo world", CWD: t.TempDir()})
	if err != nil || info.ID != "bash_1" {
		t.Fatalf("start: %v %+v", err, info)
	}
	waitFor(t, func() bool { return List("s1")[0].Status == "exited" })
	r, ok := Read("s1", "bash_1", "", false)
	if !ok || r.Text != "hello\nworld\n" {
		t.Fatalf("read %q", r.Text)
	}
	if r2, _ := Read("s1", "bash_1", "", false); r2.Text != "" {
		t.Fatalf("second read should be empty, got %q", r2.Text)
	}
	if n := TakeNotices("s1"); len(n) != 1 || *n[0].ExitCode != 0 {
		t.Fatalf("notices %+v", n)
	}

	long, _ := Start(StartOptions{SessionID: "s1", Command: "sleep 30", CWD: t.TempDir()})
	if RunningCount("s1") != 1 {
		t.Fatal("expected one running")
	}
	Kill("s1", long.ID)
	waitFor(t, func() bool { return List("s1")[1].Status == "killed" })
	if !strings.HasPrefix(StatusText(List("s1")[0]), "exited 0") {
		t.Fatal(StatusText(List("s1")[0]))
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{8 * time.Second: "8s", 134 * time.Second: "2m14s", 3780 * time.Second: "1h03m"} {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: %s want %s", d, got, want)
		}
	}
}
