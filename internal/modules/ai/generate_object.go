package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/jsonschema"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// ObjectResult is the outcome of a GenerateObject call.
type ObjectResult[T any] struct {
	// Object is the model's output decoded into T.
	Object T

	// Text is the raw JSON the model produced, kept for logging and for the
	// cases where a field was dropped in decoding.
	Text string

	FinishReason provider.FinishReason
	Usage        provider.Usage
	Warnings     []provider.Warning

	// Messages is the conversation including the model's reply, ready to pass
	// back as Options.Messages.
	Messages []provider.Message
}

// ObjectParseError reports that the model's output was not the requested
// shape. The raw text is attached so a caller can log it or retry.
type ObjectParseError struct {
	Text string
	Err  error
}

func (e *ObjectParseError) Error() string {
	return fmt.Sprintf("pi: model output is not valid JSON for the requested schema: %v (received %s)",
		e.Err, truncate(e.Text, 400))
}

func (e *ObjectParseError) Unwrap() error { return e.Err }

// GenerateObject runs the model once and decodes its reply into T.
//
// The schema is derived from T exactly as tool inputs are, so the same struct
// tags apply:
//
//	type Review struct {
//	    Summary string   `json:"summary" jsonschema:"description=One sentence"`
//	    Score   int      `json:"score" jsonschema:"minimum=1,maximum=5"`
//	    Tags    []string `json:"tags,omitempty"`
//	}
//
//	res, err := ai.GenerateObject[Review](ctx, ai.Options{Model: model, ...})
//
// Providers reach structured output differently — a response format, a
// response schema, or a forced tool — but the result is the same either way.
// Options.Tools is not allowed: a run that can call tools is GenerateText's
// job, and no provider supports both at once.
func GenerateObject[T any](ctx context.Context, opts Options) (*ObjectResult[T], error) {
	return GenerateObjectWithSchema[T](ctx, opts, jsonschema.Reflect[T]())
}

// GenerateObjectWithSchema is GenerateObject with a hand-written schema, for
// shapes reflection cannot express. The schema is what the model sees; the
// reply is still decoded into T, so the two must agree.
func GenerateObjectWithSchema[T any](ctx context.Context, opts Options, schema *jsonschema.Schema) (*ObjectResult[T], error) {
	if err := prepareObjectCall(&opts, schema); err != nil {
		return nil, err
	}

	res, err := GenerateText(ctx, opts)
	if err != nil {
		return nil, err
	}

	object, err := decodeObject[T](res.Text)
	if err != nil {
		return nil, err
	}

	return &ObjectResult[T]{
		Object:       object,
		Text:         res.Text,
		FinishReason: res.FinishReason,
		Usage:        res.Usage,
		Warnings:     res.Warnings,
		Messages:     res.Messages,
	}, nil
}

// prepareObjectCall validates the options for an object call and attaches the
// response format the providers read.
func prepareObjectCall(opts *Options, schema *jsonschema.Schema) error {
	if len(opts.Tools) > 0 {
		return errors.New("pi: GenerateObject and StreamObject do not accept tools")
	}

	name := opts.ObjectName
	if name == "" {
		name = "response"
	}

	opts.responseFormat = &provider.ResponseFormat{
		Type:        "json",
		Schema:      schema,
		Name:        name,
		Description: opts.ObjectDescription,
	}

	// One call, one object: there are no tools to feed back, so a second step
	// would only re-ask the same question.
	opts.MaxSteps = 1
	return nil
}

// decodeObject parses the model's reply into T.
func decodeObject[T any](text string) (T, error) {
	var value T

	cleaned := stripCodeFence(text)
	if strings.TrimSpace(cleaned) == "" {
		return value, &ObjectParseError{Text: text, Err: errors.New("the model returned no output")}
	}

	if err := json.Unmarshal([]byte(cleaned), &value); err != nil {
		return value, &ObjectParseError{Text: text, Err: err}
	}
	return value, nil
}

// stripCodeFence removes a markdown fence around a JSON document. Models asked
// for JSON in the prompt rather than through a schema often wrap it, and the
// wrapper is never what the caller wanted.
func stripCodeFence(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}

	// Drop the opening fence and its optional language tag.
	if newline := strings.IndexByte(trimmed, '\n'); newline >= 0 {
		trimmed = trimmed[newline+1:]
	} else {
		return trimmed
	}
	if end := strings.LastIndex(trimmed, "```"); end >= 0 {
		trimmed = trimmed[:end]
	}
	return strings.TrimSpace(trimmed)
}
