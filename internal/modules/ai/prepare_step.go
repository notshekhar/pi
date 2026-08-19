package ai

import (
	"context"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// StepContext describes the step about to run, for PrepareStep.
type StepContext struct {
	// Number is the zero-based index of the step within the run.
	Number int

	// Messages is the conversation as it stands, including everything earlier
	// steps added. It is a copy; editing it changes nothing.
	Messages []provider.Message

	// Steps are the completed steps, in order.
	Steps []Step
}

// StepOverrides changes how one step runs. Every field is optional, and a zero
// field leaves the run's setting alone.
//
// This is what a multi-phase agent needs: a cheap model for the first pass and
// an expensive one for the last, a narrowed tool set once the plan is settled,
// or a forced tool on the opening step.
type StepOverrides struct {
	// Model replaces the model for this step.
	Model provider.LanguageModel

	// ActiveTools restricts which tools the model is offered, by name. Nil
	// offers all of them; a non-nil empty slice offers none, which is how a
	// step is made to answer rather than call anything.
	//
	// Names that match no tool are ignored: a policy listing tools that are
	// not registered should narrow the set, not fail the run.
	ActiveTools []string

	// ToolChoice replaces the tool choice for this step. Forcing a tool for
	// one step is the safe way to use a forced choice, since leaving it set
	// for the whole run loops until MaxSteps.
	ToolChoice *provider.ToolChoice

	// System replaces the system prompt for this step.
	System string

	// Messages replaces the conversation for this step. This is the hook for
	// summarising or dropping history — see PruneMessages.
	//
	// It changes only what this step sends. The run's own history is
	// unaffected, so a later step still sees the full conversation unless it
	// prunes again.
	Messages []provider.Message

	MaxOutputTokens *int64
	Temperature     *float64
	TopP            *float64

	// Reasoning replaces the thinking effort for this step.
	Reasoning provider.ReasoningEffort
}

// PrepareStepFunc is called before each step and may change how it runs.
// Returning the zero StepOverrides leaves everything as configured.
type PrepareStepFunc func(ctx context.Context, step StepContext) (StepOverrides, error)

// stepPlan is the resolved configuration for one step.
type stepPlan struct {
	model     provider.LanguageModel
	tools     []Tool
	overrides StepOverrides
}

// dispatch indexes the step's tools by name.
//
// Execution has to go through this rather than the run's full set: narrowing
// the active tools is a permission boundary, and a model that calls a tool it
// was not offered — from replayed history, or by inventing it — must not be
// able to reach past that boundary.
func (p stepPlan) dispatch() toolSet {
	set := make(toolSet, len(p.tools))
	for _, t := range p.tools {
		set[t.Name()] = t
	}
	return set
}

// toolNames lists the step's tools, for error messages.
func (p stepPlan) toolNames() string {
	if len(p.tools) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(p.tools))
	for _, t := range p.tools {
		names = append(names, t.Name())
	}
	return joinComma(names)
}

// planStep resolves the options for the step about to run.
func (r *runner) planStep(ctx context.Context) (stepPlan, error) {
	plan := stepPlan{model: r.opts.Model, tools: r.opts.Tools}

	if r.opts.PrepareStep == nil {
		// ActiveTools on Options still applies to every step.
		plan.tools = filterActiveTools(plan.tools, r.opts.ActiveTools)
		return plan, nil
	}

	// The callback gets a copy: a callback that appends to Messages would
	// otherwise corrupt the run it is inspecting.
	messages := make([]provider.Message, len(r.messages))
	copy(messages, r.messages)

	overrides, err := r.opts.PrepareStep(ctx, StepContext{
		Number:   len(r.steps),
		Messages: messages,
		Steps:    r.steps,
	})
	if err != nil {
		return stepPlan{}, err
	}

	plan.overrides = overrides
	if overrides.Model != nil {
		plan.model = overrides.Model
	}

	// The step's own list wins over the run-wide one, so a step can widen the
	// set as well as narrow it.
	active := r.opts.ActiveTools
	if overrides.ActiveTools != nil {
		active = overrides.ActiveTools
	}
	plan.tools = filterActiveTools(plan.tools, active)

	return plan, nil
}

// filterActiveTools narrows a tool list to the named subset, preserving order
// so the prompt stays byte-identical between steps that use the same set and
// the provider's prompt cache keeps hitting.
//
// A nil list means "everything"; an empty non-nil list means "nothing".
func filterActiveTools(tools []Tool, active []string) []Tool {
	if active == nil {
		return tools
	}
	if len(active) == 0 {
		return nil
	}

	allowed := make(map[string]bool, len(active))
	for _, name := range active {
		allowed[name] = true
	}

	out := make([]Tool, 0, len(active))
	for _, t := range tools {
		if allowed[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

// prompt returns the prompt for this step, honouring a step's replacement
// system prompt and message list.
func (p stepPlan) prompt(r *runner) provider.Prompt {
	system := r.opts.System
	if p.overrides.System != "" {
		system = p.overrides.System
	}

	messages := r.messages
	if p.overrides.Messages != nil {
		messages = p.overrides.Messages
	}

	return buildPrompt(system, messages)
}

// callOptions renders the step's model call options, applying the overrides on
// top of the run's.
func (p stepPlan) callOptions(r *runner, prompt provider.Prompt, stream bool) provider.CallOptions {
	opts := r.opts.callOptions(prompt, stream)

	// Tools are always the step's, since the active set can differ per step.
	opts.Tools = specTools(p.tools, r.opts.ProviderTools)

	if p.overrides.ToolChoice != nil {
		opts.ToolChoice = p.overrides.ToolChoice
	}
	if p.overrides.MaxOutputTokens != nil {
		opts.MaxOutputTokens = p.overrides.MaxOutputTokens
	}
	if p.overrides.Temperature != nil {
		opts.Temperature = p.overrides.Temperature
	}
	if p.overrides.TopP != nil {
		opts.TopP = p.overrides.TopP
	}
	if p.overrides.Reasoning != "" {
		opts.Reasoning = p.overrides.Reasoning
	}

	return opts
}
