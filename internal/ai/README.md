# pigo

A Go port of the Vercel AI SDK's model layer (`ai@7`), aimed at command-line
agents. The import path is `github.com/notshekhar/pigo`; the package is `pi`.

```go
model := anthropic.New(anthropic.Options{}).LanguageModel("claude-opus-5")

res, err := pi.StreamText(ctx, pi.Options{
    Model:    model,
    System:   "You are a terse assistant.",
    Messages: []provider.Message{pi.UserText("what is in ./cmd?")},
    Tools:    []pi.Tool{listDir},
})
for part := range res.Stream {
    if d, ok := part.(provider.TextDelta); ok {
        fmt.Print(d.Delta)
    }
}
```

No third-party dependencies. Standard library only, including Vertex AI's
Google Cloud authentication and Bedrock's SigV4 signing.

## Layout

| Package | Role |
| --- | --- |
| `pi` (root) | `GenerateText`, `StreamText`, `GenerateObject`, `Embed`, `GenerateImage`, tools, the agent loop |
| `provider` | The model specs — ports of `LanguageModelV4` plus the embedding and image models |
| `providerutil` | HTTP, SSE, retries, credential loading |
| `jsonschema` | Schema types plus a reflector for Go structs |
| `middleware` | Wrapping a model in logging, cost accounting, defaults and shims |
| `providers/*` | Provider implementations |

Dependencies run one way: `providers/*` and `pi` both depend on `provider`,
which depends on nothing but `jsonschema`.

## Providers

| Package | Endpoint | Credentials |
| --- | --- | --- |
| `providers/anthropic` | Anthropic Messages | `ANTHROPIC_API_KEY` |
| `providers/google` | Generative Language (AI Studio) | `GOOGLE_API_KEY` or `GEMINI_API_KEY` |
| `providers/googlevertex` | Vertex AI | Application Default Credentials |
| `providers/deepseek` | DeepSeek | `DEEPSEEK_API_KEY` |
| `providers/kimi` | Moonshot AI (Kimi) | `KIMI_API_KEY` |
| `providers/bedrock` | Amazon Bedrock Converse | AWS credentials (env, `~/.aws/credentials`, instance role) |
| `providers/openaicompat` | OpenAI chat completions | configurable |

`openaicompat` targets the protocol rather than a vendor, so it also drives
Groq, Cerebras, Together, OpenRouter, Ollama, vLLM and similar:

```go
groq := openaicompat.New(openaicompat.Options{
    Name:      "groq",
    BaseURL:   "https://api.groq.com/openai/v1",
    APIKeyEnv: "GROQ_API_KEY",
})
```

Bedrock signs its own requests (SigV4) and decodes the binary event stream,
both on the standard library. Credentials resolve the way the AWS tools do:

```go
model := bedrock.New(bedrock.Options{Region: "us-east-1"}).
    LanguageModel("us.anthropic.claude-sonnet-4-5-20250929-v1:0")
```

## Tools

Tool input schemas are derived from a Go type by reflection. A field is
required unless it is a pointer or its json tag has `omitempty`.

```go
type ReadFileArgs struct {
    Path   string `json:"path" jsonschema:"description=Absolute path to read"`
    Offset *int   `json:"offset,omitempty" jsonschema:"description=Line to start at,minimum=0"`
}

readFile := pi.NewTool("read_file", "Read a file from disk",
    func(ctx context.Context, a ReadFileArgs) (pi.ToolResult, error) {
        b, err := os.ReadFile(a.Path)
        if err != nil {
            return pi.ToolError(err.Error()), nil
        }
        return pi.ToolText(string(b)), nil
    })
```

A tag value containing a comma must be single-quoted:
`jsonschema:"description='find files, then grep them'"`.

Use `pi.NewToolWithSchema` when reflection cannot express the shape.

Tool failures are reported to the model rather than ending the run, so it can
correct itself. That covers a returned error, a hallucinated tool name,
unparseable arguments, and a panic. To stop the whole run, return an error
wrapping `pi.ErrAbortRun`.

### Provider-hosted tools

Some tools run on the provider's side: the call and its result both arrive
inside the model's turn, and nothing executes locally. They go in
`ProviderTools`, not `Tools`.

```go
res, err := pi.StreamText(ctx, pi.Options{
    Model:         model,
    Messages:      []provider.Message{pi.UserText("what shipped in Go 1.26?")},
    ProviderTools: []provider.ProviderTool{google.Search()},
})
```

| Provider | Tools |
| --- | --- |
| `anthropic` | `WebSearch`, `WebFetch`, `CodeExecution`, `Computer`, `TextEditor`, `Bash`, `Memory` |
| `google` | `Search`, `URLContext`, `CodeExecution` |

Search results also arrive as `provider.Source` parts, so an answer can be
cited without parsing the tool payload. Google rejects hosted tools mixed with
function tools in one request, and pigo refuses the combination up front rather
than letting it surface as an opaque 400.

## Structured output

`GenerateObject` derives a schema from a Go type and decodes the reply into it.

```go
type Review struct {
    Summary string   `json:"summary"`
    Score   int      `json:"score" jsonschema:"minimum=1,maximum=5"`
    Tags    []string `json:"tags,omitempty"`
}

res, err := pi.GenerateObject[Review](ctx, pi.Options{Model: model, Messages: msgs})
res.Object.Summary
```

Providers reach it differently — a `json_schema` response format, a
`responseSchema`, or a forced tool on Anthropic, which has no native JSON mode
— but the result is the same struct. `StreamObject` gives the same thing as a
value that fills in as it is written; its snapshots are always valid, because
a partial document is closed off before decoding.

Object calls do not accept tools: no provider supports both at once.

## Embeddings and images

```go
model := google.New(google.Options{}).EmbeddingModel("gemini-embedding-001")
res, err := pi.EmbedMany(ctx, docs, pi.EmbedOptions{Model: model})
score, err := pi.CosineSimilarity(query, res.Embeddings[0])
```

`EmbedMany` splits the input into whatever the model says it accepts per call,
runs the batches in parallel where the provider allows it, and reassembles
them in input order. `GenerateImage` does the same for image counts.

Google's embeddings are asymmetric: pass `taskType` through `ProviderOptions`
(`RETRIEVAL_DOCUMENT` when indexing, `RETRIEVAL_QUERY` when searching) and
retrieval gets measurably better.

## Middleware

`middleware.Wrap` returns a `provider.LanguageModel`, so a wrapped model goes
anywhere a model goes.

```go
model = middleware.Wrap(model,
    middleware.Logging(slog.Default()),
    middleware.Observe(tracker.Record),
    middleware.DefaultSettings(middleware.Settings{Reasoning: provider.ReasoningLow}),
)
```

The first middleware listed is the outermost. Built in: `DefaultSettings`,
`Observe` (the hook for cost tracking), `Logging`, `ExtractReasoning` (for
models that emit `<think>` inline), and `SimulateStreaming` (for models with no
streaming endpoint).

## The stream

`StreamText` returns a channel carrying the provider's parts (`TextDelta`,
`ToolCall`, `ReasoningDelta`, …) interleaved with core's own `StepStart`,
`StepFinish`, `ToolExecuted` and `RunFinish`. Every part has a
`StreamPartType() string`, so a consumer can type-switch or dispatch on the
string — the latter is what a JSON transport wants.

The channel must be drained, or the context cancelled. Abandoning it without
either blocks the run's goroutine and leaks the provider connection.
`Final()` blocks until the stream is fully consumed.

Text and reasoning arrive as delimited blocks (`TextStart` → `TextDelta`… →
`TextEnd`) sharing an id. Blocks can interleave, so key on the id rather than
assuming the stream is sequential.

## Things worth knowing

**Token accounting differs by provider.** Anthropic's `input_tokens` excludes
cached tokens, so the spec's input total is the sum of fresh, cache-read and
cache-write. OpenAI's `prompt_tokens` already includes them. Google's
`candidatesTokenCount` excludes thinking tokens, so the output total is the
sum of the two. All three are normalised into `provider.Usage`, and every
field is a pointer — nil means "not reported", which is not zero.

**Reasoning needs its signature to be replayable.** Anthropic signs thinking
blocks and Google signs thought parts; a history that has lost the signature
is rejected. Core carries it through `ProviderMetadata` automatically. The
chat completions API has no field for replayed reasoning at all, so it is
dropped there.

**Google's schemas are OpenAPI 3.0, not JSON Schema.** `additionalProperties`,
`$schema` and `$defs` are stripped. A recursive tool input cannot be
expressed and must be flattened.

**Google reports `STOP` even when it asked for tools**, so the finish reason
is derived from whether tool calls were present. Taking it at face value would
end an agent loop before the tool ran.

**Anthropic's newer models reject sampling parameters.** `claude-opus-5` and
friends 400 if `temperature` is present, so it is dropped with a warning.
Thinking also has two shapes: a token budget for Gemini-2.5-era models and
Claude ≤ 4.5, and a level/effort pair for the newest ones.

**A thinking block with empty text still needs its `thinking` field.** A model
can return a signed block whose text is empty; omitting the field on replay
fails the next turn with ``missing field `thinking` ``. Measured, not guessed.

**DeepSeek has no `json_schema` response format**, only the schema-less
`json_object` mode. Structured output there falls back to describing the schema
in the prompt, which nothing enforces, so every such call carries a warning.
`openaicompat.Options.DisableJSONSchema` turns the same fallback on for other
gateways that need it.

**A `null` hosted tool is worse than an error.** Google accepts
`{"googleSearch": null}`, ignores it, and answers ungrounded with nothing to
say that search never ran. The tool objects are pointers precisely so
`omitempty` cannot turn an empty configuration into an absent one.

## Not implemented

- MCP as a provider-hosted tool, and Anthropic's remaining server tool block
  types beyond search, fetch and code execution.
- Bedrock hosted tools and image generation, which use per-model APIs rather
  than Converse.
- Speech and transcription — the remaining non-text model specs.
- Telemetry and the batch APIs.
- Workload identity federation for Vertex; use `Options.TokenSource`.

## Status

Every provider is covered by tests against replayed wire captures, and the
agent loop is covered end to end against all three wire formats.

Verified against live APIs: text and tool calling on all three wire formats,
reasoning and its replay, structured output (all three), streaming objects,
Google search grounding and hosted code execution, and embeddings on both
Google and the chat-completions shape.

Not yet verified live, for want of credentials rather than for want of trying:
Anthropic's own endpoint and its hosted tools (the Anthropic wire format was
exercised through a compatible gateway), Vertex ADC, Bedrock (SigV4 and the
event-stream decoder are covered by unit tests against captured frames), and
image generation — Imagen is closed to new API keys and Gemini's image models
have no free-tier quota.

```bash
go test -race ./...

go run ./examples/agent  -provider google -model gemini-3-pro "what is in ./provider?"
go run ./examples/agent  -provider bedrock -model us.anthropic.claude-sonnet-4-5-20250929-v1:0 "what is in ./provider?"
go run ./examples/object -provider google -stream "SQLite"
go run ./examples/hosted -provider google -tool search "what shipped in Go 1.26?"
go run ./examples/embed  -provider google "how do I run the tests?"
go run ./examples/image  -provider google -aspect 16:9 "a lighthouse in a storm"
```
