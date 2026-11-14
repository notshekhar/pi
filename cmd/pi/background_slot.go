package main

import (
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/tui"
)

// Opening a stored session in a background slot (loop's background-slot.ts):
// a live session you are not looking at, whose turns run exactly as a typed
// turn would and which Ctrl+S lists beside the others. Thread teams
// (team_runtime.go) and `/btw` side threads (btw.go) both run their
// sessions this way.

// backgroundRunChoices is how the slot's turns run; anything left empty
// follows the foreground slot.
type backgroundRunChoices struct {
	model    string
	thinking provider.ReasoningEffort
	// agent is applied when useAgent is set — "" is the default persona.
	agent    string
	useAgent bool
}

// slotOpening is how a session's slot is opened when it has none.
type slotOpening struct {
	choices backgroundRunChoices
	// banner, if any, is drawn above its replayed transcript — what this
	// session is, for when you switch to it.
	banner func(p *repl)
}

// slotFor is the live slot for sessionID, else one opened in the
// background as opening says (loop's createSlotOpener). Opens are
// serialised, so two at the same moment share one slot: two Session objects
// for one conversation would each append to their own branch.
func (t *repl) slotFor(sessionID string, opening slotOpening) (*slot, error) {
	t.openingSlot.Lock()
	defer t.openingSlot.Unlock()
	if s := t.slots.bySessionID(sessionID); s != nil {
		return s, nil
	}
	return t.openBackgroundSlot(sessionID, opening)
}

// openBackgroundSlot loads sessionID into a new background slot.
func (t *repl) openBackgroundSlot(sessionID string, opening slotOpening) (*slot, error) {
	sess, err := session.Load(sessionID)
	if err != nil {
		return nil, err
	}
	choices := opening.choices
	run := t.slots.foreground().run.Fork()
	run.Session = sess
	model := choices.model
	if model == "" {
		model = sess.LastModel()
	}
	if model == "" {
		model = sess.Info.Model
	}
	run.Config.Provider, run.Config.ModelID = config.SplitModel(model)
	run.Config.CWD = sess.Info.CWD
	run.Tools.CWD = sess.Info.CWD
	if choices.thinking != "" {
		run.Config.Reasoning = choices.thinking
	}
	if choices.useAgent {
		run.UseAgent(choices.agent)
	}
	s := &slot{cfg: run.Config, run: run, agent: run.Agent, conv: tui.NewConversation(run.Config.FullID()), firstPrompt: sess.Name()}
	t.slots.add(s)
	t.wireRun(s)
	p := &repl{shared: t.shared, slot: s}
	// Drawn before anything can run in the slot: a replay that landed after
	// a turn had started would draw that turn's saved steps over its live
	// ones.
	drawn := make(chan struct{})
	p.do(func() {
		if opening.banner != nil {
			opening.banner(p)
		}
		p.replayTranscript(sess)
		close(drawn)
	})
	<-drawn
	t.refreshSessionsChip()
	return s, nil
}
