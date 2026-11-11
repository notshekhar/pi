package teams

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/session"
)

// Who runs a team's turns, and how the rest of the process hears about them.
//
// A member's turns run in whatever process hosts the team — the TUI (each
// member a background session you can switch to) or `pi serve`/`pi rpc`.
// That process registers a Runtime; the team tools only ever talk to it.
//
// Delivery is the one rule that matters: mail to an idle thread starts a
// turn in it; mail to a thread mid-turn waits in its inbox and is handed to
// the model before its next step, or — if the turn ends first — starts the
// next turn when the host calls WakeInbox on the way out.

// Peer is one teammate as a card shows them.
type Peer struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// MailView is a team message as it reaches a thread.
type MailView struct {
	ID        int64  `json:"id"`
	From      Peer   `json:"from"`
	Kind      Kind   `json:"kind"`
	Text      string `json:"text"`
	TS        int64  `json:"ts"`
	Broadcast bool   `json:"broadcast,omitempty"`
}

// TurnMeta is why a turn the team started is running. It rides on the
// turn's opening user entry (loop's `team` key), so a client draws a card
// instead of a user bubble the user never typed.
type TurnMeta struct {
	// Kind is "spawn" (a member's brief) or "mail".
	Kind   string `json:"kind"`
	TeamID string `json:"teamId"`
	// From is, for a spawn, the lead that started the member.
	From *Peer `json:"from,omitempty"`
	// Title is, for a spawn, the thread's own title.
	Title string `json:"title,omitempty"`
	// Agent is, for a spawn, the named agent the lead chose.
	Agent string     `json:"agent,omitempty"`
	Mail  []MailView `json:"mail,omitempty"`
	// MidTurn is set on mail delivered between two steps of a running turn.
	MidTurn bool `json:"midTurn,omitempty"`
}

// MailIDs is the mail a team turn carries, to put back if it never runs.
func (m TurnMeta) MailIDs() []int64 {
	ids := make([]int64, len(m.Mail))
	for i, v := range m.Mail {
		ids[i] = v.ID
	}
	return ids
}

// Runtime runs a team's turns in this process.
type Runtime interface {
	// Deliver runs input as a turn in the session now. Only called while it
	// is idle.
	Deliver(sessionID, input string, meta TurnMeta) error
	IsRunning(sessionID string) bool
	// Cancel stops the turn running in the session, if any.
	Cancel(sessionID string)
}

var (
	rtMu    sync.Mutex
	rt      Runtime
	rtOwner any
)

// ClaimRuntime registers the process's runtime. The first claim wins: the
// TUI claims at startup, and a server it embeds must not take it over.
func ClaimRuntime(owner any, r Runtime) bool {
	rtMu.Lock()
	defer rtMu.Unlock()
	if rt != nil {
		return false
	}
	rt, rtOwner = r, owner
	return true
}

// ReleaseRuntime gives the claim back, if owner holds it.
func ReleaseRuntime(owner any) {
	rtMu.Lock()
	defer rtMu.Unlock()
	if rtOwner == owner {
		rt, rtOwner = nil, nil
	}
}

func runtime() Runtime {
	rtMu.Lock()
	defer rtMu.Unlock()
	return rt
}

// HasRuntime is whether this process can run threads.
func HasRuntime() bool { return runtime() != nil }

// ─── change notices ─────────────────────────────────────────────────────

// ChangeKind says what about a team changed.
type ChangeKind string

const (
	ChangeMembers  ChangeKind = "members"
	ChangeActivity ChangeKind = "activity"
	ChangeCost     ChangeKind = "cost"
	ChangeBoard    ChangeKind = "board"
	ChangeBudget   ChangeKind = "budget"
)

// Change is something about a team that changed.
type Change struct {
	TeamID    string
	Kind      ChangeKind
	SessionID string
}

var (
	listenMu  sync.Mutex
	listeners = map[int]func(Change){}
	nextLis   int
	chatty    = map[string]*Change{}
)

// chattyInterval is how often activity and cost changes may reach
// listeners, per team: they move with every tool call and step.
const chattyInterval = time.Second

// OnChange registers a listener; the returned func removes it.
func OnChange(f func(Change)) func() {
	listenMu.Lock()
	defer listenMu.Unlock()
	id := nextLis
	nextLis++
	listeners[id] = f
	return func() {
		listenMu.Lock()
		delete(listeners, id)
		listenMu.Unlock()
	}
}

func hasListeners() bool {
	listenMu.Lock()
	defer listenMu.Unlock()
	return len(listeners) > 0
}

func broadcast(c Change) {
	listenMu.Lock()
	fs := make([]func(Change), 0, len(listeners))
	for _, f := range listeners {
		fs = append(fs, f)
	}
	listenMu.Unlock()
	for _, f := range fs {
		f(c)
	}
}

// Emit tells listeners about a change. Activity and cost are throttled to
// one per second per team, carrying the latest.
func Emit(c Change) {
	if c.Kind != ChangeActivity && c.Kind != ChangeCost {
		broadcast(c)
		return
	}
	key := c.TeamID + ":" + string(c.Kind)
	listenMu.Lock()
	_, pending := chatty[key]
	chatty[key] = &c
	listenMu.Unlock()
	if pending {
		return
	}
	time.AfterFunc(chattyInterval, func() {
		listenMu.Lock()
		latest := chatty[key]
		delete(chatty, key)
		listenMu.Unlock()
		if latest != nil {
			broadcast(*latest)
		}
	})
}

// LedgerRowBilled is told about every bill: a member's spend is summed when
// asked, so its team must hear there is something new, and the budget is
// checked within one step of being crossed.
func LedgerRowBilled(sessionPub string) {
	team, ok := TeamOf(sessionPub)
	if !ok {
		return
	}
	if hasListeners() {
		Emit(Change{TeamID: team.TeamID, Kind: ChangeCost, SessionID: sessionPub})
	}
	if BudgetUSD() > 0 && !IsStopped(team.TeamID) && IsOverBudget(team.TeamID) {
		Stop(team.TeamID, StopBudget)
	}
}

// ─── mail ───────────────────────────────────────────────────────────────

// MailViews is stored mail as a thread is shown it.
func MailViews(messages []Message) []MailView {
	var out []MailView
	for _, m := range messages {
		if m.Kind == KindBoard {
			continue
		}
		out = append(out, MailView{ID: m.ID, From: Peer{ID: m.From, Title: SessionTitle(m.From)}, Kind: m.Kind, Text: m.Body, TS: m.TS})
	}
	return out
}

// FormatMail is the mail as the model reads it: one tagged block each.
func FormatMail(mail []MailView) string {
	blocks := make([]string, len(mail))
	for i, m := range mail {
		tag := "team-message"
		switch m.Kind {
		case KindReport:
			tag = "team-report"
		case KindUpdate:
			tag = "team-update"
		}
		blocks[i] = fmt.Sprintf("<%s from=\"%s\" id=\"%s\">\n%s\n</%s>", tag, escapeAttr(m.From.Title), m.From.ID,
			escapeTeamTags(strings.TrimSpace(m.Text)), tag)
	}
	return strings.Join(blocks, "\n\n")
}

var reTeamTag = regexp.MustCompile(`(?i)<(/?)(team-)`)

// escapeTeamTags stops a body that writes `</team-message>` from closing its
// own block early, so what follows can't pass as someone else's message.
func escapeTeamTags(text string) string { return reTeamTag.ReplaceAllString(text, "&lt;$1$2") }

func escapeAttr(v string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;").Replace(v)
}

// ─── who is busy ────────────────────────────────────────────────────────

var (
	busyMu sync.Mutex
	// waiters are the wake funcs of turns inside wait_for_team, by session.
	waiters = map[string]map[int]func(){}
	nextWtr int
	// active is sessions with a team turn going in this process, whoever
	// started it: the runtime only knows the turns IT started, and a lead's
	// turn the user typed is just as busy.
	active = map[string]bool{}
	// waiting is sessions whose turn sits in wait_for_team (it takes mail
	// itself there).
	waiting = map[string]bool{}
	// delivering is sessions handed to the runtime whose turn hasn't begun:
	// until it runs nobody reports it busy, and a second message in the gap
	// would start a second turn beside the first.
	delivering = map[string]bool{}
)

// Nudge wakes a session's waiters; "" wakes everyone's.
func Nudge(sessionID string) {
	busyMu.Lock()
	var wake []func()
	for id, set := range waiters {
		if sessionID == "" || id == sessionID {
			for _, f := range set {
				wake = append(wake, f)
			}
		}
	}
	busyMu.Unlock()
	for _, f := range wake {
		f()
	}
}

func addWaiter(sessionID string, wake func()) func() {
	busyMu.Lock()
	defer busyMu.Unlock()
	set := waiters[sessionID]
	if set == nil {
		set = map[int]func(){}
		waiters[sessionID] = set
	}
	id := nextWtr
	nextWtr++
	set[id] = wake
	return func() {
		busyMu.Lock()
		defer busyMu.Unlock()
		delete(set, id)
		if len(set) == 0 {
			delete(waiters, sessionID)
		}
	}
}

func mark(set map[string]bool, sessionID string, on bool) {
	busyMu.Lock()
	defer busyMu.Unlock()
	if on {
		set[sessionID] = true
	} else {
		delete(set, sessionID)
	}
}

func marked(set map[string]bool, sessionID string) bool {
	busyMu.Lock()
	defer busyMu.Unlock()
	return set[sessionID]
}

// MarkTurnActive records a team turn starting or ending in this process.
func MarkTurnActive(sessionID string, on bool) { mark(active, sessionID, on) }

// IsTurnActive is whether a turn is going in the session in this process.
func IsTurnActive(sessionID string) bool { return marked(active, sessionID) }

// IsWaiting is whether the session's turn sits in wait_for_team.
func IsWaiting(sessionID string) bool { return marked(waiting, sessionID) }

// IsBusyHere is whether a turn is running, starting or being handed over in
// the session, in this process.
func IsBusyHere(sessionID string) bool {
	if marked(delivering, sessionID) || marked(active, sessionID) {
		return true
	}
	r := runtime()
	return r != nil && r.IsRunning(sessionID)
}

// MayRunTurn is whether a queued team turn may still run: one queued behind
// a busy thread can outlive the team's stop, or the thread's membership.
func MayRunTurn(sessionID string) bool {
	team, ok := TeamOf(sessionID)
	return ok && !IsStopped(team.TeamID)
}

// DropTurn is a queued team turn that will never run: its mail goes back
// for whatever wakes the thread next.
func DropTurn(meta TurnMeta) { _ = ReturnToInbox(meta.MailIDs()) }

var errNoRuntime = errors.New("thread teams are not available in this process")

// DeliverTurn gives the runtime a turn to run, counting the session busy
// until the runtime has it going.
func DeliverTurn(sessionID, input string, meta TurnMeta) error {
	r := runtime()
	if r == nil {
		return errNoRuntime
	}
	mark(delivering, sessionID, true)
	defer mark(delivering, sessionID, false)
	return r.Deliver(sessionID, input, meta)
}

// WakeInbox hands a thread its mail if it can take it now: an idle thread
// gets a turn with the mail as its prompt. A busy one is left alone — it
// reads its inbox before its next step, in wait_for_team, or here again once
// its turn ends (every runtime calls this as a turn settles).
func WakeInbox(sessionID string) bool {
	if runtime() == nil || !MayRunTurn(sessionID) {
		return false
	}
	if IsBusyHere(sessionID) {
		Nudge(sessionID)
		return false
	}
	if InboxCount(sessionID) == 0 {
		return false
	}
	taken, err := TakeInbox(sessionID)
	mail := MailViews(taken)
	if err != nil || len(mail) == 0 {
		return false
	}
	team, _ := TeamOf(sessionID)
	meta := TurnMeta{Kind: "mail", TeamID: team.TeamID, Mail: mail}
	// Marked busy before the goroutine starts, so a second wake in between
	// can't start a turn beside this one.
	mark(delivering, sessionID, true)
	go func() {
		defer mark(delivering, sessionID, false)
		if r := runtime(); r == nil || r.Deliver(sessionID, FormatMail(mail), meta) != nil {
			DropTurn(meta)
		}
	}()
	return true
}

func init() { session.OnLedgerRow(LedgerRowBilled) }
