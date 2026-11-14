package remote

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
	"github.com/notshekhar/pi/internal/rpc"
	"github.com/notshekhar/pi/internal/rpc/serve"
)

// testPort is fixed because serve treats 0 as its default port; it differs
// from serve's own tests (47667) so the two packages can run together.
const testPort = 47668

func TestParsePairingLink(t *testing.T) {
	cases := []struct {
		name, input, token string
		want               PairingLink
		ok                 bool
	}{
		{"the URL serve prints", "http://192.168.1.4:5667/?token=abc", "", PairingLink{"http://192.168.1.4:5667", "abc"}, true},
		{"a token in the fragment", "http://studio.tail1.ts.net:5667/#token=abc", "", PairingLink{"http://studio.tail1.ts.net:5667", "abc"}, true},
		{"a bare host gets serve's port", "studio", "t0k", PairingLink{"http://studio:5667", "t0k"}, true},
		{"a bare host keeps its port", "studio:6000", "t0k", PairingLink{"http://studio:6000", "t0k"}, true},
		{"an explicit URL keeps no port", "https://loop.example.com/?token=x", "", PairingLink{"https://loop.example.com", "x"}, true},
		{"no token", "http://studio:5667/", "", PairingLink{}, false},
		{"not http", "ftp://studio/?token=x", "", PairingLink{}, false},
		{"blank", "   ", "", PairingLink{}, false},
	}
	for _, c := range cases {
		got, ok := ParsePairingLink(c.input, c.token)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// startHost is a real serve in a temp HOME — nothing touches the real
// ~/.loop. Its turns fail at once (an unknown provider), which is still a
// whole turn as a client sees it.
func startHost(t *testing.T) *serve.Handle {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOOP_WEB_APP_DIR", t.TempDir())
	newRun := func(s *session.Session, _ string, _ tools.Asker) (*agent.Run, error) {
		return &agent.Run{Config: config.Config{Provider: "nope", ModelID: "none", CWD: t.TempDir()}, Session: s}, nil
	}
	h, err := serve.Start(serve.Options{Host: "127.0.0.1", Port: testPort, Version: "1.2.3", RPC: rpc.Host{NewRun: newRun}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h
}

func pair(t *testing.T, h *serve.Handle) HostRecord {
	t.Helper()
	link, ok := ParsePairingLink(h.URL, "")
	if !ok {
		t.Fatalf("serve's own URL is not a pairing link: %s", h.URL)
	}
	record, err := Pair(context.Background(), nil, link)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func until(t *testing.T, label string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("until: condition not met: %s", label)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPairsOnceAndKeepsTheLabel(t *testing.T) {
	h := startHost(t)
	record := pair(t, h)
	if record.URL != "http://127.0.0.1:47668" || record.Token != h.Token {
		t.Fatalf("record = %+v", record)
	}
	if !regexp.MustCompile(`^loop-[0-9a-f]{24}$`).MatchString(record.ID) {
		t.Fatalf("id = %q", record.ID)
	}
	// Pairing again (a new address, a rotated token) replaces, never duplicates.
	if err := Rename(record.ID, "studio"); err != nil {
		t.Fatal(err)
	}
	again := pair(t, h)
	if n := len(Hosts()); n != 1 {
		t.Fatalf("%d hosts after re-pairing, want 1", n)
	}
	if again.Label != "studio" {
		t.Fatalf("label = %q, want the rename kept", again.Label)
	}
}

func TestPairsWithTheCodeAndKeepsTheToken(t *testing.T) {
	h := startHost(t)
	code := h.PairingCode(true).Code
	link, _ := ParsePairingLink("http://127.0.0.1:47668", code)
	record, err := Pair(context.Background(), nil, link)
	if err != nil {
		t.Fatal(err)
	}
	if record.Token != h.Token {
		t.Fatalf("saved %q, want the host's token, not the code", record.Token)
	}
}

func TestRefusesAWrongTokenAndSavesNothing(t *testing.T) {
	startHost(t)
	_, err := Pair(context.Background(), nil, PairingLink{URL: "http://127.0.0.1:47668", Token: "nope"})
	if err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("err = %v", err)
	}
	if n := len(Hosts()); n != 0 {
		t.Fatalf("%d hosts saved", n)
	}
}

func TestForgetsAMachineByName(t *testing.T) {
	h := startHost(t)
	record := pair(t, h)
	if err := Rename(record.ID, "Studio"); err != nil {
		t.Fatal(err)
	}
	gone, err := Remove("studio")
	if err != nil || gone.ID != record.ID {
		t.Fatalf("Remove = %+v, %v", gone, err)
	}
	if n := len(Hosts()); n != 0 {
		t.Fatalf("%d hosts left", n)
	}
}

func TestDrivesATurnOverTheSocket(t *testing.T) {
	h := startHost(t)
	client := NewClient(pair(t, h), Options{})
	defer client.Close()
	ctx := context.Background()

	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := client.Call(ctx, "session.create", map[string]any{"cwd": t.TempDir(), "provider": "nope", "model": "nope/none"}, &created); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := client.Call(ctx, "session.list", nil, &rows); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(rows, func(r struct {
		ID string `json:"id"`
	}) bool {
		return r.ID == created.SessionID
	}) {
		t.Fatalf("session.list has no %s", created.SessionID)
	}

	var mu sync.Mutex
	var events []SessionEvent
	client.OnSessionEvent(created.SessionID, func(e SessionEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})
	if err := client.Call(ctx, "session.attach", map[string]any{"sessionId": created.SessionID, "afterSeq": 0}, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(ctx, "session.send", map[string]any{"sessionId": created.SessionID, "input": "hi"}, nil); err != nil {
		t.Fatal(err)
	}
	until(t, "turn end", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.ContainsFunc(events, func(e SessionEvent) bool {
			return e.Part.Type == "session-running" && strings.Contains(string(e.Part.Data), `"running":false`)
		})
	})
	mu.Lock()
	defer mu.Unlock()
	types := make([]string, len(events))
	seqs := make([]int64, len(events))
	for i, e := range events {
		types[i], seqs[i] = e.Part.Type, e.Seq
	}
	if types[0] != "session-running" || !slices.Contains(types, "error") {
		t.Fatalf("events = %v", types)
	}
	// Seqs are the host's, strictly increasing — what a reconnect resumes from.
	if !slices.IsSorted(seqs) {
		t.Fatalf("seqs out of order: %v", seqs)
	}
}

// recordingDialer dials real sockets and keeps them, so a test can drop one.
type recordingDialer struct {
	mu      sync.Mutex
	sockets []Socket
	wrap    func(dial int, s Socket) Socket
}

func (d *recordingDialer) dial(ctx context.Context, url string) (Socket, error) {
	s, err := dialWebSocket(ctx, url)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.wrap != nil {
		s = d.wrap(len(d.sockets)+1, s)
	}
	d.sockets = append(d.sockets, s)
	return s, nil
}

func (d *recordingDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sockets)
}

func (d *recordingDialer) drop(i int) {
	d.mu.Lock()
	s := d.sockets[i]
	d.mu.Unlock()
	_ = s.Close()
}

type statusLog struct {
	mu   sync.Mutex
	seen []Status
}

func (l *statusLog) add(s Status) {
	l.mu.Lock()
	l.seen = append(l.seen, s)
	l.mu.Unlock()
}

func (l *statusLog) count(s Status) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, x := range l.seen {
		if x == s {
			n++
		}
	}
	return n
}

// helloCutter loses its socket the moment it says hello.
type helloCutter struct{ Socket }

func (h helloCutter) Write(ctx context.Context, data []byte) error {
	if strings.Contains(string(data), `"hello"`) {
		return h.Socket.Close()
	}
	return h.Socket.Write(ctx, data)
}

func TestAHelloCutOffKeepsReconnecting(t *testing.T) {
	h := startHost(t)
	d := &recordingDialer{wrap: func(dial int, s Socket) Socket {
		if dial == 2 {
			return helloCutter{s}
		}
		return s
	}}
	client := NewClient(pair(t, h), Options{Dial: d.dial, ReconnectBase: 20 * time.Millisecond})
	defer client.Close()
	statuses := &statusLog{}
	client.OnStatus(statuses.add)
	if err := client.Call(context.Background(), "session.list", nil, nil); err != nil {
		t.Fatal(err)
	}
	d.drop(0)
	until(t, "reopened after the failed hello", func() bool { return statuses.count(StatusOpen) == 2 })
	if n := d.count(); n < 3 {
		t.Fatalf("%d dials, want at least 3", n)
	}
	if statuses.count(StatusClosed) != 0 {
		t.Fatal("a cut-off hello closed the client for good")
	}
}

func TestReconnectsAfterTheSocketDrops(t *testing.T) {
	h := startHost(t)
	d := &recordingDialer{}
	client := NewClient(pair(t, h), Options{Dial: d.dial, ReconnectBase: 20 * time.Millisecond})
	defer client.Close()
	statuses := &statusLog{}
	client.OnStatus(statuses.add)
	ctx := context.Background()
	if err := client.Call(ctx, "session.list", nil, nil); err != nil {
		t.Fatal(err)
	}
	d.drop(0)
	until(t, "reopened", func() bool { return statuses.count(StatusOpen) == 2 })
	if statuses.count(StatusReconnecting) == 0 {
		t.Fatal("never reported reconnecting")
	}
	var rows []any
	if err := client.Call(ctx, "session.list", nil, &rows); err != nil {
		t.Fatal(err)
	}
}

func TestATokenTheHostNoLongerTakesSaysPairAgain(t *testing.T) {
	h := startHost(t)
	record := pair(t, h)
	record.Token = "rotated"
	client := NewClient(record, Options{})
	defer client.Close()
	err := client.Call(context.Background(), "session.list", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "pair it again") {
		t.Fatalf("err = %v", err)
	}
}
