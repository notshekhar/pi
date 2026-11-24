package chatimport

import (
	json "encoding/json/v2"
	"strings"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
)

// Another agent's tool calls, renamed into our own tools where one does the
// same job, so an imported call draws as the row we would have drawn and,
// if the chat is continued, reads to the model like its own history. A call
// with no counterpart keeps its name and input untouched. loop's
// tool-calls.ts, table for table.

type args = map[string]any

// pick is the first of keys the call actually set, as a string.
func pick(a args, keys ...string) string {
	for _, k := range keys {
		if s, ok := a[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// defined drops empty fields, so a translated input carries only what was set.
func defined(fields args) args {
	out := args{}
	for k, v := range fields {
		if s, isString := v.(string); isString && s == "" {
			continue
		}
		if v != nil {
			out[k] = v
		}
	}
	return out
}

var (
	pathKeys = []string{"path", "file_path", "filePath", "target_file", "targetFile", "relativeWorkspacePath"}
	dirKeys  = []string{"path", "target_directory", "targetDirectory", "directory", "dir_path"}
)

// commandOf is a shell command however it was spelled — including Codex's
// argv form ["bash", "-lc", cmd].
func commandOf(a args) string {
	if s := pick(a, "command", "cmd"); s != "" {
		return s
	}
	argv, ok := a["command"].([]any)
	if !ok {
		argv, ok = a["cmd"].([]any)
	}
	if !ok {
		return ""
	}
	parts := make([]string, len(argv))
	for i, p := range argv {
		parts[i], _ = p.(string)
	}
	for i, p := range parts {
		if (p == "-c" || p == "-lc") && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return strings.Join(parts, " ")
}

func bashCall(a args) (string, any) { return "bash", defined(args{"command": commandOf(a)}) }

func readCall(a args) (string, any) {
	return "read", defined(args{"path": pick(a, pathKeys...), "offset": numberOr(a["offset"]), "limit": numberOr(a["limit"])})
}

func numberOr(v any) any {
	if n, ok := v.(float64); ok {
		return n
	}
	return nil
}

func writeCall(a args) (string, any) {
	return "write", defined(args{"path": pick(a, pathKeys...), "content": pick(a, "content", "contents")})
}

func editCall(a args) (string, any) {
	many := []args{a}
	if list, ok := a["edits"].([]any); ok {
		many = many[:0]
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				many = append(many, m)
			}
		}
	}
	edits := make([]any, len(many))
	for i, e := range many {
		edits[i] = args{"oldText": pick(e, "old_string", "oldText", "old_str"), "newText": pick(e, "new_string", "newText", "new_str")}
	}
	return "edit", args{"path": pick(a, pathKeys...), "edits": edits}
}

func grepCall(a args) (string, any) {
	return "grep", defined(args{
		"pattern": pick(a, "pattern", "query", "regex"),
		"path":    pick(a, dirKeys...),
		"glob":    pick(a, "glob", "include", "glob_pattern"),
	})
}

func findCall(a args) (string, any) {
	return "find", defined(args{"pattern": pick(a, "pattern", "glob_pattern", "globPattern"), "path": pick(a, dirKeys...)})
}

func lsCall(a args) (string, any) { return "ls", defined(args{"path": pick(a, dirKeys...)}) }

// fetchCall: we read a URL with `read`.
func fetchCall(a args) (string, any) { return "read", defined(args{"path": pick(a, "url")}) }

func websearchCall(a args) (string, any) {
	return "websearch", defined(args{"query": pick(a, "query", "search_term", "searchTerm")})
}

func taskCall(a args) (string, any) {
	agent := pick(a, "subagent_type", "agent")
	if agent == "" {
		agent = "general"
	}
	return "task", defined(args{"agent": agent, "prompt": pick(a, "prompt")})
}

var translations = map[string]func(args) (string, any){
	// Claude Code
	"bash": bashCall, "read": readCall, "write": writeCall, "edit": editCall, "multiedit": editCall,
	"grep": grepCall, "glob": findCall, "ls": lsCall, "webfetch": fetchCall, "websearch": websearchCall,
	"task": taskCall, "agent": taskCall,
	// Codex
	"exec_command": bashCall, "shell": bashCall, "local_shell": bashCall,
	// Cursor CLI
	"strreplace": editCall, "search_replace": editCall,
	// Cursor IDE
	"run_terminal_command_v2": bashCall, "run_terminal_cmd": bashCall, "read_file": readCall,
	"read_file_v2": readCall, "ripgrep_raw_search": grepCall, "grep_search": grepCall,
	"glob_file_search": findCall, "file_search": findCall, "list_dir": lsCall, "list_dir_v2": lsCall,
	"web_fetch": fetchCall, "web_search": websearchCall, "task_v2": taskCall,
}

// TranslateCall renames a foreign call into ours where one does the same job.
func TranslateCall(name string, input any) (string, any) {
	translate, known := translations[strings.ToLower(name)]
	a, isObject := input.(map[string]any)
	if !known || !isObject {
		return name, input
	}
	return translate(a)
}

// OutputText is a tool's output as the text we show: strings as they are,
// text-part arrays joined, anything else as JSON.
func OutputText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		lines := make([]string, 0, len(t))
		for _, p := range t {
			switch q := p.(type) {
			case string:
				lines = append(lines, q)
			case map[string]any:
				if text, ok := q["text"].(string); ok {
					lines = append(lines, text)
				} else if q["type"] == "image" {
					lines = append(lines, "[image]")
				} else {
					lines = append(lines, marshal(q))
				}
			}
		}
		return strings.Join(lines, "\n")
	}
	return marshal(v)
}

func marshal(v any) string {
	raw, err := json.Marshal(v, jsonopt.Write)
	if err != nil {
		return ""
	}
	return string(raw)
}

// ParseJSONish parses a JSON string a source stored arguments or results in;
// the raw value when it is not JSON.
func ParseJSONish(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	var out any
	if json.Unmarshal([]byte(s), &out, jsonopt.Read) != nil {
		return s
	}
	return out
}
