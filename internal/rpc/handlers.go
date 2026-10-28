package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/agents"
	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
)

// ─── small helpers ───────────────────────────────────────────────────────

func toMap(v any) map[string]any {
	raw, _ := json.Marshal(v)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// snapshot freezes a value into JSON now, so it cannot change while the
// response is written.
func snapshot(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func errAborted(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return context.Canceled
}

func thinkingEffort(level string) provider.ReasoningEffort { return config.ThinkingToEffort(level) }

func agentExists(name string) bool { return agents.Exists(name) }

// applyAgent runs a turn under a named agent's prompt and tool set.
func applyAgent(run *agent.Run, name string) error {
	if !agents.Exists(name) {
		return fmt.Errorf("Unknown agent: %s", name)
	}
	if name != agents.DefaultAgentName {
		run.Persona = agents.Prompt(name)
	}
	run.AllowedTools = agents.Tools(name)
	if name == agents.PlanAgentName {
		run.Planning = true
	}
	return nil
}

// ─── teams ───────────────────────────────────────────────────────────────

func (s *Server) teamHandlers() map[string]handler {
	return map[string]handler{
		// pi runs no thread teams: there is never a team to show.
		"team.get": func(p map[string]any, _ Transport) (any, error) {
			if pstr(p, "teamId") == "" && pstr(p, "sessionId") == "" {
				return nil, errors.New("team.get needs a teamId or a sessionId")
			}
			return nil, nil
		},
		"team.stop": func(p map[string]any, _ Transport) (any, error) {
			return nil, errors.New("team.stop needs a teamId or a sessionId in a team")
		},
	}
}

// ─── agents ──────────────────────────────────────────────────────────────

func (s *Server) agentHandlers() map[string]handler {
	return map[string]handler{
		"agent.list": func(map[string]any, Transport) (any, error) {
			out := []map[string]any{}
			for _, a := range agents.List() {
				if a.Hidden {
					continue
				}
				row := map[string]any{"name": a.Name, "builtin": a.Builtin}
				if a.Model != "" {
					row["model"] = a.Model
				}
				if a.Tools != nil {
					row["tools"] = a.Tools
				}
				out = append(out, row)
			}
			return out, nil
		},
		"agent.get": func(p map[string]any, _ Transport) (any, error) {
			name := pstr(p, "name")
			if !agents.Exists(name) {
				return nil, fmt.Errorf("Unknown agent: %s", name)
			}
			builtin := agents.IsBuiltin(name)
			out := map[string]any{"name": name, "prompt": agents.Prompt(name), "builtin": builtin,
				"toolsEditable": !builtin, "hasOverride": agents.HasBuiltinOverride(name)}
			if t := agents.Tools(name); t != nil {
				out["tools"] = t
			}
			if m := agents.Model(name); m != "" {
				out["model"] = m
			}
			return out, nil
		},
		"agent.save": func(p map[string]any, _ Transport) (any, error) {
			name := strings.TrimSpace(pstr(p, "name"))
			if !agents.IsValidName(name) {
				return nil, fmt.Errorf("Invalid agent name: %s (alphanumeric and dashes, 32 chars max)", name)
			}
			prompt := pstr(p, "prompt")
			if strings.TrimSpace(prompt) == "" {
				return nil, errors.New("An agent needs a system prompt")
			}
			if !agents.Exists(name) && s.host.HasCommand != nil && s.host.HasCommand(name) {
				return nil, fmt.Errorf("%q is already a command", name)
			}
			var tools []string
			if list, ok := p["tools"].([]any); ok {
				tools = []string{}
				for _, t := range list {
					tools = append(tools, fmt.Sprint(t))
				}
			}
			if err := agents.Save(name, prompt, tools, pstr(p, "model")); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "name": name}, nil
		},
		"agent.delete": func(p map[string]any, _ Transport) (any, error) {
			ok, err := agents.Delete(pstr(p, "name"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": ok}, nil
		},
		"agent.tools": func(map[string]any, Transport) (any, error) {
			return agents.ValidToolNames(), nil
		},
	}
}

// ─── catalog, cost, git ──────────────────────────────────────────────────

func (s *Server) catalogHandlers() map[string]handler {
	return map[string]handler{
		"catalog.list": func(p map[string]any, _ Transport) (any, error) {
			want := pstr(p, "provider")
			out := []catalog.Model{}
			for _, m := range catalog.All() {
				if want == "" || m.Provider == want {
					out = append(out, m)
				}
			}
			return out, nil
		},
		"cost.session": func(p map[string]any, _ Transport) (any, error) {
			c, err := s.ensureCtx(pstr(p, "sessionId"))
			if err != nil {
				return nil, err
			}
			return c.tracker.SessionBreakdown(), nil
		},
		"cost.lifetime": func(map[string]any, Transport) (any, error) {
			return agent.NewCostTracker().LifetimeBreakdown(), nil
		},
		"cost.stats": func(p map[string]any, _ Transport) (any, error) {
			return agent.NewCostTracker().Stats(pstr(p, "cwd")), nil
		},
		"usage.steak": func(p map[string]any, _ Transport) (any, error) {
			daily, err := session.DailyTokens()
			if err != nil {
				return nil, err
			}
			year := 0
			if n, ok := pnum(p, "year"); ok && n == float64(int(n)) {
				year = int(n)
			}
			return agent.BuildSteakGrid(daily, year, time.Now()), nil
		},
		"git.commitMessage": func(p map[string]any, _ Transport) (any, error) {
			diff := pstr(p, "diff")
			if strings.TrimSpace(diff) == "" {
				return map[string]any{"message": ""}, nil
			}
			modelID := orStr(pstr(p, "model"), config.LoadSettings().DefaultModel)
			if modelID == "" {
				return nil, errors.New("no model configured to write a commit message")
			}
			msg, err := agent.GenerateCommitMessage(context.Background(), agent.CommitMessageOptions{
				Diff: diff, ModelID: modelID, Branch: pstr(p, "branch"), CWD: orStr(pstr(p, "cwd"), s.host.CWD),
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"message": msg}, nil
		},
	}
}

// ─── settings, context, reload, hello, info ─────────────────────────────

type webSetting struct {
	key, label, description string
	def                     bool
}

// webSettings is loop's WEB_SETTINGS allowlist.
var webSettings = []webSetting{
	{"subagents", "subagents", "task tool: delegate work to parallel subagents", true},
	{"memory", "memory", "agent saves per-project facts across sessions", true},
	{"skills", "skills", "let the agent load project skills", true},
	{"recap", "recap", "short AI recap under responses that changed files", false},
	{"webSearch", "websearch", "websearch tool (DuckDuckGo scrape, may rate-limit)", false},
	{"todos", "todos", "visible checklist during multi-step tasks", false},
	{"idleCompact", "idle compaction", "compact a chat over 70% full just before its prompt cache expires, while you are away", true},
	{"threadTeams", "thread teams", "let the agent split a job across threads that work in parallel and report back", false},
	{"backgroundShells", "background shells", "let bash start servers/watchers that keep running after the tool call", false},
	{"artifacts", "artifacts", "let the agent publish documents it writes as pages you can open", false},
	{"reminders", "reminders", "fire /reminder alerts", true},
	{"mcp", "mcp servers", "connect configured MCP servers and expose their tools", true},
	{"serve", "serve (web UI)", "allow loop serve — turning this off blocks the NEXT serve start", false},
}

func (s *Server) settingsHandlers() map[string]handler {
	return map[string]handler{
		"settings.list": func(map[string]any, Transport) (any, error) {
			store := config.SettingsStore()
			out := make([]map[string]any, 0, len(webSettings))
			for _, w := range webSettings {
				value := w.def
				var b bool
				if store.Get(w.key, &b) {
					value = b
				}
				out = append(out, map[string]any{"key": w.key, "label": w.label, "description": w.description, "value": value})
			}
			return out, nil
		},
		"settings.set": func(p map[string]any, _ Transport) (any, error) {
			key := pstr(p, "key")
			found := false
			for _, w := range webSettings {
				if w.key == key {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("setting not writable over RPC: %s", key)
			}
			value, ok := pbool(p, "value")
			if !ok {
				return nil, errors.New("value must be boolean")
			}
			if err := config.SetSetting(key, value); err != nil {
				return nil, err
			}
			return map[string]any{"key": key, "value": value}, nil
		},
		"context.report": func(p map[string]any, _ Transport) (any, error) {
			if id := pstr(p, "sessionId"); id != "" {
				c, err := s.ensureCtx(id)
				if err != nil {
					return nil, err
				}
				c.mu.Lock()
				sess, modelID := c.session, c.modelID
				c.mu.Unlock()
				return s.contextReport(sess, modelID, sess.Info.CWD)
			}
			modelID := orStr(pstr(p, "model"), config.LoadSettings().DefaultModel)
			return s.contextReport(nil, modelID, orStr(pstr(p, "cwd"), s.host.CWD))
		},
		"config.reload": func(p map[string]any, _ Transport) (any, error) {
			config.RefreshConfigStores()
			cwd := orStr(pstr(p, "cwd"), s.host.CWD)
			commands := 0
			if s.host.Reload != nil {
				commands = s.host.Reload(cwd)
			}
			catalog.Bust()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			available, total, _ := catalog.Refresh(ctx)
			if s.host.MCP != nil {
				s.host.MCP.Reconnect(ctx, cwd, "")
			}
			return map[string]any{"models": total, "availableModels": available, "commands": commands,
				"agents": len(agents.List()), "providers": len(usableProviders())}, nil
		},
		"hello": func(p map[string]any, _ Transport) (any, error) {
			client, ok := parseProtocol(p["protocol"])
			if !ok {
				client = [2]int{1, 0}
			}
			negotiated, err := negotiate(client)
			if err != nil {
				return nil, err
			}
			return map[string]any{"protocol": ProtocolVersion, "negotiated": negotiated,
				"minClientProtocol": MinClientProtocol, "host": "loop", "version": s.host.Version,
				"capabilities": ProtocolCapabilities}, nil
		},
		"server.info": func(map[string]any, Transport) (any, error) {
			st := config.LoadSettings()
			var model, prov any
			if st.DefaultModel != "" {
				model = st.DefaultModel
				if p := providerOfModel(st.DefaultModel); p != "" {
					prov = p
				}
			}
			thinking := st.ThinkingLevel
			if thinking == "" {
				thinking = "off"
			}
			cwd := s.host.CWD
			if cwd == "" {
				cwd, _ = os.Getwd()
			}
			return map[string]any{"protocol": "2.0", "methods": Methods,
				"events":   append(append([]string{}, agent.TurnEventNames...), "session-running"),
				"defaults": map[string]any{"model": model, "provider": prov, "cwd": cwd, "thinking": thinking}}, nil
		},
	}
}

// contextReport is loop's ContextReport shape.
func (s *Server) contextReport(sess *session.Session, modelID, cwd string) (any, error) {
	prov, model := config.SplitModel(modelID)
	window := 0
	if info, ok := catalog.Lookup(prov, model); ok {
		window = info.Context
	}
	cfg := config.Config{Provider: prov, ModelID: model, CWD: cwd}
	if sess == nil {
		sess = session.New(modelID, cwd)
	}
	run, err := s.host.NewRun(sess, modelID, nil)
	if err != nil {
		return nil, err
	}
	run.Config.CWD = cfg.CWD
	sess.RLock()
	r := agent.BuildContextReport(run, window)
	sess.RUnlock()
	keys := map[string]string{"System prompt": "systemPrompt", "Extension prompt": "extensionPrompt",
		"System tools": "systemTools", "MCP tools": "mcpTools", "Workspace context": "workspaceContext",
		"Memory": "memory", "Skills": "skills", "Messages": "messages", "Compact summary": "compactSummary"}
	cats := []map[string]any{}
	total := 0
	for _, c := range r.Categories {
		key := keys[c.Label]
		if key == "" {
			key = strings.ToLower(strings.ReplaceAll(c.Label, " ", ""))
		}
		cats = append(cats, map[string]any{"key": key, "label": c.Label, "tokens": c.Tokens})
		total += c.Tokens
	}
	skills := []map[string]any{}
	for _, sk := range r.Skills {
		skills = append(skills, map[string]any{"name": sk.Name, "tokens": sk.Tokens})
	}
	return map[string]any{"modelId": modelID, "contextWindow": window, "autoCompactThreshold": config.LoadSettings().AutoCompact(),
		"categories": cats, "totalTokens": total, "freeTokens": max(0, window-total), "skills": skills,
		"toolCount": len(run.ToolNames()), "mcpToolCount": len(run.Extra)}, nil
}
