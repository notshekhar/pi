package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
)

// Compaction, ported from loop's agent/compact.ts and agent/summarize.ts.
//
// Nothing is rewritten. A compaction APPENDS a `compact` entry whose cutAt
// says how many of the branch's messages its summary stands in for; the
// model is then sent the summary plus everything after the cut, while the
// session keeps every message (see session.ModelMessages). The prompts are
// loop's, word for word, so both programs summarise the same way.

const summarizationSystemPrompt = "You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.\n\nDo NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary."

const summarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const turnPrefixSummarizationPrompt = `The messages above are earlier context from an ongoing conversation. Later messages are stored separately and do not need to be reconstructed.

Create a concise checkpoint of the user's request and the progress shown above. This checkpoint will be placed before the later messages so the conversation can continue with the necessary context.

## Original Request
[What did the user ask for?]

## Progress So Far
- [Key decisions and work completed in these messages]

## Context Needed to Continue
- [Information from these messages needed to understand the later work]

Only summarize information explicitly present above. Do not infer or recreate later messages.`

const (
	// DefaultKeepRecentTokens is how much recent conversation a compaction
	// keeps word for word, capped to a quarter of a small window.
	DefaultKeepRecentTokens = 20_000
	keepRecentWindowShare   = 0.25

	summaryInputWindowShare = 0.7
	summaryInputMaxChars    = 800_000
	summaryInputMinChars    = 16_000
	maxOverflowRetries      = 3
	toolResultMaxChars      = 2000
	summaryMaxOutputTokens  = 32_000
	maxSummaryRetries       = 3

	// CharsPerToken is the crude estimate used wherever no tokenizer is.
	CharsPerToken = 4
)

// EstimateTokens is loop's estimateTokens: ceil(chars / 4).
func EstimateTokens(chars int) int { return (chars + CharsPerToken - 1) / CharsPerToken }

// ShouldCompact reports whether `used` real input tokens crossed threshold of
// window. A zero threshold or unknown window disables it.
func ShouldCompact(used, window int, threshold float64) bool {
	if threshold <= 0 || window <= 0 || used <= 0 {
		return false
	}
	return float64(used) >= threshold*float64(window)
}

// CompactOptions tunes one compaction.
type CompactOptions struct {
	// Manual is an explicit /compact: if the kept window would swallow
	// everything, summarise everything rather than report nothing to do.
	Manual bool
	// Focus is what the user asked the summary to focus on.
	Focus string
	// KeepRecentTokens overrides the kept window.
	KeepRecentTokens int
	// Summarize writes the summary in one request instead of the text
	// summarizer (idle compaction's cache-reading summary); where the cut
	// falls is unchanged.
	Summarize func(ctx context.Context) (text string, usage *session.Usage, finish provider.FinishReasonKind, err error)
	// Trigger is recorded on the entry ("idle").
	Trigger string
}

// CompactResult reports what a compaction did. CutAt 0 means nothing to do.
type CompactResult struct {
	Summary                   string
	CutAt                     int
	TokensBefore, TokensAfter int
	// Truncated is the error the summary failed with when the span was
	// truncated instead (truncationRecord); Summary is that record.
	Truncated string
}

// TruncationPreamble opens the record a compaction saves when its summary
// could not be written (loop's TRUNCATION_PREAMBLE). It sits inside the usual
// summary block, so it says plainly that it is not one.
const TruncationPreamble = "The summary of the conversation before this point could not be written, so the conversation was truncated instead. What follows is a record built from the transcript — inputs and state, NOT a record of progress. Check live state (files on disk, the todo list) before continuing any work it mentions."

// truncationRecord is the fallback when every summary attempt failed: drop
// the span a summary would have replaced, keep a mechanical record of it. No
// model call, so it cannot fail the way the summary just did — a session too
// big to summarize still gets smaller instead of failing at every turn until
// it no longer fits at all.
//
// The record is a rollover's over the dropped span alone: every user
// message, the files touched, the commands run. An earlier summary is
// carried whole — it is already the only memory of what came before it. The
// todo list is re-injected on its own (compactedContext).
func truncationRecord(dropped []session.Entry, previousBody string) string {
	record := BuildRolloverHandoff(dropped, HandoffOptions{Limit: MaxHandoffChars})
	if record == "" {
		record = "(nothing in the truncated part could be carried over)"
	}
	text := TruncationPreamble
	if previousBody != "" {
		text += "\n\n## Summary from an earlier compaction\n" + previousBody
	}
	return text + "\n\n" + record
}

// ErrNothingToCompact is a compaction with nothing older than the kept window.
var ErrNothingToCompact = errors.New("nothing to compact")

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// messageTokens is a message's weight on the context estimate's scale.
func messageTokens(e session.Entry) int { return EstimateTokens(len(contentText(e.Content))) }

// findCutPoint walks back from the end keeping keepRecent tokens, never
// stopping inside a tool result's run (the call must travel with it).
func findCutPoint(msgs []session.Entry, previousCut, keepRecent int) int {
	cut, kept := len(msgs), 0
	for cut > previousCut && kept < keepRecent {
		cut--
		kept += messageTokens(msgs[cut])
	}
	for cut > previousCut && cut < len(msgs) && msgs[cut].Role == "tool" {
		cut--
	}
	return cut
}

func summaryInputBudgetChars(window int) int {
	if window <= 0 {
		return summaryInputMinChars
	}
	share := int(float64(window)*summaryInputWindowShare) * 4
	return max(summaryInputMinChars, min(summaryInputMaxChars, share))
}

func clipMiddle(text string, head, tail int) string {
	if len(text) <= head+tail {
		return text
	}
	omitted := len(text) - head - tail
	return fmt.Sprintf("%s\n[… %d chars omitted …]\n%s", text[:head], omitted, text[len(text)-tail:])
}

type part struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ToolName string          `json:"toolName"`
	Input    json.RawMessage `json:"input"`
	Output   json.RawMessage `json:"output"`
}

func partsOf(raw json.RawMessage) ([]part, bool) {
	var ps []part
	if json.Unmarshal(raw, &ps) != nil {
		return nil, false
	}
	return ps, true
}

// textOf is the text parts of a message's content, joined by newlines.
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	ps, _ := partsOf(raw)
	var texts []string
	for _, p := range ps {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// toolOutputText is a stored tool output as plain text.
func toolOutputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	switch o.Type {
	case "text", "error-text":
		var v string
		_ = json.Unmarshal(o.Value, &v)
		return v
	case "content":
		var items []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(o.Value, &items)
		var lines []string
		for _, it := range items {
			if it.Type == "text" {
				lines = append(lines, it.Text)
			} else {
				t := it.Type
				if t == "" {
					t = "media"
				}
				lines = append(lines, "["+t+"]")
			}
		}
		return strings.Join(lines, "\n")
	}
	if len(o.Value) > 0 {
		return string(o.Value)
	}
	return string(raw)
}

func truncateForSummary(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return fmt.Sprintf("%s\n\n[... %d more characters truncated]", text[:n], len(text)-n)
}

// serializeMessage is one message as the summariser reads it — loop's
// serializeMessage: reasoning dropped, tool results clipped, media named.
func serializeMessage(e session.Entry) string {
	var out []string
	switch e.Role {
	case "user":
		var all []string
		if t := textOf(e.Content); t != "" {
			all = append(all, t)
		}
		if ps, ok := partsOf(e.Content); ok {
			for _, p := range ps {
				if p.Type == "image" || p.Type == "file" {
					all = append(all, "["+p.Type+"]")
				}
			}
		}
		if len(all) > 0 {
			out = append(out, "[User]: "+strings.Join(all, " "))
		}
	case "assistant":
		if t := textOf(e.Content); t != "" {
			out = append(out, "[Assistant]: "+t)
		}
		if ps, ok := partsOf(e.Content); ok {
			var calls []string
			for _, p := range ps {
				if p.Type != "tool-call" {
					continue
				}
				var input map[string]json.RawMessage
				args := string(p.Input)
				if json.Unmarshal(p.Input, &input) == nil {
					keys := make([]string, 0, len(input))
					for k := range input {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					var kv []string
					for _, k := range keys {
						kv = append(kv, k+"="+string(input[k]))
					}
					args = strings.Join(kv, ", ")
				}
				calls = append(calls, p.ToolName+"("+args+")")
			}
			if len(calls) > 0 {
				out = append(out, "[Assistant tool calls]: "+strings.Join(calls, "; "))
			}
		}
	case "tool":
		ps, ok := partsOf(e.Content)
		if !ok {
			return ""
		}
		for _, p := range ps {
			if p.Type != "tool-result" {
				continue
			}
			if t := toolOutputText(p.Output); t != "" {
				out = append(out, "[Tool result]: "+truncateForSummary(t, toolResultMaxChars))
			}
		}
	}
	return strings.Join(out, "\n\n")
}

func serializeConversation(msgs []session.Entry) string {
	var out []string
	for _, m := range msgs {
		if s := serializeMessage(m); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n\n")
}

func messageToText(e session.Entry) string {
	return "[" + e.Role + "] " + contentText(e.Content)
}

var fileListsRE = regexp.MustCompile(`(?s)\n*<(read|modified)-files>.*?</(read|modified)-files>`)

func withoutFileLists(summary string) string {
	return strings.TrimRight(fileListsRE.ReplaceAllString(summary, ""), " \n\t")
}

// collectFileLists is the files the summarised span read and changed, from
// its tool calls, carried across compactions.
func collectFileLists(msgs []session.Entry, prev session.FileLists) session.FileLists {
	read, modified := map[string]bool{}, map[string]bool{}
	for _, f := range prev.ReadFiles {
		read[f] = true
	}
	for _, f := range prev.ModifiedFiles {
		modified[f] = true
	}
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		ps, _ := partsOf(m.Content)
		for _, p := range ps {
			if p.Type != "tool-call" {
				continue
			}
			var in struct {
				Path *string `json:"path"`
			}
			if json.Unmarshal(p.Input, &in) != nil || in.Path == nil {
				continue
			}
			switch {
			case p.ToolName == "edit" || p.ToolName == "write":
				modified[*in.Path] = true
			case p.ToolName == "read" && !strings.Contains(*in.Path, "://"):
				read[*in.Path] = true
			}
		}
	}
	out := session.FileLists{ReadFiles: []string{}, ModifiedFiles: []string{}}
	for f := range read {
		if !modified[f] {
			out.ReadFiles = append(out.ReadFiles, f)
		}
	}
	for f := range modified {
		out.ModifiedFiles = append(out.ModifiedFiles, f)
	}
	sort.Strings(out.ReadFiles)
	sort.Strings(out.ModifiedFiles)
	return out
}

func formatFileLists(f session.FileLists) string {
	out := ""
	if len(f.ReadFiles) > 0 {
		out += "\n\n<read-files>\n" + strings.Join(f.ReadFiles, "\n") + "\n</read-files>"
	}
	if len(f.ModifiedFiles) > 0 {
		out += "\n\n<modified-files>\n" + strings.Join(f.ModifiedFiles, "\n") + "\n</modified-files>"
	}
	return out
}

// requestSummary is one summariser request on the session's model, at the
// least reasoning it takes (lowestThinkingLevel), every attempt billed
// (source "compact"). An empty or cut-off summary is an error: it would
// become the session's only memory of what it replaced.
//
// A model that refuses its lowest level outright (a 400 for "reasoning
// effort none") is asked once more at the session's own level: the settings
// every turn already succeeds with.
func (r *Run) requestSummary(ctx context.Context, messages []provider.Message, onUsage func(session.Usage)) (string, error) {
	lowest := config.ThinkingToEffort(lowestThinkingLevel(r.Config.ModelID))
	text, err := r.requestSummaryAt(ctx, messages, lowest, onUsage)
	if err == nil || r.Config.Reasoning == lowest || !isRejectedRequest(err) {
		return text, err
	}
	return r.requestSummaryAt(ctx, messages, r.Config.Reasoning, onUsage)
}

// isRejectedRequest: the provider turned the request down as malformed — the
// shape of a model that does not accept a reasoning setting. Not an overflow
// (too big, not wrong) and nothing worth retrying as is.
func isRejectedRequest(err error) bool {
	if isContextOverflow(err) || IsRetryableStreamError(err) {
		return false
	}
	var apiErr *provider.APICallError
	return errors.As(err, &apiErr) && (apiErr.StatusCode == 400 || apiErr.StatusCode == 422)
}

func (r *Run) requestSummaryAt(ctx context.Context, messages []provider.Message, effort provider.ReasoningEffort, onUsage func(session.Usage)) (string, error) {
	model, err := config.LanguageModel(r.Config)
	if err != nil {
		return "", err
	}
	var maxOut *int64
	if m, ok := config.ModelInfo(r.Config.Provider, r.Config.ModelID); ok && m.MaxOut > 0 {
		v := int64(min(m.MaxOut, summaryMaxOutputTokens))
		maxOut = &v
	}
	opts := ai.Options{
		Model:           model,
		System:          summarizationSystemPrompt,
		Messages:        messages,
		MaxSteps:        1,
		Reasoning:       effort,
		MaxOutputTokens: maxOut,
	}
	// The span's tool calls need their tools declared; none may run.
	if stubs := toolStubs(messages); len(stubs) > 0 {
		opts.Tools, opts.ToolChoice = stubs, &provider.ToolChoice{Type: provider.ToolChoiceNone}
	}
	// The wire shape a turn at this level gets (loop's buildAgentCallConfig):
	// anthropic thinking disabled, community toggles off, and so on.
	applyReasoning(&opts, r.Config)
	var lastErr error
	for attempt := 0; attempt <= maxSummaryRetries; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		res, err := ai.GenerateText(ctx, opts)
		if res != nil {
			onUsage(ToUsage(res.Usage))
		}
		if err != nil {
			lastErr = err
			// Only a dropped stream or a transient 5xx/529 is tried again; a
			// bad request fails once and says so.
			if ctx.Err() != nil || !IsRetryableStreamError(err) {
				return "", err
			}
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
			continue
		}
		if res.FinishReason.Unified == provider.FinishLength {
			return "", errors.New("the summary hit the output limit before it finished")
		}
		text := strings.TrimSpace(res.Text)
		if text == "" {
			return "", errors.New("the model returned an empty summary")
		}
		return text, nil
	}
	return "", lastErr
}

// Compact summarises everything older than the kept window into a compact
// entry. A failed compaction writes nothing.
func Compact(ctx context.Context, r *Run, opts CompactOptions) (CompactResult, error) {
	s := r.Session
	msgs := s.BranchMessages()
	prev, hasPrev := s.LatestCompact()
	previousCut := 0
	if hasPrev {
		previousCut = prev.CutAt
	}
	modelID := r.Config.FullID()
	window := 0
	if m, ok := config.ModelInfo(r.Config.Provider, r.Config.ModelID); ok {
		window = m.Context
	}
	keep := opts.KeepRecentTokens
	if keep <= 0 {
		keep = DefaultKeepRecentTokens
		if window > 0 {
			keep = min(DefaultKeepRecentTokens, int(float64(window)*keepRecentWindowShare))
		}
	}
	cut := findCutPoint(msgs, previousCut, keep)
	if cut <= previousCut && opts.Manual {
		cut = findCutPoint(msgs, previousCut, 0)
	}
	if cut <= previousCut {
		return CompactResult{}, ErrNothingToCompact
	}

	head := msgs[previousCut:cut]
	previousBody := ""
	if hasPrev {
		body := prev.Summary
		if prev.Handoff != "" {
			body = prev.Handoff
		}
		previousBody = withoutFileLists(body)
	}
	full := ""
	if previousBody != "" {
		full = session.CompactionBlockText(previousBody, "") + "\n"
	}
	var texts []string
	for _, m := range msgs[previousCut:] {
		texts = append(texts, messageToText(m))
	}
	full += strings.Join(texts, "\n")
	tokensBefore := EstimateTokens(len(full))

	// A kept window opening inside a request would leave the summary without
	// the user's message: summarise that request's start on its own.
	turnStart := -1
	if cut < len(msgs) && msgs[cut].Role != "user" {
		for i := cut - 1; i >= previousCut; i-- {
			if msgs[i].Role == "user" {
				turnStart = i
				break
			}
		}
	}
	history, turnPrefix := head, []session.Entry(nil)
	if turnStart >= 0 {
		history, turnPrefix = msgs[previousCut:turnStart], msgs[turnStart:cut]
	}

	var usage *session.Usage
	bill := func(u session.Usage) {
		Bill(modelID, u, BillContext{Source: session.SourceCompact, CWD: s.Info.CWD, SessionPub: s.ID}, false)
		if usage == nil {
			c := u
			usage = &c
		} else {
			usage = sumUsage(usage, u)
		}
	}
	focus := ""
	if f := strings.TrimSpace(opts.Focus); f != "" {
		focus = "\n\nAdditional focus: " + f
	}
	summarizeHistory := func(budget int) (string, error) {
		running := previousBody
		messages := toSummaryMessages(history)
		if len(messages) == 0 {
			if running == "" {
				return "No prior history.", nil
			}
			return running, nil
		}
		for _, chunk := range chunkMessages(messages, max(summaryInputMinChars/2, budget-len(running))) {
			instructions := summarizationPrompt
			if running != "" {
				instructions = updateSummarizationPrompt
			}
			out, err := r.requestSummary(ctx, summaryRequest(chunk, instructions+focus, running), bill)
			if err != nil {
				return "", err
			}
			running = out
		}
		return running, nil
	}
	summarizeTurnPrefix := func(budget int) (string, error) {
		var span []summaryMessage
		if chunks := chunkMessages(toSummaryMessages(turnPrefix), budget); len(chunks) > 0 {
			span = chunks[0]
		}
		return r.requestSummary(ctx, summaryRequest(span, turnPrefixSummarizationPrompt+focus, ""), bill)
	}

	text, truncated := "", ""
	budget := summaryInputBudgetChars(window)
	for retry := 0; opts.Summarize == nil; retry++ {
		historyText, err := summarizeHistory(budget)
		prefixText := ""
		if err == nil && len(turnPrefix) > 0 {
			prefixText, err = summarizeTurnPrefix(budget)
		}
		if err == nil {
			text = historyText
			if prefixText != "" {
				text += "\n\n---\n\n**Turn Context (split turn):**\n\n" + prefixText
			}
			break
		}
		if ctx.Err() != nil {
			return CompactResult{}, ctx.Err()
		}
		if !isContextOverflow(err) || retry >= maxOverflowRetries {
			// Every attempt failed: truncate instead. Idle compaction never
			// gets here — it reports its failure, and the next turn compacts
			// properly if it must.
			truncated = err.Error()
			text = truncationRecord(head, previousBody)
			break
		}
		budget /= 2
	}
	if opts.Summarize != nil {
		out, u, finish, err := opts.Summarize(ctx)
		if u != nil {
			bill(*u)
		}
		switch {
		case err != nil:
			return CompactResult{}, err
		case finish == provider.FinishLength:
			return CompactResult{}, errors.New("the summary hit the output limit before it finished")
		case strings.TrimSpace(out) == "":
			return CompactResult{}, errors.New("the model returned an empty summary")
		}
		text = strings.TrimSpace(out)
	}
	if ctx.Err() != nil {
		return CompactResult{}, ctx.Err()
	}

	files := collectFileLists(head, prev.CompactDetails())
	text += formatFileLists(files)
	tokensAfter := EstimateTokens(len(text))
	for _, m := range msgs[cut:] {
		tokensAfter += messageTokens(m)
	}
	details, _ := json.Marshal(files)
	entry := session.Entry{
		Type: session.TypeCompact, Summary: text, CutAt: cut,
		TokensBefore: tokensBefore, TokensAfter: &tokensAfter,
		Extra: map[string]json.RawMessage{"details": details},
	}
	if opts.Trigger != "" {
		entry.Extra["trigger"], _ = json.Marshal(opts.Trigger)
	}
	if truncated != "" {
		entry.Extra["truncated"], _ = json.Marshal(truncated)
	}
	if usage != nil {
		stamped := StampUsage(modelID, *usage)
		entry.Usage, entry.Model = &stamped, modelID
	}
	if err := s.AppendAll(entry); err != nil {
		return CompactResult{}, err
	}
	return CompactResult{Summary: text, CutAt: cut, TokensBefore: tokensBefore, TokensAfter: tokensAfter, Truncated: truncated}, nil
}

// sumUsage adds b into a, field by field (loop's sumUsage).
func sumUsage(a *session.Usage, b session.Usage) *session.Usage {
	add := func(x, y *int64) *int64 {
		if x == nil && y == nil {
			return nil
		}
		v := deref(x) + deref(y)
		return &v
	}
	out := *a
	out.InputTokens = add(a.InputTokens, b.InputTokens)
	out.OutputTokens = add(a.OutputTokens, b.OutputTokens)
	out.TotalTokens = add(a.TotalTokens, b.TotalTokens)
	if a.InputTokenDetails != nil || b.InputTokenDetails != nil {
		ai, bi := a.InputTokenDetails, b.InputTokenDetails
		if ai == nil {
			ai = &session.InputTokenDetails{}
		}
		if bi == nil {
			bi = &session.InputTokenDetails{}
		}
		out.InputTokenDetails = &session.InputTokenDetails{
			NoCacheTokens: add(ai.NoCacheTokens, bi.NoCacheTokens), CacheReadTokens: add(ai.CacheReadTokens, bi.CacheReadTokens),
			CacheWriteTokens: add(ai.CacheWriteTokens, bi.CacheWriteTokens),
		}
	}
	if a.OutputTokenDetails != nil || b.OutputTokenDetails != nil {
		ao, bo := a.OutputTokenDetails, b.OutputTokenDetails
		if ao == nil {
			ao = &session.OutputTokenDetails{}
		}
		if bo == nil {
			bo = &session.OutputTokenDetails{}
		}
		out.OutputTokenDetails = &session.OutputTokenDetails{
			TextTokens: add(ao.TextTokens, bo.TextTokens), ReasoningTokens: add(ao.ReasoningTokens, bo.ReasoningTokens),
		}
	}
	return &out
}
