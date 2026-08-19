package ai

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/modules/ai/jsonschema"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// ObjectPart is one update to a streaming object.
type ObjectPart[T any] struct {
	// Delta is the JSON text that arrived in this event.
	Delta string

	// Snapshot is everything received so far, decoded. It is a partial view:
	// fields appear as the model writes them, and a string still being written
	// appears truncated. Only the value from Final is complete.
	Snapshot T
}

// ObjectStreamResult is a running StreamObject call.
type ObjectStreamResult[T any] struct {
	// Stream carries a part per update. It is closed when the run ends.
	//
	// The caller must drain it or cancel the context, as with StreamText.
	Stream <-chan ObjectPart[T]

	done   chan struct{}
	mu     sync.Mutex
	result *ObjectResult[T]
	err    error
}

// Final blocks until the run has finished and returns the completed object.
// It must be called after the Stream has been drained.
func (r *ObjectStreamResult[T]) Final() (*ObjectResult[T], error) {
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result, r.err
}

// StreamObject runs the model once and streams its reply as a growing value of
// type T, which is useful for filling in a form or a table as it is written.
//
// The schema comes from T, exactly as in GenerateObject.
func StreamObject[T any](ctx context.Context, opts Options) (*ObjectStreamResult[T], error) {
	return StreamObjectWithSchema[T](ctx, opts, jsonschema.Reflect[T]())
}

// StreamObjectWithSchema is StreamObject with a hand-written schema.
func StreamObjectWithSchema[T any](ctx context.Context, opts Options, schema *jsonschema.Schema) (*ObjectStreamResult[T], error) {
	if err := prepareObjectCall(&opts, schema); err != nil {
		return nil, err
	}

	inner, err := StreamText(ctx, opts)
	if err != nil {
		return nil, err
	}

	out := make(chan ObjectPart[T], streamBufferSize)
	result := &ObjectStreamResult[T]{Stream: out, done: make(chan struct{})}

	go func() {
		defer close(out)
		defer close(result.done)

		var text strings.Builder
		// The last snapshot that parsed, so an update that adds no complete
		// field does not travel as a duplicate.
		var lastValid string

		for part := range inner.Stream {
			delta, ok := part.(provider.TextDelta)
			if !ok || delta.Delta == "" {
				continue
			}
			text.WriteString(delta.Delta)

			candidate, ok := completeJSON(text.String())
			if !ok || candidate == lastValid {
				continue
			}
			lastValid = candidate

			var snapshot T
			if err := json.Unmarshal([]byte(candidate), &snapshot); err != nil {
				// A prefix that closes into valid JSON can still mismatch T,
				// for instance a number that has only its minus sign so far.
				continue
			}

			select {
			case out <- ObjectPart[T]{Delta: delta.Delta, Snapshot: snapshot}:
			case <-ctx.Done():
				return
			}
		}

		final, runErr := inner.Final()

		result.mu.Lock()
		defer result.mu.Unlock()

		if runErr != nil {
			result.err = runErr
			return
		}

		object, err := decodeObject[T](final.Text)
		if err != nil {
			result.err = err
			return
		}
		result.result = &ObjectResult[T]{
			Object:       object,
			Text:         final.Text,
			FinishReason: final.FinishReason,
			Usage:        final.Usage,
			Warnings:     final.Warnings,
			Messages:     final.Messages,
		}
	}()

	return result, nil
}
