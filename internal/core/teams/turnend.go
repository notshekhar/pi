package teams

import "fmt"

// What happens in a team when a turn ends: the panels hear the state it
// left behind, and a lead whose thread stopped without reporting is told —
// it would otherwise wait for a report that is never coming.

// maxFailureChars is enough of an error for the lead to act on; the
// thread's transcript has the rest.
const maxFailureChars = 500

// TurnEnd is how a team turn ended.
type TurnEnd struct {
	StartedAt int64
	Aborted   bool
	// Failure is the error the turn ended on, or "".
	Failure string
	// Meta is why the team started the turn; nil when the user typed it.
	Meta *TurnMeta
}

// Settle records a turn's end. A report already said "done", and a stopped
// thread stays stopped: both are final.
func Settle(m Membership, end TurnEnd) {
	if m.State == StateDone || m.State == StateStopped {
		if m.Activity != "" {
			SetActivity(m.SessionID, "")
		}
		return
	}
	state := StateIdle
	if end.Failure != "" && !end.Aborted {
		state = StateFailed
	}
	Move(m.SessionID, state, "")
	if m.Role == RoleMember {
		tellLeadHowItEnded(m, end)
	}
}

// owesLeadAnAnswer: the lead gave this turn its work — the brief, or a
// message of its own. A turn a teammate or the user started is theirs.
func owesLeadAnAnswer(m Membership, meta *TurnMeta) bool {
	if meta == nil {
		return false
	}
	if meta.Kind == "spawn" {
		return true
	}
	for _, mail := range meta.Mail {
		if mail.From.ID == m.LeadID {
			return true
		}
	}
	return false
}

func howItEnded(end TurnEnd) string {
	switch {
	case end.Aborted:
		return "This thread was cancelled before it reported."
	case end.Failure != "":
		failure := end.Failure
		if r := []rune(failure); len(r) > maxFailureChars {
			failure = string(r[:maxFailureChars])
		}
		return fmt.Sprintf("This thread failed before it reported: %s", failure)
	}
	return "This thread ended its turn without a report. Message it to ask what happened."
}

func tellLeadHowItEnded(m Membership, end TurnEnd) {
	if !owesLeadAnAnswer(m, end.Meta) {
		return
	}
	// Answering the lead with send_message is an answer, if the turn then
	// ended cleanly.
	clean := end.Failure == "" && !end.Aborted
	if clean && HasMailedSince(m.SessionID, m.LeadID, end.StartedAt) {
		return
	}
	_, _ = PostMessages(m.TeamID, m.SessionID, []string{m.LeadID}, KindUpdate, howItEnded(end))
	WakeInbox(m.LeadID)
}
