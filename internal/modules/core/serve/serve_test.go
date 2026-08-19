package serve

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
)

func start(t *testing.T, onPrompt func(string)) *Server {
	t.Helper()
	s := New(onPrompt)
	// A negative port asks the OS for a free one, so tests never collide —
	// and they did, on a runner fast enough to start two before the first
	// released 4517.
	if err := s.Start("127.0.0.1", -1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// The token is the only thing between a stranger on the machine and a shell.
func TestUnauthorizedIsRejected(t *testing.T) {
	s := start(t, nil)
	resp, err := http.Get("http://" + s.Addr + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token got %d, want 401", resp.StatusCode)
	}

	resp2, err := http.Get("http://" + s.Addr + "/health?token=wrong")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token got %d, want 401", resp2.StatusCode)
	}
}

func TestHealthWithToken(t *testing.T) {
	s := start(t, nil)
	resp, err := http.Get("http://" + s.Addr + "/health?token=" + s.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d", resp.StatusCode)
	}
}

// Binding loopback by default: an agent that runs shell commands must not be
// reachable from the network because someone typed a port.
func TestBindsLoopbackByDefault(t *testing.T) {
	s := New(nil)
	if err := s.Start("", -1); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if !strings.HasPrefix(s.Addr, "127.0.0.1:") {
		t.Errorf("bound %q, want loopback", s.Addr)
	}
}

func TestEventsStream(t *testing.T) {
	s := start(t, nil)
	req, _ := http.NewRequest("GET", "http://"+s.Addr+"/events?token="+s.Token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Give the handler a moment to register before publishing.
	time.Sleep(100 * time.Millisecond)
	s.Publish(Event{Kind: "text", Text: "hello"})

	line := make(chan string, 1)
	go func() {
		r := bufio.NewReader(resp.Body)
		for {
			b, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			if bytes.HasPrefix(b, []byte("data:")) {
				line <- string(b)
				return
			}
		}
	}()
	select {
	case got := <-line:
		if !strings.Contains(got, "hello") {
			t.Errorf("event = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event arrived")
	}
}

func TestPromptIsDelivered(t *testing.T) {
	got := make(chan string, 1)
	s := start(t, func(p string) { got <- p })

	resp, err := http.Post("http://"+s.Addr+"/prompt?token="+s.Token,
		"application/json", strings.NewReader(`{"prompt":"do a thing"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	select {
	case p := <-got:
		if p != "do a thing" {
			t.Errorf("prompt = %q", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt never arrived")
	}
}

// A browser that stopped reading must not stall the agent.
func TestPublishDoesNotBlockOnASlowClient(t *testing.T) {
	s := New(nil)
	ch := make(chan Event) // unbuffered, nobody reading
	s.clients[ch] = struct{}{}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.Publish(Event{Kind: "text", Text: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a client that was not reading")
	}
}

func TestTokenIsNotTrivial(t *testing.T) {
	a, b := New(nil), New(nil)
	if a.Token == b.Token {
		t.Error("two servers produced the same token")
	}
	if len(a.Token) < 32 {
		t.Errorf("token is only %d chars", len(a.Token))
	}
}
