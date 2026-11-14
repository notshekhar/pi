package rpc

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
)

// anthropicReply is one streamed Anthropic text reply.
func anthropicReply(text string) string {
	quoted, _ := json.Marshal(text, jsonopt.Write)
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(quoted) + `}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
}

// fakeModel answers each request with reply(body), and remembers the bodies.
type fakeModel struct {
	mu     sync.Mutex
	bodies []string
	reply  func(body string) string
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.bodies = append(f.bodies, string(raw))
	reply := f.reply
	f.mu.Unlock()
	text := reply(string(raw))
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, anthropicReply(text))
}

func (f *fakeModel) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.bodies...)
}

func (f *fakeModel) answer(reply func(string) string) {
	f.mu.Lock()
	f.reply = reply
	f.mu.Unlock()
}

// eventLog is a transport that keeps every session.event it is sent.
type eventLog struct {
	mu     sync.Mutex
	events []map[string]any
}

func (l *eventLog) Send(msg any) {
	n, ok := msg.(Notification)
	if !ok || n.Method != "session.event" {
		return
	}
	params, _ := n.Params.(map[string]any)
	l.mu.Lock()
	l.events = append(l.events, params)
	l.mu.Unlock()
}

// finishes counts the finish events a session has streamed.
func (l *eventLog) finishes(sessionID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		part, _ := e["part"].(map[string]any)
		if e["sessionId"] == sessionID && part["type"] == "finish" {
			n++
		}
	}
	return n
}

func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// btwServer is an RPC server whose turns go to a fake Anthropic gateway
// ("gw"), in a scratch home and database.
func btwServer(t *testing.T, model *fakeModel) *Server {
	t.Helper()
	srv := httptest.NewServer(model)
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	db.SetPath(filepath.Join(home, ".loop", "agent.db"))
	t.Cleanup(func() { db.Close(); db.SetPath("") })
	auth := map[string]any{"providers": map[string]any{}, "customProviders": map[string]any{
		"gw": map[string]any{"sdk": "anthropic", "baseURL": srv.URL, "apiKey": "k", "models": []map[string]any{{"id": "claude-x", "contextWindow": 100000}}},
	}}
	raw, _ := json.Marshal(auth, jsonopt.Write)
	if err := os.MkdirAll(filepath.Join(home, ".loop"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".loop", "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	newRun := func(s *session.Session, modelID string, ask tools.Asker) (*agent.Run, error) {
		prov, id := config.SplitModel(modelID)
		cfg := config.Config{Provider: prov, ModelID: id, CWD: s.Info.CWD}
		return &agent.Run{Config: cfg, Session: s, Tools: &tools.Context{CWD: cfg.CWD, Registry: tools.NewRegistry(), Todos: &tools.TodoList{}, Ask: ask}}, nil
	}
	s := NewServer(Host{NewRun: newRun})
	t.Cleanup(s.Dispose)
	return s
}

// listedBtwParents is session.list's `btw.parentId` by session id.
func listedBtwParents(t *testing.T, s *Server) map[string]string {
	t.Helper()
	got, err := s.Call("session.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	parents := map[string]string{}
	for _, row := range got.([]map[string]any) {
		if btw, ok := row["btw"].(map[string]any); ok {
			parents[row["id"].(string)] = btw["parentId"].(string)
		}
	}
	return parents
}

func historyJSON(t *testing.T, s *Server, sessionID string) string {
	t.Helper()
	got, err := s.Call("session.history", map[string]any{"sessionId": sessionID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got.(map[string]any)["entries"], jsonopt.Write)
	return string(raw)
}

// session.btw answers in a fresh thread while the source's turn keeps
// running; the thread never sees the source, the source never sees the
// thread, and the list says where the thread came from.
func TestSessionBtwAnswersInAFreshThreadWhileTheSourceTurnKeepsRunning(t *testing.T) {
	model := &fakeModel{reply: func(string) string { return "first answer" }}
	s := btwServer(t, model)
	log := &eventLog{}
	created, err := s.dispatch(Request{Method: "session.create", Params: map[string]any{"cwd": t.TempDir(), "provider": "gw", "model": "gw/claude-x"}}, log)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := created.(map[string]any)["sessionId"].(string)
	if _, err := s.Call("session.send", map[string]any{"sessionId": sourceID, "input": "the main task"}); err != nil {
		t.Fatal(err)
	}
	until(t, "the first turn", func() bool { return log.finishes(sourceID) == 1 })

	// A slow second turn, so the question lands while it is still running.
	// Released at cleanup too, so a failing run cannot hang the server's
	// Close behind a held request.
	held := make(chan struct{})
	release := sync.OnceFunc(func() { close(held) })
	t.Cleanup(release)
	model.answer(func(body string) string {
		if strings.Contains(body, "what is this repo?") {
			return "side answer"
		}
		<-held
		return "main answer"
	})
	if _, err := s.Call("session.send", map[string]any{"sessionId": sourceID, "input": "keep working"}); err != nil {
		t.Fatal(err)
	}
	asked, err := s.dispatch(Request{Method: "session.btw", Params: map[string]any{"sessionId": sourceID, "question": "what is this repo?"}}, log)
	if err != nil {
		t.Fatal(err)
	}
	result := asked.(map[string]any)
	threadID := result["sessionId"].(string)
	if threadID == sourceID || result["name"] != "btw: what is this repo?" {
		t.Fatalf("session.btw = %v", result)
	}

	until(t, "the side answer", func() bool { return log.finishes(threadID) == 1 })
	if log.finishes(sourceID) != 1 {
		t.Fatal("the side question waited for the source's turn")
	}
	release()
	until(t, "the source's second turn", func() bool { return log.finishes(sourceID) == 2 })

	var side string
	for _, body := range model.requests() {
		if strings.Contains(body, "what is this repo?") {
			side = body
		}
	}
	if side == "" || strings.Contains(side, "the main task") {
		t.Errorf("the side request was not the question alone: %s", side)
	}
	thread := historyJSON(t, s, threadID)
	if !strings.Contains(thread, "side answer") || !strings.Contains(thread, `"parentId":"`+sourceID+`"`) {
		t.Errorf("thread history = %s", thread)
	}
	parents := listedBtwParents(t, s)
	if parents[threadID] != sourceID {
		t.Errorf("the thread's list row says it came from %q, want %s", parents[threadID], sourceID)
	}
	if parent, listed := parents[sourceID]; listed {
		t.Errorf("the source's list row claims a btw parent %q", parent)
	}
	source := historyJSON(t, s, sourceID)
	if strings.Contains(source, "what is this repo?") || strings.Contains(source, "side answer") {
		t.Errorf("the source saw the side thread: %s", source)
	}
}

func TestSessionBtwRefusesAnEmptyQuestion(t *testing.T) {
	s := btwServer(t, &fakeModel{reply: func(string) string { return "" }})
	created, err := s.Call("session.create", map[string]any{"cwd": t.TempDir(), "provider": "gw", "model": "gw/claude-x"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Call("session.btw", map[string]any{"sessionId": created.(map[string]any)["sessionId"], "question": " "})
	if err == nil || !strings.Contains(err.Error(), "question") {
		t.Fatalf("err = %v, want the empty-question refusal", err)
	}
}
