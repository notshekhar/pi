package rpc

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"crypto/rand"
	"encoding/hex"

	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
)

// ─── param helpers ───────────────────────────────────────────────────────

func pstr(p map[string]any, key string) string {
	switch v := p[key].(type) {
	case string:
		return v
	case nil:
		return ""
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprint(int64(v))
		}
		return fmt.Sprint(v)
	default:
		return fmt.Sprint(v)
	}
}

func pnum(p map[string]any, key string) (float64, bool) {
	v, ok := p[key].(float64)
	return v, ok
}

func pbool(p map[string]any, key string) (bool, bool) {
	v, ok := p[key].(bool)
	return v, ok
}

// providerOfModel is the provider half of "provider/model", "" without one.
func providerOfModel(modelID string) string {
	if strings.HasPrefix(modelID, config.CustomPrefix) {
		if i := strings.IndexByte(modelID[len(config.CustomPrefix):], '/'); i >= 0 {
			return modelID[:len(config.CustomPrefix)+i]
		}
		return ""
	}
	p, _, ok := strings.Cut(modelID, "/")
	if !ok {
		return ""
	}
	return p
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// infoJSON is loop's SessionInfoData.
func infoJSON(info session.Info) map[string]any {
	out := map[string]any{"id": info.ID, "createdAt": info.CreatedAt, "cwd": info.CWD,
		"provider": info.Provider, "model": info.Model}
	if info.ParentSession != "" {
		out["parentSession"] = info.ParentSession
	}
	return out
}

func scopeOf(v any) session.Scope {
	switch v {
	case "all":
		return session.AllScopes
	case true, "true":
		return session.Archived
	}
	return session.Active
}

// ─── attachments ─────────────────────────────────────────────────────────

var attachmentExt = map[string]string{
	"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp", "image/bmp": "bmp",
	"application/pdf": "pdf",
}

const (
	maxAttachments     = 8
	maxAttachmentBytes = 10 * 1024 * 1024
)

// writeAttachmentPayloads persists base64 attachments to temp files and
// returns the [image:<path>] sentinels the input pipeline understands.
func writeAttachmentPayloads(raw any) string {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	var b strings.Builder
	for i, item := range list {
		if i >= maxAttachments {
			break
		}
		a, _ := item.(map[string]any)
		ext := attachmentExt[pstr(a, "mediaType")]
		data, ok := a["data"].(string)
		if ext == "" || !ok {
			continue
		}
		bytes, err := base64.StdEncoding.DecodeString(data)
		if err != nil || len(bytes) == 0 || len(bytes) > maxAttachmentBytes {
			continue
		}
		var rnd [8]byte
		_, _ = rand.Read(rnd[:])
		path := filepath.Join(os.TempDir(), "loop-attach-"+hex.EncodeToString(rnd[:])+"."+ext)
		if os.WriteFile(path, bytes, 0o600) != nil {
			continue
		}
		b.WriteString("\n[image:" + path + "]")
	}
	return b.String()
}

// ─── handlers ────────────────────────────────────────────────────────────

func (s *Server) sessionHandlers() map[string]handler {
	return map[string]handler{
		"session.create": func(p map[string]any, t Transport) (any, error) {
			cwd := orStr(pstr(p, "cwd"), s.host.CWD)
			prov := orStr(pstr(p, "provider"), orStr(config.ActiveProvider(), "xai"))
			model := pstr(p, "model")
			sess, err := session.CreateWith(prov, model, cwd)
			if err != nil {
				return nil, err
			}
			c := newActive(sess, model)
			c.subscribers[t] = true
			s.mu.Lock()
			s.sessions[sess.ID] = c
			s.mu.Unlock()
			s.announce(SessionStatus{SessionID: sess.ID, Change: "created"})
			return map[string]any{"sessionId": sess.ID}, nil
		},
		"session.answer": func(p map[string]any, _ Transport) (any, error) {
			askID := pstr(p, "askId")
			s.mu.Lock()
			ch, ok := s.pendingAsks[askID]
			s.mu.Unlock()
			if !ok {
				return map[string]any{"ok": false, "askId": askID}, nil
			}
			var answers []AskAnswer
			if list, ok := p["answers"].([]any); ok {
				for _, item := range list {
					m, _ := item.(map[string]any)
					a := AskAnswer{Answers: []string{}}
					if arr, ok := m["answers"].([]any); ok {
						for _, x := range arr {
							a.Answers = append(a.Answers, fmt.Sprint(x))
						}
					}
					a.Custom, _ = m["custom"].(bool)
					a.Declined, _ = m["declined"].(bool)
					a.Note, _ = m["note"].(string)
					answers = append(answers, a)
				}
			}
			select {
			case ch <- answers:
			default:
			}
			return map[string]any{"ok": true, "askId": askID}, nil
		},
		"session.list": func(p map[string]any, _ Transport) (any, error) {
			page := session.Page{Limit: -1}
			if n, ok := pnum(p, "limit"); ok {
				page.Limit = int(n)
			}
			if n, ok := pnum(p, "offset"); ok {
				page.Offset = int(n)
			}
			if ids, ok := p["ids"].([]any); ok {
				page.IDs = []string{}
				for _, id := range ids {
					page.IDs = append(page.IDs, fmt.Sprint(id))
				}
			}
			cwd, hasCwd := p["cwd"].(string)
			rows, err := session.Rows(cwd, hasCwd, scopeOf(p["archived"]), page)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				m := toMap(row)
				model := orStr(row.LastModel, row.Model)
				m["lastModel"] = model
				m["lastProvider"] = orStr(providerOfModel(model), row.Provider)
				running, attached := false, 0
				if c := s.lookup(row.ID); c != nil {
					c.mu.Lock()
					running, attached = c.running, len(c.subscribers)
					c.mu.Unlock()
				}
				m["running"], m["attached"] = running, attached
				out = append(out, m)
			}
			return out, nil
		},
		"session.projects": func(p map[string]any, _ Transport) (any, error) {
			scope := session.Active
			if p["archived"] == "all" {
				scope = session.AllScopes
			} else if p["archived"] == true {
				scope = session.Archived
			}
			return session.Folders(scope)
		},
		"session.history": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			c, err := s.ensureCtx(id)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			sess := c.session
			sess.RLock()
			defer sess.RUnlock()
			model := sess.LastModel()
			branch := sess.CurrentBranch()
			after, hasAfter := p["afterEntryId"].(string)
			at := -1
			if hasAfter {
				for i, e := range branch {
					if e.ID == after {
						at = i
						break
					}
				}
			}
			entries := branch
			if at >= 0 {
				entries = branch[at+1:]
			}
			if entries == nil {
				entries = []session.Entry{}
			}
			out := map[string]any{
				"sessionId": id, "info": infoJSON(sess.Info), "model": model,
				"provider": orStr(providerOfModel(model), sess.Info.Provider),
				"leafId":   nullable(sess.LeafID()), "entries": entries, "running": c.running,
			}
			if name := sess.Name(); name != "" {
				out["name"] = name
			}
			if at >= 0 {
				out["tail"] = map[string]any{"afterEntryId": after}
			}
			seq := c.seq
			if c.running && c.hasSaved {
				seq = c.savedSeq
			}
			out["seq"] = seq
			return out, nil
		},
		"session.messages": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			c, err := s.ensureCtx(id)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			sess := c.session
			sess.RLock()
			model := sess.LastModel()
			name := sess.Name()
			info := infoJSON(sess.Info)
			prov := orStr(providerOfModel(model), sess.Info.Provider)
			sess.RUnlock()
			out := map[string]any{"sessionId": id, "info": info, "model": model, "provider": prov,
				"messages": c.transcript.Messages, "todos": c.transcript.Todos, "running": c.running, "seq": c.seq}
			if name != "" {
				out["name"] = name
			}
			return snapshot(out), nil
		},
		"session.open": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			c, err := s.ensureCtx(id)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			return map[string]any{"sessionId": id, "info": infoJSON(c.session.Info), "running": c.running, "seq": c.seq}, nil
		},
		"session.attach": func(p map[string]any, t Transport) (any, error) {
			id := pstr(p, "sessionId")
			c, err := s.ensureCtx(id)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			c.subscribers[t] = true
			resync := false
			if n, ok := pnum(p, "afterSeq"); ok {
				after := int64(n)
				switch {
				case after < c.seq:
					oldest := c.seq + 1
					if len(c.ring) > 0 {
						oldest = c.ring[0].seq
					}
					if after < oldest-1 {
						resync = true
					} else {
						for _, e := range c.ring {
							if e.seq > after {
								t.Send(Notification{JSONRPC: "2.0", Method: "session.event",
									Params: map[string]any{"sessionId": id, "seq": e.seq, "part": e.part}})
							}
						}
					}
				case after > c.seq:
					resync = true
				}
			}
			return map[string]any{"ok": true, "seq": c.seq, "running": c.running, "resync": resync}, nil
		},
		"session.detach": func(p map[string]any, t Transport) (any, error) {
			if c := s.lookup(pstr(p, "sessionId")); c != nil {
				c.mu.Lock()
				delete(c.subscribers, t)
				c.mu.Unlock()
			}
			return map[string]any{"ok": true}, nil
		},
		"session.send": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			images, _ := p["images"].([]any)
			if strings.TrimSpace(pstr(p, "input")) == "" && len(images) == 0 {
				return nil, errors.New("session.send needs a message: `input` is empty and there are no images")
			}
			// Open in the TUI: it runs there, on screen, and streams to every
			// client through PublishLive; one already running queues it.
			if live := s.liveGet(id); live != nil {
				live.Send(pstr(p, "input") + writeAttachmentPayloads(p["images"]))
				return map[string]any{"ok": true}, nil
			}
			c, err := s.requireSession(id)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.running {
				return nil, fmt.Errorf("session %s already has a turn running (cancel it first)", id)
			}
			input := pstr(p, "input") + writeAttachmentPayloads(p["images"])
			modelID := orStr(pstr(p, "model"), c.modelID)
			thinking := pstr(p, "thinking")
			switch thinking {
			case "off", "minimal", "low", "medium", "high", "xhigh":
			default:
				thinking = ""
			}
			agentName := strings.TrimSpace(pstr(p, "agent"))
			if agentName != "" && !agentExists(agentName) {
				return nil, fmt.Errorf("Unknown agent: %s", agentName)
			}
			c.modelID = modelID
			if err := s.startTurn(id, c, input, modelID, thinking, agentName); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
		"session.rename": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			c, err := s.requireSession(id)
			if err != nil {
				return nil, err
			}
			name := strings.TrimSpace(pstr(p, "name"))
			c.mu.Lock()
			c.session.Lock()
			err = c.session.SetName(name)
			c.session.Unlock()
			c.mu.Unlock()
			if err != nil {
				return nil, err
			}
			s.announce(SessionStatus{SessionID: id, Change: "renamed"})
			return map[string]any{"ok": true, "name": name}, nil
		},
		"session.cancel": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			if live := s.liveGet(id); live != nil {
				live.Cancel()
				return map[string]any{"ok": true}, nil
			}
			c, err := s.requireSession(id)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			if c.cancel != nil {
				c.cancel()
			}
			c.mu.Unlock()
			return map[string]any{"ok": true}, nil
		},
		"session.tree": func(p map[string]any, _ Transport) (any, error) {
			c, err := s.ensureCtx(pstr(p, "sessionId"))
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			c.session.RLock()
			defer c.session.RUnlock()
			return c.session.BuildTreeView(), nil
		},
		"session.branch": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			c, err := s.requireSession(id)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.running {
				return nil, fmt.Errorf("session %s has a turn running (cancel it first)", id)
			}
			entryID := pstr(p, "entryId")
			c.session.Lock()
			if p["entryId"] == nil || entryID == "" {
				c.session.ResetLeaf()
			} else {
				if _, ok := c.session.Entry(entryID); !ok {
					c.session.Unlock()
					return nil, fmt.Errorf("Unknown entryId: %s", entryID)
				}
				c.session.Branch(entryID)
			}
			c.modelID = c.session.LastModel()
			leaf := c.session.LeafID()
			c.session.Unlock()
			c.transcript = transcriptOf(c.session, false)
			return map[string]any{"ok": true, "leafId": nullable(leaf), "model": c.modelID}, nil
		},
		"session.fork": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			c, err := s.ensureCtx(id)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			src := c.session
			src.RLock()
			defer src.RUnlock()
			position := "at"
			if p["position"] == "before" {
				position = "before"
			}
			entryID := orStr(pstr(p, "entryId"), src.LeafID())
			if entryID == "" {
				return nil, fmt.Errorf("session %s has nothing to fork from", id)
			}
			entry, ok := src.Entry(entryID)
			if !ok {
				return nil, fmt.Errorf("Unknown entryId: %s", entryID)
			}
			target := entryID
			if position == "before" {
				if entry.Type != session.TypeMessage || entry.Role != "user" {
					return nil, fmt.Errorf("Only a user message can be forked \"before\": %s", entryID)
				}
				target = entry.ParentID
			}
			var forked *session.Session
			if target != "" {
				forked, err = session.ForkAtEntry(src, target)
			} else {
				forked, err = session.CreateWith(src.Info.Provider, src.LastModel(), src.Info.CWD)
			}
			if err != nil {
				return nil, err
			}
			out := map[string]any{"sessionId": forked.ID, "cwd": forked.Info.CWD, "leafId": nullable(forked.LeafID())}
			if position == "before" && entry.Type == session.TypeMessage {
				out["text"] = session.StripHookContext(session.TextContent(entry.Content))
			}
			return out, nil
		},
		"session.archive": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			archived := p["archived"] != false
			ok, err := session.SetArchived(id, archived)
			if err != nil {
				return nil, err
			}
			if ok {
				s.announce(SessionStatus{SessionID: id, Change: "archived"})
			}
			return map[string]any{"ok": ok, "sessionId": id, "archived": ok && archived}, nil
		},
		"session.delete": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			if c := s.lookup(id); c != nil {
				c.mu.Lock()
				running := c.running
				if !running {
					if c.cancel != nil {
						c.cancel()
					}
					c.subscribers = map[Transport]bool{}
				}
				c.mu.Unlock()
				if running {
					return nil, fmt.Errorf("session %s has a turn running (cancel it first)", id)
				}
				s.mu.Lock()
				delete(s.sessions, id)
				s.mu.Unlock()
			}
			ok, err := session.Delete(id)
			if err != nil {
				return nil, err
			}
			if ok {
				s.announce(SessionStatus{SessionID: id, Change: "deleted"})
			}
			return map[string]any{"ok": ok, "sessionId": id}, nil
		},
		"session.compact": func(p map[string]any, _ Transport) (any, error) {
			id := pstr(p, "sessionId")
			c, err := s.requireSession(id)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			if c.running {
				c.mu.Unlock()
				return nil, fmt.Errorf("session %s already has a turn running (cancel it first)", id)
			}
			run, err := s.host.NewRun(c.session, c.modelID, nil)
			if err != nil {
				c.mu.Unlock()
				return nil, err
			}
			ctx, cancel := contextWithCancel()
			c.cancel = cancel
			s.setRunning(id, c, true)
			s.broadcast(id, c, "compact-start", map[string]any{"reason": "manual"})
			c.mu.Unlock()
			defer cancel()

			res, err := agent.Compact(ctx, run, agent.CompactOptions{Manual: true, Focus: strings.TrimSpace(pstr(p, "focus"))})
			c.mu.Lock()
			defer c.mu.Unlock()
			defer s.setRunning(id, c, false)
			if err != nil {
				end := map[string]any{"summary": "", "cutAt": 0, "tokensBefore": 0, "tokensAfter": 0}
				if errors.Is(err, errAborted(ctx)) {
					end["aborted"] = true
				} else {
					end["error"] = err.Error()
				}
				s.broadcast(id, c, "compact-end", end)
				return nil, err
			}
			result := map[string]any{"summary": res.Summary, "cutAt": res.CutAt, "tokensBefore": res.TokensBefore, "tokensAfter": res.TokensAfter}
			s.broadcast(id, c, "compact-end", result)
			c.transcript = transcriptOf(c.session, true)
			return result, nil
		},
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

var _ = time.Now
