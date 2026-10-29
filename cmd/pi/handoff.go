package main

import (
	"context"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/agents"
	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
	"github.com/notshekhar/pi/internal/tui"
)

// /handoff — loop's handoff-handlers.ts: write a brief of this session,
// review it, then continue in a fresh session (model and agent chosen) that
// opens with the brief and links back to this one.

const handoffContinue = "Continue from the handoff brief above. Verify the current workspace state, then work on the next unfinished step. Respect the constraints and verification requirements in the brief."

func (t *repl) handoffCmd(ctx context.Context, focus string) {
	if t.busy() {
		t.dim("Finish or cancel the current turn before creating a handoff.")
		return
	}
	go func() {
		if err := t.handoffFlow(ctx, strings.TrimSpace(focus)); err != nil {
			t.fail("%s", err)
		}
	}()
}

func (t *repl) handoffFlow(ctx context.Context, focus string) error {
	source := t.run.Session
	branch := source.CurrentBranch()
	has := false
	for _, e := range branch {
		if e.Type == session.TypeMessage {
			has = true
			break
		}
	}
	if !has {
		t.dim("Nothing to hand off yet — send a message first.")
		return nil
	}
	sourceModel := t.cfg.FullID()
	sourceAgent := t.agent
	if sourceAgent == "" {
		sourceAgent = agents.DefaultAgentName
	}
	sourcePlan := t.run.Planning
	cwd := t.cfg.CWD
	sourceLeaf := source.LeafID()

	t.do(t.app.LoaderStart)
	status := agent.HandoffGitStatus(cwd)
	brief, err := agent.GenerateHandoff(ctx, agent.DraftOptions{ModelID: sourceModel, CWD: cwd, SessionPub: source.ID,
		Tracker: agent.NewCostTracker()}, branch, focus, status)
	t.do(t.app.LoaderStop)
	if err != nil {
		return err
	}
	continueNow := false
	for {
		t.do(func() { t.app.PrintDraft("Handoff brief", brief) })
		review := t.choose("Review handoff",
			tui.Item{Value: "open", Label: "open a fresh session", Description: "choose a model and agent; start when you are ready"},
			tui.Item{Value: "continue", Label: "open and continue", Description: "choose a model and agent; start the next step immediately"},
			tui.Item{Value: "edit", Label: "edit brief", Description: "revise the summary and next steps"},
			tui.Item{Value: "cancel", Label: "cancel", Description: "stay in this session"})
		if review == nil || review.Value == "cancel" {
			return nil
		}
		if review.Value == "edit" {
			if edited := strings.TrimSpace(t.ask("Edit the handoff brief", brief)); edited != "" {
				brief = edited
			}
			continue
		}
		continueNow = review.Value == "continue"
		break
	}

	models := []tui.Item{{Value: sourceModel, Label: sourceModel + " (current)"}}
	var others []catalog.Model
	for _, m := range catalog.All() {
		if m.Available && m.ID != sourceModel {
			others = append(others, m)
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i].ID < others[j].ID })
	for _, m := range others {
		models = append(models, tui.Item{Value: m.ID, Label: m.ID, Description: m.Name})
	}
	model := t.app.Pick("Handoff model", models, 0, "")
	if model == nil {
		return nil
	}
	keep := "keep the current agent"
	if sourcePlan {
		keep = "keep plan mode (read-only)"
	}
	agentItems := []tui.Item{{Value: sourceAgent, Label: sourceAgent + " (current)", Description: keep}}
	for _, a := range agents.List() {
		if a.Name == sourceAgent || a.Hidden {
			continue
		}
		desc := "all tools"
		if a.Name == agents.PlanAgentName {
			desc = "read-only planning"
		} else if len(a.Tools) > 0 {
			desc = strings.Join(a.Tools, ", ")
		}
		agentItems = append(agentItems, tui.Item{Value: a.Name, Label: a.Name, Description: desc})
	}
	picked := t.choose("Handoff agent", agentItems...)
	if picked == nil {
		return nil
	}

	message, err := agent.HandoffMessage(source.ID, sourceLeaf, cwd, brief)
	if err != nil {
		return err
	}
	prov, _ := config.SplitModel(model.Value)
	dest, err := session.CreateHandoff(source, prov, model.Value, cwd, message)
	if err != nil {
		return err
	}
	t.run.Session = dest
	if n := dest.Name(); n != "" {
		t.setTabName(n)
	}
	t.run.Tools.Registry = tools.NewRegistry()
	t.run.Tools.Todos.Clear()
	t.lastContextTokens = 0
	t.cfg.Provider, t.cfg.ModelID = config.SplitModel(model.Value)
	t.apply()
	planMode := picked.Value == agents.PlanAgentName || (picked.Value == sourceAgent && sourcePlan)
	t.setAgent(picked.Value)
	t.run.Planning = planMode
	t.do(func() {
		t.app.Clear()
		t.app.SetSession(dest.ID)
		t.app.SetCostText("")
		t.app.SetPlanning(planMode)
		t.app.SetTodos(nil)
		t.app.ResetUsage(t.contextWindow())
		t.replayTranscript(dest)
	})
	next := "\nReady for your next prompt."
	if continueNow {
		next = ""
	}
	t.dim("Handoff opened: %s\nOriginal session: %s%s", dest.ID, source.ID, next)
	if continueNow {
		t.submit(ctx, handoffContinue)
	}
	return nil
}
