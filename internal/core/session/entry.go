package session

import (
	"bytes"
	"encoding/json"
	"sort"
)

// Entry types — loop's Entry union (types.ts).
const (
	TypeSessionInfo   = "session-info"
	TypeMessage       = "message"
	TypeSubagent      = "subagent"
	TypeModelChange   = "model-change"
	TypeCompact       = "compact"
	TypeBranchSummary = "branch-summary"
	TypeLabel         = "label"
	TypeSessionName   = "session-name"
	TypeCustom        = "custom"
)

// Entry is one row of a session's tree: loop's Entry, field for field.
//
// The JSON is the contract — loop and pi read each other's rows — so every
// key loop writes is either a typed field here or kept verbatim in Extra and
// written back unchanged. A key pi does not know is never dropped.
type Entry struct {
	Type string
	TS   int64
	// ID is the entry's 8-hex id (a session-info's is the session id).
	ID string
	// ParentID is the entry this one follows; "" is the root (JSON null).
	ParentID string

	// message
	Role        string
	Content     json.RawMessage // string or AI SDK part array
	Usage       *Usage
	Model       string // also session-info's model
	Interrupted bool
	ReasoningMs []int64

	// session-info
	CreatedAt     int64
	CWD           string
	Provider      string
	ParentSession string

	// compact
	Summary      string
	CutAt        int
	TokensBefore int
	TokensAfter  *int // absent on compactions that never measured it
	Handoff      string
	Rollover     bool

	// branch-summary
	FromID string

	// label
	TargetID string
	Label    *string

	// session-name
	Name *string

	// custom
	Payload json.RawMessage

	// subagent
	Agent  string
	Prompt string
	Result string

	// model-change
	From, To string

	// tsRaw is the ts as it was read, so a fractional value loop wrote comes
	// back out unchanged when TS was not touched.
	tsRaw json.RawMessage
	tsAt  int64

	// Extra holds every key not modelled above (timing, team, activity,
	// details, trigger, …), raw, so a round trip loses nothing.
	Extra map[string]json.RawMessage
}

// modelled lists, per type, the keys the typed fields own. Everything else in
// a row goes to Extra.
var modelled = map[string][]string{
	TypeSessionInfo:   {"createdAt", "cwd", "provider", "model", "parentSession"},
	TypeMessage:       {"role", "content", "usage", "model", "interrupted", "reasoningMs"},
	TypeSubagent:      {"agent", "prompt", "result", "usage", "model"},
	TypeModelChange:   {"from", "to"},
	TypeCompact:       {"summary", "cutAt", "tokensBefore", "tokensAfter", "usage", "model", "handoff", "rollover"},
	TypeBranchSummary: {"summary", "fromId", "usage", "model"},
	TypeLabel:         {"targetId", "label"},
	TypeSessionName:   {"name"},
	TypeCustom:        {"payload"},
}

func owns(typ, key string) bool {
	switch key {
	case "type", "ts", "id", "parentId":
		return true
	}
	for _, k := range modelled[typ] {
		if k == key {
			return true
		}
	}
	return false
}

// UnmarshalJSON reads a row, typed where modelled and raw everywhere else.
func (e *Entry) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*e = Entry{}
	get := func(key string, into any) {
		if raw, ok := m[key]; ok && !bytes.Equal(raw, []byte("null")) {
			_ = json.Unmarshal(raw, into)
		}
	}
	get("type", &e.Type)
	if raw, ok := m["ts"]; ok {
		var f float64
		if json.Unmarshal(raw, &f) == nil {
			e.TS = int64(f)
			e.tsRaw, e.tsAt = append(json.RawMessage{}, raw...), e.TS
		}
	}
	get("id", &e.ID)
	get("parentId", &e.ParentID)
	switch e.Type {
	case TypeSessionInfo:
		get("createdAt", &e.CreatedAt)
		get("cwd", &e.CWD)
		get("provider", &e.Provider)
		get("model", &e.Model)
		get("parentSession", &e.ParentSession)
	case TypeMessage:
		get("role", &e.Role)
		if raw, ok := m["content"]; ok {
			e.Content = append(json.RawMessage{}, raw...)
		}
		get("model", &e.Model)
		get("interrupted", &e.Interrupted)
		get("reasoningMs", &e.ReasoningMs)
	case TypeSubagent:
		get("agent", &e.Agent)
		get("prompt", &e.Prompt)
		get("result", &e.Result)
		get("model", &e.Model)
	case TypeModelChange:
		get("from", &e.From)
		get("to", &e.To)
	case TypeCompact:
		get("summary", &e.Summary)
		get("cutAt", &e.CutAt)
		get("tokensBefore", &e.TokensBefore)
		get("tokensAfter", &e.TokensAfter)
		get("model", &e.Model)
		get("handoff", &e.Handoff)
		get("rollover", &e.Rollover)
	case TypeBranchSummary:
		get("summary", &e.Summary)
		get("fromId", &e.FromID)
		get("model", &e.Model)
	case TypeLabel:
		get("targetId", &e.TargetID)
		if raw, ok := m["label"]; ok && !bytes.Equal(raw, []byte("null")) {
			var l string
			if json.Unmarshal(raw, &l) == nil {
				e.Label = &l
			}
		}
	case TypeSessionName:
		if raw, ok := m["name"]; ok && !bytes.Equal(raw, []byte("null")) {
			var n string
			if json.Unmarshal(raw, &n) == nil {
				e.Name = &n
			}
		}
	case TypeCustom:
		if raw, ok := m["payload"]; ok {
			e.Payload = append(json.RawMessage{}, raw...)
		}
	}
	if raw, ok := m["usage"]; ok && owns(e.Type, "usage") && !bytes.Equal(raw, []byte("null")) {
		var u Usage
		if json.Unmarshal(raw, &u) == nil {
			e.Usage = &u
		}
	}
	for k, v := range m {
		if !owns(e.Type, k) {
			if e.Extra == nil {
				e.Extra = map[string]json.RawMessage{}
			}
			e.Extra[k] = append(json.RawMessage{}, v...)
		}
	}
	return nil
}

// MarshalJSON writes the row in loop's key order: type and ts first, the
// type's own fields, anything carried in Extra, then id and parentId.
func (e Entry) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	put := func(key string, v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			return
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		k, _ := json.Marshal(key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(raw)
	}
	putRaw := func(key string, raw json.RawMessage) {
		if len(raw) == 0 {
			return
		}
		put(key, raw)
	}
	put("type", e.Type)
	if len(e.tsRaw) > 0 && e.tsAt == e.TS {
		put("ts", e.tsRaw)
	} else {
		put("ts", e.TS)
	}
	switch e.Type {
	case TypeSessionInfo:
		put("createdAt", e.CreatedAt)
		put("cwd", e.CWD)
		put("provider", e.Provider)
		put("model", e.Model)
		if e.ParentSession != "" {
			put("parentSession", e.ParentSession)
		}
	case TypeMessage:
		put("role", e.Role)
		if len(e.Content) == 0 {
			put("content", "")
		} else {
			putRaw("content", e.Content)
		}
		if e.Interrupted {
			put("interrupted", true)
		}
		if len(e.ReasoningMs) > 0 {
			put("reasoningMs", e.ReasoningMs)
		}
	case TypeSubagent:
		put("agent", e.Agent)
		put("prompt", e.Prompt)
		put("result", e.Result)
	case TypeModelChange:
		put("from", e.From)
		put("to", e.To)
	case TypeCompact:
		put("summary", e.Summary)
		put("cutAt", e.CutAt)
		put("tokensBefore", e.TokensBefore)
		if e.TokensAfter != nil {
			put("tokensAfter", *e.TokensAfter)
		}
		if e.Handoff != "" {
			put("handoff", e.Handoff)
		}
		if e.Rollover {
			put("rollover", true)
		}
	case TypeBranchSummary:
		put("summary", e.Summary)
		if e.FromID != "" {
			put("fromId", e.FromID)
		}
	case TypeLabel:
		put("targetId", e.TargetID)
		if e.Label != nil {
			put("label", *e.Label)
		}
	case TypeSessionName:
		if e.Name != nil {
			put("name", *e.Name)
		}
	case TypeCustom:
		if len(e.Payload) == 0 {
			put("payload", nil)
		} else {
			putRaw("payload", e.Payload)
		}
	}
	keys := make([]string, 0, len(e.Extra))
	for k := range e.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		putRaw(k, e.Extra[k])
	}
	if e.Usage != nil && owns(e.Type, "usage") {
		put("usage", e.Usage)
	}
	if e.Model != "" && e.Type != TypeSessionInfo && owns(e.Type, "model") {
		put("model", e.Model)
	}
	if e.ID != "" {
		put("id", e.ID)
		if e.ParentID == "" {
			put("parentId", nil)
		} else {
			put("parentId", e.ParentID)
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Info is a session's header — loop's SessionInfoData.
type Info struct {
	ID            string
	CreatedAt     int64
	CWD           string
	Provider      string
	Model         string
	ParentSession string
}

// infoEntry is the session-info root entry for info.
func infoEntry(info Info) Entry {
	return Entry{
		Type: TypeSessionInfo, TS: info.CreatedAt, ID: info.ID,
		CreatedAt: info.CreatedAt, CWD: info.CWD, Provider: info.Provider,
		Model: info.Model, ParentSession: info.ParentSession,
	}
}

// TextContent is message content as plain text: a string as-is, a part
// array's text parts joined (loop's extractMessageText).
func TextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b bytes.Buffer
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
