# pigo — where things stand, and what's next

Updated 2026-08-16, after Bedrock.

## The point of this project

`loop`'s hardest blocker for a Go rewrite was the AI provider layer — everything
else in loop is ordinary Go-shaped work, but `ai` + `@ai-sdk/*` had no Go
equivalent. pigo is that layer: a port of the Vercel AI SDK's model abstraction
so a Go loop has something to sit on.

Ported from the `~/Documents/notshekhar/ai` clone at **ai@7.0.58**, targeting
**LanguageModelV4**. V2/V3 in that repo are back-compat adapters only and were
deliberately skipped.

## What exists

`go test -race ./...` green, zero third-party dependencies.

| Package | What it is |
| --- | --- |
| `pi` (root) | `GenerateText`, `StreamText`, `GenerateObject`, `StreamObject`, `Embed`, `EmbedMany`, `GenerateImage`, tools, agent loop |
| `provider` | LanguageModelV4 plus the embedding and image model specs |
| `providerutil` | HTTP, SSE, retry, credentials |
| `jsonschema` | draft-07 subset + struct reflector |
| `middleware` | `Wrap` plus `DefaultSettings`, `Observe`, `Logging`, `ExtractReasoning`, `SimulateStreaming` |
| `providers/anthropic` | Messages API, hosted tools, structured output |
| `providers/google` | Generative Language, hosted tools, embeddings, Imagen |
| `providers/openaicompat` | chat completions (+ any gateway), embeddings, images |
| `providers/googlevertex` | Vertex AI, ADC on stdlib crypto |
| `providers/deepseek` | thin config over openaicompat |
| `providers/bedrock` | Converse + InvokeModel; SigV4 and event-stream on stdlib |
| `examples/*` | `agent`, `object`, `hosted`, `embed`, `image` |

## Verified against live APIs

Session two put real traffic through most of this. What has actually run:

- Text, tool calling and multi-step agent loops on all three wire formats.
- Reasoning and its replay, including Anthropic's signature round trip.
- Structured output on all three, generating and streaming.
- Google search grounding and Google hosted code execution.
- Embeddings on Google (`gemini-embedding-001`) and on the chat-completions
  shape (`text-embedding-3-small` via OpenRouter).

Anthropic's wire format was exercised through DeepSeek's Anthropic-compatible
endpoint, since the key in `~/.loop/auth.json` under `anthropic` is a
placeholder (`sk-test-…`, 401s against the real API).

## Still unverified, and why

| Area | Why not | What to do |
| --- | --- | --- |
| Anthropic's own endpoint | no working key | run `examples/agent -provider anthropic` once there is one |
| Anthropic hosted tools | same | `examples/hosted -provider anthropic -tool search` |
| Vertex ADC | never run; both auth flows are hand-rolled | `providers/googlevertex/auth.go`, `Options.TokenSource` is the escape hatch |
| Image generation | Imagen is closed to new API keys; Gemini image models have free-tier quota 0 | needs a billed key, then `examples/image` |

## Traps already paid for — don't rediscover these

Measured, each one after a real failure.

- **A signed thinking block with empty text still needs its `thinking` field.**
  `omitempty` on the string dropped it and the next turn 400d with
  ``missing field `thinking` ``. The field is a `*string` for exactly this.
- **A `null` hosted tool is worse than an error.** `&args` on a nil map
  marshals as `null`; Google accepts `{"googleSearch": null}`, ignores it, and
  answers ungrounded with nothing reporting that search never ran. The unit
  test that only checked for key presence passed the whole time.
- **`omitempty` treats an empty map as absent**, which is what produced the
  above: every hosted tool taking no configuration silently vanished.
- **DeepSeek has no `json_schema` response format**, only `json_object`
  ("This response_format type is unavailable now"). Hence
  `openaicompat.Options.DisableJSONSchema` and the prompt fallback.
- **Google's embedding batch repeats the model id on every entry**, even though
  it is already in the URL.
- **The `/embeddings` data array is not promised to be in input order.** It is
  sorted by the reported index; trusting arrival order would misalign every
  vector against its text.
- **Go's RE2 has no lookahead.** The legacy-Claude model regex copied verbatim
  from the TS source panicked at package init.
- **A deferred `recover()` cannot fix an unnamed return value.**
- **Token accounting disagrees across vendors.** Anthropic's `input_tokens`
  *excludes* cache; OpenAI's `prompt_tokens` *includes* it; Google's
  `candidatesTokenCount` *excludes* thoughts. Normalised in `provider.Usage`,
  where nil means "not reported" and is distinct from zero.
- **Google returns `STOP` even when it emitted a `functionCall`.**
- **Google rejects an empty root schema**; a zero-argument tool must omit
  `parameters` entirely.
- **openai-compatible tool-call deltas must be keyed by `index`.**
- **Anthropic's newest models 400 if `temperature` is present at all.**
- **Google rejects hosted tools mixed with function tools**; pigo refuses the
  combination up front so the message says why.
- **Bedrock streams with a binary event-stream, not SSE.** The frame has two
  CRC32s; a one-byte slip loses alignment for the rest of the body.
- **Mistral on Bedrock 400s unless tool-call ids are exactly nine
  alphanumeric characters.** Bedrock issues `tooluse_…` ids; they have to be
  rewritten on the way out and on the way back in.
- **Claude thinking on Bedrock lives in `additionalModelRequestFields`**, not
  `inferenceConfig`. The newest ids also reject `output_config.format` and
  tool `strict` — Bedrock's copy of the Messages schema lags Anthropic's.

## Design decisions, so they don't get re-litigated

- **Root package is `pi`, module is `pigo`.**
- **Tool API is generics + struct-tag reflection**, with `NewToolWithSchema`
  as the escape hatch.
- **`StreamPartType() string` is exported** on every stream part, returning the
  ai-sdk wire tag — which is what lets `pi` add `StepStart`/`ToolExecuted`/
  `RunFinish` to the same channel, and gives a JSON discriminator for free.
- **Tool failures feed back to the model**; `pi.ErrAbortRun` is the opt-out.
- **Hosted tools live in `Options.ProviderTools`, not `Options.Tools`**, because
  nothing local ever runs for one and the distinction matters at every layer.
- **Object calls refuse tools.** No provider supports both, and the failure
  modes if one pretended otherwise are silent.
- **Middleware's `Next` carries both directions**, so a layer can answer a
  stream request by generating — which is how `SimulateStreaming` works and
  what a cache would need.
- **Zero third-party dependencies**, including Vertex auth. Worth preserving.
- **The jsonschema reflector inlines definitions**, emitting `$ref`/`$defs`
  only for genuinely recursive types.

## Known gaps

| Gap | Notes |
| --- | --- |
| MCP as a hosted tool | Anthropic's `mcp_tool_use` / `mcp_tool_result` blocks are still warn-and-drop |
| Anthropic's remaining server tools | tool search, advisor, and the newer code-execution result shapes |
| More providers | groq/xai/mistral/cerebras have real upstream implementations rather than openai-compatible wrappers. Bedrock is in. |
| Speech / transcription | whole other model specs |
| Telemetry, batch APIs | not started |
| Vertex workload identity federation | `Options.TokenSource` covers it manually |

## The longer arc

1. Get a working Anthropic key and close the verification gap above.
2. Bedrock is in: Converse (text, tools, thinking, caching) and Titan/Cohere/Nova
   embeddings, signed and streamed with no AWS SDK. Live verification still
   needs an account. Hosted tools and image generation were left out — they
   are per-model APIs, not Converse.
3. Port loop's tool set onto `pi.Tool` — read/write/edit/bash/glob/grep. The
   fuzzy-edit bug in [[loop-tools-fork-bugs]] is inherited from pi-mono, so
   port the *fixed* version.
4. Then the TUI, which is the genuinely large piece and unrelated to any of
   this.
