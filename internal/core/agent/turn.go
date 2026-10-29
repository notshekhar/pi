package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
)

// RunTurn is loop's runTurn contract (agent/turn.ts) over pi's engine: one
// user input driven to completion, every step persisted as it finishes
// (abort-safe), every round-trip billed, and the whole thing narrated as
// loop's turn events (agent/events.ts) — the stream the desktop app, the web
// client and the RPC server consume.

// Emitter receives turn events: loop's event name and its payload. A nil
// payload is loop's `void` event.
type Emitter func(event string, data any)

// TurnEventNames is loop's TURN_EVENT_NAMES, in order.
var TurnEventNames = []string{
	"user-message", "team-message", "text-delta", "reasoning-start", "reasoning-delta", "reasoning-end",
	"tool-input-start", "tool-input-delta", "tool-call", "tool-result", "tool-error", "tool-input-updated",
	"attached-images", "hook-message", "hook-terminal-sequence", "compact-start", "compact-end", "step-usage",
	"stream-retry", "data-recap", "todo-update", "finish", "error", "subagent-delta", "subagent-tool",
	"subagent-step-usage", "subagent-finish",
}

// TurnOptions are one turn's inputs.
type TurnOptions struct {
	Input string
	// Tracker accumulates the session's spend; nil uses a throwaway one.
	Tracker *CostTracker
	Emit    Emitter
	// Source is how the ledger files the turn ("turn" by default).
	Source session.LedgerSource
	// Tap sees every raw stream part before it becomes an event — the TUI's
	// renderer, which draws parts rather than events.
	Tap func(ai.StreamPart)
}

// UserMessageEvent opens a turn.
type UserMessageEvent struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	TS   int64  `json:"ts"`
}

// ToolInputStartEvent is a tool call beginning to stream its input.
type ToolInputStartEvent struct {
	ToolName   string `json:"toolName,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

// ToolInputDeltaEvent is a fragment of a streaming tool input.
type ToolInputDeltaEvent struct {
	ToolCallID string `json:"toolCallId,omitempty"`
	Delta      string `json:"delta"`
}

// ToolCallEvent is a complete tool call.
type ToolCallEvent struct {
	ToolName   string `json:"toolName,omitempty"`
	Input      any    `json:"input,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

// ToolResultEvent is a tool's result.
type ToolResultEvent struct {
	ToolCallID string `json:"toolCallId,omitempty"`
	Output     any    `json:"output,omitempty"`
}

// ToolErrorEvent is a tool that threw.
type ToolErrorEvent struct {
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	Error      any    `json:"error"`
}

// StepUsageEvent is one billed step.
type StepUsageEvent struct {
	Usage     session.Usage `json:"usage"`
	Breakdown CostBreakdown `json:"breakdown"`
}

// FinishEvent ends a turn.
type FinishEvent struct {
	Usage         *session.Usage `json:"usage,omitempty"`
	LastStepUsage *session.Usage `json:"lastStepUsage,omitempty"`
}

// CompactStartEvent / CompactEndEvent bracket a compaction.
// StreamRetryEvent is loop's stream-retry: a transient failure killed the
// stream between steps, and the turn is reopening it.
type StreamRetryEvent struct {
	Attempt   int    `json:"attempt"`
	Max       int    `json:"max"`
	Reason    string `json:"reason"`
	StepsDone int    `json:"stepsDone"`
}

type CompactStartEvent struct {
	Reason string `json:"reason"`
	Mode   string `json:"mode,omitempty"`
}

type CompactEndEvent struct {
	Summary      string `json:"summary"`
	CutAt        int    `json:"cutAt"`
	TokensBefore int    `json:"tokensBefore"`
	TokensAfter  int    `json:"tokensAfter,omitempty"`
	Aborted      bool   `json:"aborted,omitempty"`
	Error        string `json:"error,omitempty"`
	Handoff      string `json:"handoff,omitempty"`
	Mode         string `json:"mode,omitempty"`
	// Truncated is the error the summary failed with when the span was
	// truncated instead; Summary is the record that replaced it.
	Truncated string `json:"truncated,omitempty"`
}

// TodoUpdateEvent is the checklist after a todo write.
type TodoUpdateEvent struct {
	Items any `json:"items"`
}

// PlainError is an error as data (rpc/server.ts plainError): JSON of a Go
// error is {}, which reached clients as an empty object.
func PlainError(err error) map[string]any {
	if err == nil {
		return nil
	}
	out := map[string]any{"name": "Error", "message": err.Error()}
	var api interface{ StatusCode() int }
	if errors.As(err, &api) {
		out["statusCode"] = api.StatusCode()
	}
	if cause := errors.Unwrap(err); cause != nil {
		out["cause"] = PlainError(cause)
	}
	return out
}

// decodeInput is a tool call's JSON input as a value.
func decodeInput(raw string) any {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	return v
}

// lastUsageInSession is the newest provider-reported (not estimated) usage —
// the input/cache profile an interrupted step's estimate is anchored to.
func lastUsageInSession(s *session.Session) *session.Usage {
	all := s.Entries()
	for i := len(all) - 1; i >= 0; i-- {
		e := all[i]
		if (e.Type == session.TypeMessage && e.Role == "assistant" || e.Type == session.TypeSubagent) &&
			e.Usage != nil && !e.Usage.Estimated {
			return e.Usage
		}
	}
	return nil
}

// estimateInterruptedUsage is loop's: output from the partial text and
// reasoning at chars/4, input taken from the adjacent real step.
func estimateInterruptedUsage(text, reasoning string, basis *session.Usage, contextTokens int64) *session.Usage {
	textTokens := int64((len(text) + 3) / 4)
	reasoningTokens := int64((len(reasoning) + 3) / 4)
	out := textTokens + reasoningTokens
	if out == 0 && basis == nil {
		return nil
	}
	in := contextTokens
	var details *session.InputTokenDetails
	var cached *int64
	if basis != nil {
		in = deref(basis.InputTokens)
		if basis.InputTokenDetails != nil {
			d := *basis.InputTokenDetails
			details = &d
			cached = d.CacheReadTokens
		}
		if cached == nil {
			cached = basis.CachedInputTokens
		}
	}
	total := in + out
	return &session.Usage{
		InputTokens: &in, OutputTokens: &out, TotalTokens: &total, CachedInputTokens: cached,
		InputTokenDetails:  details,
		OutputTokenDetails: &session.OutputTokenDetails{TextTokens: &textTokens, ReasoningTokens: &reasoningTokens},
		Estimated:          true,
	}
}

// RunTurn runs one turn.
func (r *Run) RunTurn(ctx context.Context, opts TurnOptions) (*ai.Result, error) {
	emit := opts.Emit
	if emit == nil {
		emit = func(string, any) {}
	}
	tracker := opts.Tracker
	if tracker == nil {
		tracker = NewCostTracker()
	}
	source := opts.Source
	if source == "" {
		source = session.SourceTurn
	}
	modelID := r.Config.FullID()
	bill := BillContext{Source: source, CWD: r.Config.CWD, SessionPub: r.Session.ID}

	model, err := config.LanguageModel(r.Config)
	if err != nil {
		emit("error", err.Error())
		return nil, err
	}

	// A new turn cancels a pending idle compaction: the user is back.
	CancelIdleCompact(r.Session.ID)

	// The prompt is saved first: its entry id is the turn's id everywhere.
	r.Session.Lock()
	err = r.Session.Add(ai.UserText(opts.Input))
	leaf, hasLeaf := r.Session.Leaf()
	r.Session.Unlock()
	if err != nil {
		emit("error", err.Error())
		return nil, err
	}
	if hasLeaf {
		emit("user-message", UserMessageEvent{ID: leaf.ID, Text: opts.Input, TS: leaf.TS})
	}

	r.autoCompact(ctx, tracker, emit)

	toolset := r.Toolset()
	names := r.ToolNames()
	needsApproval, approveTool := r.approvalHooks()

	var (
		mu             sync.Mutex
		textSinceStep  strings.Builder
		reasonSinceStp strings.Builder
		lastStepUsage  *session.Usage
		persistedAny   bool
		persistErr     error
		toolNames      = map[string]string{}
	)
	system := SystemPromptFor(r.Config.CWD, names, r.Planning) + r.personaBlock() + r.extensionBlock()
	// loop's attempt loop: one iteration is one stream. A transient failure
	// that lands BETWEEN steps reopens the stream over what the finished
	// steps persisted (a resume); a request the provider refused for its
	// size is compacted and retried once. With a step's output already
	// streaming, either would make the model repeat it, so the error is
	// reported instead.
	maxSteps := r.Config.MaxSteps
	if maxSteps <= 0 {
		maxSteps = config.DefaultMaxSteps
	}
	maxResumes := DefaultMaxStreamResumes
	if v := config.LoadSettings().MaxStreamResumes; v != nil {
		maxResumes = max(0, *v)
	}
	var (
		sawFinish         bool
		streamErr         error
		finalErr          error
		final             *ai.Result
		stepsDone         int
		resumes           int
		overflowRecovered bool
		earlier           []ai.Step
		earlierUsage      provider.Usage
	)
	for {
		r.Session.RLock()
		messages := r.Session.ModelMessages()
		r.Session.RUnlock()
		streamErr = nil
		var heldErrors []ai.StreamPart
		res, err := ai.StreamText(ctx, cachedOptions(ai.Options{
			Model:         model,
			System:        system,
			Messages:      messages,
			Tools:         toolset,
			MaxSteps:      max(1, maxSteps-stepsDone),
			Reasoning:     r.Config.Reasoning,
			NeedsApproval: needsApproval,
			ApproveTool:   approveTool,
			OnStepFinish: func(step ai.Step) {
				// Persisted as the step finishes, so an abort later keeps it.
				u := ToUsage(step.Usage)
				breakdown := tracker.Add(modelID, u, bill)
				rowID := tracker.TakeLastLedgerRowID()
				stamped := StampUsage(modelID, u)
				if err := r.persistStep(step, &stamped, modelID, rowID); err != nil {
					mu.Lock()
					persistErr = err
					mu.Unlock()
				}
				mu.Lock()
				stepsDone++
				persistedAny = true
				lastStepUsage = &stamped
				textSinceStep.Reset()
				reasonSinceStp.Reset()
				mu.Unlock()
				emit("step-usage", StepUsageEvent{Usage: stamped, Breakdown: breakdown})
			},
		}, r.Config, r.Session.ID))
		if err != nil {
			emit("error", PlainError(err))
			return nil, err
		}

		for part := range res.Stream {
			// An error is held back from the renderer until the attempt loop
			// knows it will not recover from it.
			if p, ok := part.(provider.ErrorPart); ok {
				if streamErr == nil {
					streamErr = p.Err
				}
				heldErrors = append(heldErrors, part)
				continue
			}
			if opts.Tap != nil {
				opts.Tap(part)
			}
			switch p := part.(type) {
			case provider.TextDelta:
				mu.Lock()
				textSinceStep.WriteString(p.Delta)
				mu.Unlock()
				emit("text-delta", p.Delta)
			case provider.ReasoningStart:
				emit("reasoning-start", nil)
			case provider.ReasoningDelta:
				mu.Lock()
				reasonSinceStp.WriteString(p.Delta)
				mu.Unlock()
				emit("reasoning-delta", p.Delta)
			case provider.ReasoningEnd:
				emit("reasoning-end", nil)
			case provider.ToolInputStart:
				toolNames[p.ID] = p.ToolName
				emit("tool-input-start", ToolInputStartEvent{ToolName: p.ToolName, ToolCallID: p.ID})
			case provider.ToolInputDelta:
				emit("tool-input-delta", ToolInputDeltaEvent{ToolCallID: p.ID, Delta: p.Delta})
			case provider.ToolCall:
				toolNames[p.ToolCallID] = p.ToolName
				emit("tool-call", ToolCallEvent{ToolName: p.ToolName, Input: decodeInput(p.Input), ToolCallID: p.ToolCallID})
			case ai.ToolExecuted:
				x := p.Execution
				out, failed := outputValue(x.Result.Output())
				switch {
				case x.Err != nil && !x.Denied:
					emit("tool-error", ToolErrorEvent{ToolCallID: x.ToolCallID, ToolName: x.ToolName, Error: PlainError(x.Err)})
				case failed:
					emit("tool-error", ToolErrorEvent{ToolCallID: x.ToolCallID, ToolName: x.ToolName, Error: out})
				default:
					emit("tool-result", ToolResultEvent{ToolCallID: x.ToolCallID, Output: out})
				}
				if x.ToolName == "todo" && x.Err == nil {
					r.persistTodos(emit)
				}
			case ai.RunFinish:
				total := ToUsage(p.Usage)
				sawFinish = true
				mu.Lock()
				last := lastStepUsage
				mu.Unlock()
				emit("finish", FinishEvent{Usage: &total, LastStepUsage: last})
			}
		}
		final, finalErr = res.Final()
		final = withEarlierSteps(final, earlier, earlierUsage)

		failure := firstErr(streamErr, finalErr)
		mu.Lock()
		tailInFlight := textSinceStep.Len() > 0 || reasonSinceStp.Len() > 0
		mu.Unlock()
		if failure != nil && ctx.Err() == nil && !tailInFlight {
			if !overflowRecovered && isContextOverflow(failure) {
				overflowRecovered = true
				emit("compact-start", CompactStartEvent{Reason: "overflow"})
				cres, cerr := Compact(ctx, r, CompactOptions{})
				end := CompactEndEvent{Summary: cres.Summary, CutAt: cres.CutAt, TokensBefore: cres.TokensBefore, TokensAfter: cres.TokensAfter, Truncated: cres.Truncated}
				if cerr != nil && !errors.Is(cerr, ErrNothingToCompact) {
					end.Error = cerr.Error()
				}
				emit("compact-end", end)
				if cerr == nil && cres.CutAt > 0 {
					earlier, earlierUsage = keepSteps(final)
					continue
				}
			}
			if IsRetryableStreamError(failure) && !sawFinish && resumes < maxResumes && stepsDone < maxSteps {
				resumes++
				emit("stream-retry", StreamRetryEvent{Attempt: resumes, Max: maxResumes,
					Reason: describeRetry(failure), StepsDone: stepsDone})
				waitOrDone(ctx, resumeDelay(resumes-1))
				if ctx.Err() == nil {
					earlier, earlierUsage = keepSteps(final)
					continue
				}
			}
		}
		// Not recovering: the renderer sees the failure after all.
		if opts.Tap != nil {
			for _, part := range heldErrors {
				opts.Tap(part)
			}
		}
		break
	}

	aborted := ctx.Err() != nil
	mu.Lock()
	tailText, tailReasoning := textSinceStep.String(), reasonSinceStp.String()
	last := lastStepUsage
	any := persistedAny
	mu.Unlock()
	if aborted {
		r.persistInterrupted(tailText, tailReasoning, last, !any, modelID, tracker, bill, emit)
		if !sawFinish {
			emit("finish", FinishEvent{LastStepUsage: last})
		}
		return final, ctx.Err()
	}
	if err := firstErr(finalErr, streamErr); err != nil {
		if !any && (strings.TrimSpace(tailText) != "" || strings.TrimSpace(tailReasoning) != "") {
			// The stream ended with nothing persisted: keep what streamed.
			r.Session.Lock()
			_ = r.Session.AppendAll(session.Entry{Type: session.TypeMessage, TS: time.Now().UnixMilli(),
				Role: "assistant", Content: tailContent(tailText, tailReasoning), Model: modelID})
			r.Session.Unlock()
		}
		emit("error", PlainError(err))
		return final, err
	}
	if persistErr != nil {
		emit("error", PlainError(persistErr))
		return final, persistErr
	}
	if source == session.SourceTurn {
		r.scheduleIdleCompact(model, system, toolset, last, emit)
	}
	return final, nil
}

// scheduleIdleCompact is loop's idle compaction (idlecompact.go): a big
// conversation is summarized just before its prompt cache lapses, if nobody
// sends anything first. The request is this turn's own — same system
// prompt, same tools (never called), same caching — with one message
// appended, so the provider reads the conversation from cache.
func (r *Run) scheduleIdleCompact(model provider.LanguageModel, system string, toolset []ai.Tool,
	last *session.Usage, emit Emitter) {
	delay, ok := IdleCompactDelay(r.Config)
	settings := config.LoadSettings()
	if !ok || !settings.IdleCompactOn() {
		return
	}
	info, ok := config.ModelInfo(r.Config.Provider, r.Config.ModelID)
	if !ok || info.Context <= 0 {
		return
	}
	minPercent := settings.IdleCompactMinPercent
	if minPercent <= 0 {
		minPercent = DefaultIdleCompactMinPercent
	}
	used := ContextTokensFromUsage(last)
	if used == 0 {
		used = int64(EstimateTokens(r.Session.Chars()))
	}
	if float64(used) < float64(info.Context)*float64(minPercent)/100 {
		return
	}
	ScheduleIdleCompact(r.Session.ID, delay, func(ctx context.Context) {
		emit("compact-start", CompactStartEvent{Reason: "idle"})
		res, err := Compact(ctx, r, CompactOptions{Trigger: "idle", Summarize: func(ctx context.Context) (string, *session.Usage, provider.FinishReasonKind, error) {
			r.Session.RLock()
			conversation := append(r.Session.ModelMessages(), ai.UserText(IdleSummaryInstructions))
			r.Session.RUnlock()
			out, err := ai.StreamText(ctx, cachedOptions(ai.Options{
				Model:      model,
				System:     system,
				Messages:   conversation,
				Tools:      toolset,
				ToolChoice: &provider.ToolChoice{Type: provider.ToolChoiceNone},
				MaxSteps:   1,
				Reasoning:  r.Config.Reasoning,
			}, r.Config, r.Session.ID))
			if err != nil {
				return "", nil, "", err
			}
			for range out.Stream {
			}
			final, err := out.Final()
			if final == nil {
				return "", nil, "", err
			}
			u := ToUsage(final.Usage)
			return final.Text, &u, final.FinishReason.Unified, err
		}})
		end := CompactEndEvent{Summary: res.Summary, CutAt: res.CutAt, TokensBefore: res.TokensBefore, TokensAfter: res.TokensAfter, Truncated: res.Truncated}
		switch {
		case ctx.Err() != nil:
			// Cancelled because the user came back: not news.
			end = CompactEndEvent{TokensBefore: int(used), Aborted: true}
		case errors.Is(err, ErrNothingToCompact):
			// The kept window is the whole conversation: an empty result.
		case err != nil:
			end = CompactEndEvent{TokensBefore: int(used), Error: err.Error()}
		}
		emit("compact-end", end)
	})
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// tailContent is an unfinished step's partial output as AI SDK parts.
func tailContent(text, reasoning string) json.RawMessage {
	var parts []map[string]string
	if strings.TrimSpace(reasoning) != "" {
		parts = append(parts, map[string]string{"type": "reasoning", "text": reasoning})
	}
	if strings.TrimSpace(text) != "" {
		parts = append(parts, map[string]string{"type": "text", "text": text})
	}
	if len(parts) == 0 {
		return json.RawMessage(`""`)
	}
	raw, _ := json.Marshal(parts)
	return raw
}

// persistStep writes one finished step: the assistant message carrying the
// step's stamped usage and model (linked to its ledger row), then the tool
// results.
func (r *Run) persistStep(step ai.Step, usage *session.Usage, modelID string, rowID int64) error {
	var batch []session.Entry
	billed := -1
	for _, msg := range step.Messages {
		e, err := session.MessageEntry(msg)
		if err != nil {
			return err
		}
		if e.Role == "assistant" && billed < 0 {
			e.Usage, e.Model = usage, modelID
			billed = len(batch)
		}
		batch = append(batch, e)
	}
	if billed < 0 && usage != nil {
		// A step with no assistant message still billed real tokens.
		batch = append([]session.Entry{{Type: session.TypeMessage, TS: time.Now().UnixMilli(), Role: "assistant",
			Content: json.RawMessage(`[]`), Usage: usage, Model: modelID}}, batch...)
		billed = 0
	}
	if len(batch) == 0 {
		return nil
	}
	r.Session.Lock()
	defer r.Session.Unlock()
	if err := r.Session.AppendAll(batch...); err != nil {
		return err
	}
	if billed >= 0 && rowID != 0 {
		all := r.Session.Entries()
		if idx := len(all) - len(batch) + billed; idx >= 0 && idx < len(all) {
			_ = session.AttachLedgerEntry(rowID, all[idx].ID)
		}
	}
	return nil
}

// persistInterrupted saves an aborted turn's unfinished step so the
// transcript and the next turn's context both show it (`interrupted: true`
// makes the model context carry loop's interrupt note).
func (r *Run) persistInterrupted(text, reasoning string, last *session.Usage, nothingPersisted bool,
	modelID string, tracker *CostTracker, bill BillContext, emit Emitter) {
	streamed := strings.TrimSpace(text) != "" || strings.TrimSpace(reasoning) != ""
	var est *session.Usage
	if streamed {
		basis := last
		if basis == nil {
			basis = lastUsageInSession(r.Session)
		}
		est = estimateInterruptedUsage(text, reasoning, basis, int64(EstimateTokens(r.Session.Chars())))
		if est != nil {
			breakdown := tracker.AddEstimated(modelID, *est, bill)
			emit("step-usage", StepUsageEvent{Usage: *est, Breakdown: breakdown})
		}
	}
	if !streamed && !nothingPersisted {
		return
	}
	e := session.Entry{Type: session.TypeMessage, TS: time.Now().UnixMilli(), Role: "assistant",
		Content: tailContent(text, reasoning), Interrupted: true}
	if est != nil {
		stamped := StampUsage(modelID, *est)
		e.Usage, e.Model = &stamped, modelID
	}
	r.Session.Lock()
	defer r.Session.Unlock()
	if err := r.Session.AppendAll(e); err != nil {
		return
	}
	if rowID := tracker.TakeLastLedgerRowID(); est != nil && rowID != 0 {
		if leaf, ok := r.Session.Leaf(); ok {
			_ = session.AttachLedgerEntry(rowID, leaf.ID)
		}
	}
}

// persistTodos records the checklist as loop's custom entry and tells the
// stream.
func (r *Run) persistTodos(emit Emitter) {
	if r.Tools == nil || r.Tools.Todos == nil {
		return
	}
	items := r.Tools.Todos.Items()
	payload, _ := json.Marshal(map[string]any{"kind": "todos", "items": items})
	r.Session.Lock()
	_ = r.Session.AppendAll(session.Entry{Type: session.TypeCustom, TS: time.Now().UnixMilli(), Payload: payload})
	r.Session.Unlock()
	emit("todo-update", TodoUpdateEvent{Items: items})
}

// autoCompact compacts before the turn when the context has crossed the
// configured threshold (loop's decideContextAction "compact").
func (r *Run) autoCompact(ctx context.Context, tracker *CostTracker, emit Emitter) {
	threshold := config.LoadSettings().AutoCompact()
	if threshold <= 0 {
		return
	}
	info, ok := config.ModelInfo(r.Config.Provider, r.Config.ModelID)
	if !ok || info.Context <= 0 {
		return
	}
	used := ContextTokensFromUsage(lastUsageInSession(r.Session))
	if used == 0 {
		used = int64(EstimateTokens(r.Session.Chars()))
	}
	if !ShouldCompact(int(used), info.Context, threshold) {
		return
	}
	emit("compact-start", CompactStartEvent{Reason: "auto"})
	res, err := Compact(ctx, r, CompactOptions{})
	end := CompactEndEvent{Summary: res.Summary, CutAt: res.CutAt, TokensBefore: res.TokensBefore, TokensAfter: res.TokensAfter, Truncated: res.Truncated}
	switch {
	case errors.Is(err, context.Canceled):
		end.Aborted = true
	case err != nil && !errors.Is(err, ErrNothingToCompact):
		end.Error = err.Error()
	}
	emit("compact-end", end)
}

// outputValue is a tool result as the live `tool-result` carries it: the
// tool's own value (a string for most tools), and whether it reports failure.
func outputValue(o provider.ToolResultOutput) (any, bool) {
	switch v := o.(type) {
	case provider.ToolOutputText:
		return v.Value, false
	case provider.ToolOutputJSON:
		return v.Value, false
	case provider.ToolOutputErrorText:
		return v.Value, true
	case provider.ToolOutputErrorJSON:
		return v.Value, true
	case provider.ToolOutputExecutionDenied:
		return v.Reason, true
	case provider.ToolOutputContent:
		return v.Value, false
	}
	return nil, false
}
