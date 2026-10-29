package main

import (
	"context"
	"errors"

	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/tui"
)

// /tree — loop's session-tree-handlers.ts: the CURRENT session's entry tree,
// every branch of it. Pick any point to continue from there; the branch left
// behind can be summarised into a branch-summary entry so the model keeps
// what it explored. Labels mark points worth coming back to.

func toTreeItems(nodes []*session.TreeNode) []*tui.TreeItem {
	out := make([]*tui.TreeItem, 0, len(nodes))
	for _, n := range nodes {
		e := n.Entry
		it := &tui.TreeItem{ID: e.ID, ParentID: e.ParentID, Type: e.Type, Role: e.Role, Content: e.Content,
			Agent: e.Agent, Prompt: e.Prompt, Summary: e.Summary, To: e.To, TokensBefore: e.TokensBefore,
			Fresh: e.Rollover || e.Handoff != "", EntryLabel: e.Label, Label: n.Label, LabelTS: n.LabelTS,
			Children: toTreeItems(n.Children)}
		if e.Type == session.TypeMessage && e.Role == "user" {
			it.UserText = session.StripHookContext(session.TextContent(e.Content))
		}
		out = append(out, it)
	}
	return out
}

func (t *repl) showTree() {
	if t.busy() {
		t.dim("busy; finish or abort current turn first")
		return
	}
	go t.treeSelector("")
}

func (t *repl) treeSelector(initial string) {
	sess := t.run.Session
	roots := sess.GetTree()
	if len(roots) == 0 || !sess.Saved() {
		t.dim("No entries in session")
		return
	}
	leaf := sess.LeafID()
	var mode string
	config.SettingsStore().Get("treeFilterMode", &mode)
	sel := tui.NewTreeSelector(t.app.Theme(), toTreeItems(roots), leaf, t.app.Rows(), sess.Info.CWD, initial, tui.TreeFilter(mode))
	sel.OnLabel = func(id, label string) {
		sess.Lock()
		err := sess.SetLabel(id, label)
		sess.Unlock()
		if err != nil {
			t.fail("%s", err)
		}
	}
	id := t.app.ShowTree(sel)
	if id == "" {
		return
	}
	if id == leaf {
		t.dim("Already at this point")
		return
	}
	t.navigateTree(id, leaf)
}

// promptSummaryChoice asks how to summarise the branch being left; ok false
// is Esc (back to the tree).
func (t *repl) promptSummaryChoice() (summarize bool, custom string, ok bool) {
	var skip bool
	if config.SettingsStore().Get("branchSummarySkipPrompt", &skip) && skip {
		return false, "", true
	}
	for {
		choice := t.choose("Summarize branch?",
			tui.Item{Value: "none", Label: "No summary"},
			tui.Item{Value: "summarize", Label: "Summarize"},
			tui.Item{Value: "custom", Label: "Summarize with custom prompt"})
		if choice == nil {
			return false, "", false
		}
		if choice.Value == "custom" {
			instr := t.ask("Custom summarization instructions", "")
			if instr == "" {
				continue
			}
			return true, instr, true
		}
		return choice.Value != "none", "", true
	}
}

func (t *repl) navigateTree(id, realLeaf string) {
	summarize, custom, ok := t.promptSummaryChoice()
	if !ok {
		t.treeSelector(id)
		return
	}
	sess := t.run.Session
	target, found := sess.Entry(id)
	if !found {
		t.fail("Entry %s not found", id)
		return
	}
	// A user message: continue from before it, with its text back in the
	// composer to edit and resend.
	newLeaf, editorText := id, ""
	if target.Type == session.TypeMessage && target.Role == "user" {
		newLeaf = target.ParentID
		editorText = session.StripHookContext(session.TextContent(target.Content))
	}

	summarized := false
	if summarize {
		entries := agent.CollectEntriesForBranchSummary(sess, realLeaf, id)
		if len(entries) > 0 {
			ctx, cancel := context.WithCancel(context.Background())
			t.mu.Lock()
			t.turning, t.cancel = true, cancel
			t.mu.Unlock()
			t.do(func() { t.app.LoaderStart("Summarizing branch") })
			text, usage, err := t.run.RunBranchSummary(ctx, agent.BranchSummaryOptions{Entries: entries,
				CustomInstructions: custom, Tracker: t.tracker()})
			t.do(t.app.LoaderStop)
			t.mu.Lock()
			t.turning, t.cancel = false, nil
			t.mu.Unlock()
			cancel()
			if errors.Is(err, agent.ErrBranchSummaryAborted) {
				t.dim("Branch summarization cancelled")
				t.treeSelector(id)
				return
			}
			if err != nil {
				t.fail("%s", err)
				return
			}
			sess.Lock()
			_, err = sess.BranchWithSummary(newLeaf, text, usage, t.cfg.FullID())
			sess.Unlock()
			if err != nil {
				t.fail("%s", err)
				return
			}
			summarized = true
		}
	}
	if !summarized {
		sess.Lock()
		if newLeaf == "" {
			sess.ResetLeaf()
		} else if err := sess.Branch(newLeaf); err != nil {
			sess.Unlock()
			t.fail("%s", err)
			return
		}
		sess.Unlock()
	}
	cost := t.tracker().Format()
	ctxTokens := int(agent.NewCostTracker().SeedFromSession(sess))
	t.do(func() {
		t.app.SetContextTokens(ctxTokens)
		t.app.Clear()
		t.replayTranscript(sess)
		if editorText != "" {
			t.app.SetDraftIfEmpty(editorText)
		}
		t.app.SetCostText(cost)
	})
	t.dim("Navigated to selected point")
}
