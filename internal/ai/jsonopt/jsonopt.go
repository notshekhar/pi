// Package jsonopt holds the encoding/json/v2 option sets pi passes at every
// JSON boundary. It lives under internal/ai because ai imports nothing of
// ours, and every other package (core, tui, rpc, cmd) may import ai.
package jsonopt

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// Read accepts every input encoding/json (v1) accepted and v2 rejects by
// default: invalid UTF-8 and lone surrogate escapes (decoded as U+FFFD, as v1
// did) and duplicate object member names (the last one wins, as in v1).
//
// Almost nothing pi decodes is pi's own: loop's TypeScript writes a lone
// surrogate whenever it slices a string mid-pair, and provider, MCP and LSP
// servers are someone else's code. A session that loads in v1 must still load.
var Read = json.JoinOptions(
	jsontext.AllowInvalidUTF8(true),
	jsontext.AllowDuplicateNames(true),
)

// Write keeps v1's output where it matters:
//   - map keys sorted (v2 leaves them in random order), so a request body,
//     a file in ~/.loop and anything hashed or diffed are stable — an
//     unstable tool schema would also miss the provider's prompt cache;
//   - invalid UTF-8 written as U+FFFD instead of failing, because tool output
//     and file contents are raw bytes;
//   - raw values (jsontext.Value) passed through as written: an escape such
//     as a lone "\ud800" from loop stays an escape instead of collapsing to
//     U+FFFD, and a repeated object name is kept rather than failing, since
//     they are usually someone else's bytes kept verbatim.
//
// It does not restore v1's HTML escaping of <, > and &: JSON.stringify never
// escapes them, so the unescaped form is the one loop writes.
var Write = json.JoinOptions(
	json.Deterministic(true),
	jsontext.AllowInvalidUTF8(true),
	jsontext.AllowDuplicateNames(true),
	jsontext.PreserveRawStrings(true),
)
