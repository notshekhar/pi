package trace

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

// OutputCap is loop's OUTPUT_CAP: tool outputs keep their head.
const OutputCap = 6_000

type part struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Input      json.RawMessage `json:"input"`
	Output     json.RawMessage `json:"output"`
}

func partsOf(raw json.RawMessage) []part {
	var parts []part
	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}
	return parts
}

// ── JS string semantics ──────────────────────────────────────────────────

func u16len(s string) int { return len(utf16.Encode([]rune(s))) }

func u16slice(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}

// jsSpace is JS's \s: Unicode White_Space plus U+FEFF.
func jsSpace(r rune) bool { return unicode.IsSpace(r) || r == 0xfeff }

func collapse(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if jsSpace(r) {
			in = true
			continue
		}
		if in && b.Len() > 0 {
			b.WriteByte(' ')
		}
		in = false
		b.WriteRune(r)
	}
	return b.String()
}

func oneLineString(s string, max int) string {
	s = collapse(s)
	if u16len(s) > max {
		return u16slice(s, max) + "…"
	}
	return s
}

// oneLine is loop's oneLine over a raw JSON value.
func oneLine(raw json.RawMessage, max int) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return oneLineString(s, max)
	}
	return oneLineString(safeJSON(raw, false), max)
}

// jsNumber formats a float as JS's Number#toString.
func jsNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	a := math.Abs(f)
	if a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(s, "e")
	if exp[0] == '+' {
		exp = "+" + strings.TrimLeft(exp[1:], "0")
	} else {
		exp = "-" + strings.TrimLeft(exp[1:], "0")
	}
	return mant + "e" + exp
}

// commas is en-US toLocaleString for an integer.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func jsString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteString(strconv.FormatInt(int64(r)>>4, 16))
				b.WriteString(strconv.FormatInt(int64(r)&0xf, 16))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// stringify re-encodes raw JSON as JSON.stringify would (key order kept,
// JS number and string forms, optional two-space indent).
func stringify(raw json.RawMessage, pretty bool) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var b bytes.Buffer
	var walk func(depth int) error
	nl := func(depth int) {
		if pretty {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat("  ", depth))
		}
	}
	walk = func(depth int) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch v := tok.(type) {
		case json.Delim:
			closing := map[json.Delim]byte{'{': '}', '[': ']'}[v]
			b.WriteByte(byte(v))
			n := 0
			for dec.More() {
				if n > 0 {
					b.WriteByte(',')
				}
				nl(depth + 1)
				if v == '{' {
					k, err := dec.Token()
					if err != nil {
						return err
					}
					jsString(&b, k.(string))
					b.WriteByte(':')
					if pretty {
						b.WriteByte(' ')
					}
				}
				if err := walk(depth + 1); err != nil {
					return err
				}
				n++
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			if n > 0 {
				nl(depth)
			}
			b.WriteByte(closing)
		case string:
			jsString(&b, v)
		case json.Number:
			f, err := v.Float64()
			if err != nil {
				return err
			}
			b.WriteString(jsNumber(f))
		case bool:
			b.WriteString(strconv.FormatBool(v))
		case nil:
			b.WriteString("null")
		}
		return nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	if err := walk(0); err != nil && err != io.EOF {
		return "", false
	}
	return b.String(), true
}

func safeJSON(raw json.RawMessage, pretty bool) string {
	s, _ := stringify(raw, pretty)
	return s
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || string(t) == "null"
}

// toolOutputText is loop's: a tool-result output in the AI SDK's tagged
// shape, as printable text.
func toolOutputText(raw json.RawMessage) string {
	if isNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o map[string]json.RawMessage
	if json.Unmarshal(raw, &o) != nil {
		return safeJSON(raw, true)
	}
	var typ string
	value, hasValue := o["value"]
	if json.Unmarshal(o["type"], &typ) != nil || !hasValue {
		return safeJSON(raw, true)
	}
	switch typ {
	case "text", "error-text":
		var v string
		if json.Unmarshal(value, &v) == nil {
			return v
		}
		if isNull(value) {
			return ""
		}
		return safeJSON(value, false)
	case "json", "error-json":
		return safeJSON(value, true)
	case "content":
		var items []json.RawMessage
		if json.Unmarshal(value, &items) != nil {
			return safeJSON(value, true)
		}
		var out []string
		for _, it := range items {
			var p struct {
				Type string `json:"type"`
				Text any    `json:"text"`
			}
			if json.Unmarshal(it, &p) == nil && p.Type == "text" && p.Text != nil {
				if t, ok := p.Text.(string); ok && t != "" {
					out = append(out, t)
				}
			}
		}
		return strings.Join(out, "\n")
	}
	return safeJSON(raw, true)
}

func isErrorOutput(raw json.RawMessage) bool {
	var o struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &o)
	return o.Type == "error-text" || o.Type == "error-json"
}

var headlineArgs = []string{"command", "cmd", "pattern", "query", "file_path", "path", "url", "prompt", "description", "content", "name"}

func rank(k string) int {
	for i, h := range headlineArgs {
		if h == k {
			return i
		}
	}
	return len(headlineArgs)
}

// toolInputLine is loop's: the call's arguments as one readable line, the
// argument a human would name the call by first and unlabelled.
func toolInputLine(raw json.RawMessage, max int) string {
	if isNull(raw) {
		return ""
	}
	t := bytes.TrimSpace(raw)
	if t[0] != '{' {
		return oneLine(raw, max)
	}
	dec := json.NewDecoder(bytes.NewReader(t))
	if _, err := dec.Token(); err != nil {
		return oneLine(raw, max)
	}
	type kv struct {
		k string
		v json.RawMessage
	}
	var keys []kv
	seen := map[string]int{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return oneLine(raw, max)
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return oneLine(raw, max)
		}
		ks := k.(string)
		if i, dup := seen[ks]; dup {
			keys[i].v = v
			continue
		}
		seen[ks] = len(keys)
		keys = append(keys, kv{ks, v})
	}
	var kept []kv
	for _, p := range keys {
		vt := string(bytes.TrimSpace(p.v))
		if vt == "null" || vt == `""` {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return ""
	}
	sort.SliceStable(kept, func(i, j int) bool { return rank(kept[i].k) < rank(kept[j].k) })
	parts := make([]string, len(kept))
	for i, p := range kept {
		var s string
		text := ""
		if json.Unmarshal(p.v, &s) == nil {
			text = s
		} else {
			text = safeJSON(p.v, false)
		}
		if i == 0 && rank(p.k) < len(headlineArgs) {
			parts[i] = text
		} else {
			parts[i] = p.k + ": " + text
		}
	}
	return oneLineString(strings.Join(parts, " · "), max)
}
