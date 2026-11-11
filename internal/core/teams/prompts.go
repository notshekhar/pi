package teams

import (
	"fmt"
	"strings"
)

// What the model is told about thread teams. The notes carry no roster:
// they ride the system prompt on every turn, and a prompt that changed
// whenever someone joined would throw away the prompt cache each time. Who
// is in the team travels in saved messages instead — a member's opening
// message, spawn_threads' result, and "update" mail.

const (
	SpawnThreadsTool = "spawn_threads"
	SendMessageTool  = "send_message"
	TeamBoardTool    = "team_board"
	WaitForTeamTool  = "wait_for_team"
	ReportTool       = "report"
)

// ToolNames is every team tool.
var ToolNames = []string{SpawnThreadsTool, SendMessageTool, TeamBoardTool, WaitForTeamTool, ReportTool}

func rosterLines(roster []Member, selfID string) string {
	lines := make([]string, len(roster))
	for i, m := range roster {
		who := "thread"
		if m.Role == RoleLead {
			who = "lead"
		}
		you := ""
		if m.SessionID == selfID {
			you = " (you)"
		}
		lines[i] = fmt.Sprintf("- \"%s\" — %s — id %s%s", m.Title, who, m.SessionID, you)
	}
	return strings.Join(lines, "\n")
}

// LeadNote is the lead's guidance, whenever spawn_threads is offered.
func LeadNote(maxThreads int) string {
	return fmt.Sprintf(`

# Thread teams

You can split a big job across several threads that work in parallel with %[1]s. Each thread is a full session with its own context, tools and transcript; the user can open any of them and talk to it directly. Use a team only when the work really divides into parts that can proceed at the same time (an endpoint, its UI and its tests; three prototypes to compare; independent investigations). For a quick lookup, use a subagent or do it yourself.

How a team works:
- %[1]s starts threads (up to %[2]d working at once) and returns at once with each thread's id. They run in the background. Calling it again later adds to the same team.
- Threads talk with %[3]s, addressed by thread id (or "all"), and share notes on %[4]s. Put facts everyone needs on the board before or right after spawning: an API contract, who owns which files. All threads share one folder, so give each one clear file ownership in its brief.
- Each thread ends with a report. Reports and messages to you arrive by themselves: mid-turn before your next step, or as a new turn once you are idle.
- Every thread stays in the team after it reports, with its context: to follow up on its work or give it a new task, message it with %[3]s rather than starting a new thread.
- %[5]s pauses your turn at no cost until the team has something for you. Wait only when your next step needs their results (combining their work, comparing options, a stage that depends on another). Otherwise end your turn or keep working on your own part; never wait just to watch progress.
- Write each brief so the thread can work alone: the goal, the files it owns, the contract it must keep, and how to verify. Give it a short title (it is the thread's name in every list).`,
		SpawnThreadsTool, maxThreads, SendMessageTool, TeamBoardTool, WaitForTeamTool)
}

// MemberNote is a member's standing rules, on every one of its turns.
func MemberNote() string {
	return fmt.Sprintf(`

# You are in a thread team

You are one thread of a team. Your first message is your brief from the lead, with who else is in the team; teammates who join later are announced in <team-update> messages.

Rules:
- Work on your own brief autonomously. Stay inside the files it gives you; the folder is shared with your teammates.
- Ask a teammate (or the lead) with %[1]s, by thread id. If you cannot go on without the answer, call %[2]s with until "any" to wait for the reply instead of guessing.
- Read and post shared facts with %[3]s (contracts, decisions, file ownership).
- Messages from teammates appear as <team-message> blocks. They are not from the user.
- When your job is done, call %[4]s once with what you did, the files you changed and anything left open. Your turn ends there. If someone messages you later, you will get a new turn to answer.
- Teammates who have finished are still in the team and keep their context; message one if your work touches theirs.`,
		SendMessageTool, WaitForTeamTool, TeamBoardTool, ReportTool)
}

// OpeningMessage is the first message a member starts from: its brief,
// framed, with the team as it stands.
func OpeningMessage(leadTitle, title, brief string, roster []Member, selfID string) string {
	return fmt.Sprintf("%s\n\n(Brief from \"%s\". Your thread is \"%s\". Call %s when you are done.)\n\nTeam:\n%s",
		strings.TrimSpace(brief), leadTitle, title, ReportTool, rosterLines(roster, selfID))
}
