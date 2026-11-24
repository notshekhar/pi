package chatimport

import (
	"regexp"
	"strings"
)

// What the PERSON typed, out of a user message an agent stored. Every agent
// writes more than the prompt into its user turns — reminders, environment
// dumps, rules — and imported as-is those turns would bury every prompt.
// loop's user-text.ts.

// injectedTags are blocks an agent mixes INTO a turn the person typed. A turn
// made of nothing but tag blocks is dropped by shape (onlyTagBlocks), so this
// list only needs the blocks that share a turn with real text.
var injectedTags = []string{
	"system-reminder", "system_reminder", "environment_context", "user_info",
	"timestamp", "git_status", "attached_files", "open_and_recently_viewed_files",
}

// echoPrefixes mark a turn that is a tool's echo, never a prompt.
var echoPrefixes = []string{"<command-name>", "<command-message>", "<local-command-", "Caveat: The messages below"}

var (
	agentsDump = regexp.MustCompile(`^# AGENTS\.md instructions for `)
	userQuery  = regexp.MustCompile(`(?s)<user_query>(.*?)</user_query>`)
	openTag    = regexp.MustCompile(`^<([A-Za-z_][\w -]*?)(?:\s[^>]*)?>`)
)

// UserPrompt is the prompt inside an agent's user turn, or "" when the turn
// holds nothing the user wrote.
func UserPrompt(raw string) string {
	text := strings.TrimSpace(raw)
	for _, p := range echoPrefixes {
		if strings.HasPrefix(text, p) {
			return ""
		}
	}
	if agentsDump.MatchString(text) {
		return ""
	}
	// Cursor wraps the prompt itself; what is outside the wrapper is context.
	if m := userQuery.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	if onlyTagBlocks(text) {
		return ""
	}
	for _, tag := range injectedTags {
		text = removeBlocks(text, tag)
	}
	return strings.TrimSpace(text)
}

// leadingBlock is the length of one whole `<tag …>…</tag>` block at the start
// of text, or 0 when it does not start with one.
func leadingBlock(text string) int {
	m := openTag.FindStringSubmatchIndex(text)
	if m == nil {
		return 0
	}
	closing := "</" + text[m[2]:m[3]] + ">"
	end := strings.Index(text[m[1]:], closing)
	if end < 0 {
		return 0
	}
	return m[1] + end + len(closing)
}

// onlyTagBlocks: agents inject dozens of these and invent new ones each
// release, so they are recognised by shape rather than by name — a person
// does not type a message made only of XML blocks.
func onlyTagBlocks(text string) bool {
	rest := text
	for {
		n := leadingBlock(rest)
		if n == 0 {
			break
		}
		rest = strings.TrimLeft(rest[n:], " \t\r\n")
	}
	return rest == "" && text != ""
}

// removeBlocks cuts every `<tag …>…</tag>` block out of text.
func removeBlocks(text, tag string) string {
	open := regexp.MustCompile(`<` + regexp.QuoteMeta(tag) + `(?:\s[^>]*)?>`)
	closing := "</" + tag + ">"
	for {
		loc := open.FindStringIndex(text)
		if loc == nil {
			return text
		}
		end := strings.Index(text[loc[1]:], closing)
		if end < 0 {
			return text
		}
		text = text[:loc[0]] + text[loc[1]+end+len(closing):]
	}
}
