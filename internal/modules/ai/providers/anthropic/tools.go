package anthropic

import "github.com/notshekhar/pi/internal/modules/ai/provider"

// Provider-hosted tools. Anthropic runs these itself: the model calls them
// server-side and the results arrive in the same response, so nothing is
// executed locally and the agent loop does not step for them.
//
//	model := anthropic.New(anthropic.Options{}).LanguageModel("claude-opus-5")
//	res, err := ai.StreamText(ctx, ai.Options{
//	    Model:    model,
//	    Messages: []provider.Message{ai.UserText("what shipped in Go 1.26?")},
//	    ProviderTools: []provider.ProviderTool{
//	        anthropic.WebSearch(anthropic.WebSearchOptions{MaxUses: 3}),
//	    },
//	})
//
// They go in ai.Options.ProviderTools rather than Tools, since there is no
// local function to run for one.

// Tool ids, in the "{provider}.{tool}_{version}" form the spec uses. The dated
// suffix is Anthropic's own: a tool is a versioned contract, and asking for
// the wrong version is a 400 rather than a silent difference in behaviour.
const (
	toolIDWebSearch     = "anthropic.web_search_20250305"
	toolIDWebFetch      = "anthropic.web_fetch_20250910"
	toolIDCodeExecution = "anthropic.code_execution_20250825"
	toolIDComputer      = "anthropic.computer_20250124"
	toolIDTextEditor    = "anthropic.text_editor_20250728"
	toolIDBash          = "anthropic.bash_20250124"
	toolIDMemory        = "anthropic.memory_20250818"
)

// UserLocation biases web search towards a place. Type is always "approximate".
type UserLocation struct {
	City     string
	Region   string
	Country  string
	Timezone string
}

// WebSearchOptions configures the hosted web search tool.
type WebSearchOptions struct {
	// MaxUses caps how many searches the model may run in one turn. Zero
	// leaves the cap to Anthropic.
	MaxUses int

	// AllowedDomains and BlockedDomains restrict results. Setting both is an
	// error on Anthropic's side, so set at most one.
	AllowedDomains []string
	BlockedDomains []string

	// UserLocation biases results geographically.
	UserLocation *UserLocation
}

// WebSearch returns Anthropic's hosted web search tool.
//
// Results arrive as provider-executed tool results plus Source parts, so a
// caller can cite what the model read.
func WebSearch(opts WebSearchOptions) provider.ProviderTool {
	args := map[string]any{}
	if opts.MaxUses > 0 {
		args["maxUses"] = opts.MaxUses
	}
	if len(opts.AllowedDomains) > 0 {
		args["allowedDomains"] = opts.AllowedDomains
	}
	if len(opts.BlockedDomains) > 0 {
		args["blockedDomains"] = opts.BlockedDomains
	}
	if loc := opts.UserLocation; loc != nil {
		args["userLocation"] = map[string]any{
			"type":     "approximate",
			"city":     loc.City,
			"region":   loc.Region,
			"country":  loc.Country,
			"timezone": loc.Timezone,
		}
	}
	return provider.ProviderTool{ID: toolIDWebSearch, Name: "web_search", Args: args}
}

// WebFetchOptions configures the hosted web fetch tool.
type WebFetchOptions struct {
	// MaxUses caps how many pages the model may fetch in one turn.
	MaxUses int

	AllowedDomains []string
	BlockedDomains []string

	// MaxContentTokens truncates a fetched page. Zero leaves it to Anthropic.
	MaxContentTokens int

	// Citations asks for citation blocks alongside the fetched content.
	Citations bool
}

// WebFetch returns Anthropic's hosted web fetch tool, which retrieves a URL
// the model chooses.
func WebFetch(opts WebFetchOptions) provider.ProviderTool {
	args := map[string]any{}
	if opts.MaxUses > 0 {
		args["maxUses"] = opts.MaxUses
	}
	if len(opts.AllowedDomains) > 0 {
		args["allowedDomains"] = opts.AllowedDomains
	}
	if len(opts.BlockedDomains) > 0 {
		args["blockedDomains"] = opts.BlockedDomains
	}
	if opts.MaxContentTokens > 0 {
		args["maxContentTokens"] = opts.MaxContentTokens
	}
	if opts.Citations {
		args["citations"] = true
	}
	return provider.ProviderTool{ID: toolIDWebFetch, Name: "web_fetch", Args: args}
}

// CodeExecution returns Anthropic's hosted code execution tool, which runs
// Python in a sandbox on Anthropic's side.
func CodeExecution() provider.ProviderTool {
	return provider.ProviderTool{ID: toolIDCodeExecution, Name: "code_execution"}
}

// ComputerOptions configures the computer-use tool.
type ComputerOptions struct {
	// DisplayWidthPx and DisplayHeightPx describe the screen the model is
	// driving. Both are required.
	DisplayWidthPx  int
	DisplayHeightPx int

	// DisplayNumber is the X11 display, for a multi-screen setup.
	DisplayNumber int
}

// Computer returns the computer-use tool.
//
// Unlike web search this one is not hosted: Anthropic decides the actions and
// the caller performs them, so the tool result has to come back from client
// code driving a real screen.
func Computer(opts ComputerOptions) provider.ProviderTool {
	return provider.ProviderTool{
		ID:   toolIDComputer,
		Name: "computer",
		Args: map[string]any{
			"displayWidthPx":  opts.DisplayWidthPx,
			"displayHeightPx": opts.DisplayHeightPx,
			"displayNumber":   opts.DisplayNumber,
		},
	}
}

// TextEditor returns the text editor tool. Like Computer, the caller executes
// the edits the model asks for.
func TextEditor() provider.ProviderTool {
	return provider.ProviderTool{ID: toolIDTextEditor, Name: "str_replace_based_edit_tool"}
}

// Bash returns the bash tool. The caller runs the commands the model produces.
func Bash() provider.ProviderTool {
	return provider.ProviderTool{ID: toolIDBash, Name: "bash"}
}

// Memory returns the memory tool, which lets the model keep notes across
// turns in a directory the caller manages.
func Memory() provider.ProviderTool {
	return provider.ProviderTool{ID: toolIDMemory, Name: "memory"}
}

// hostedToolSpec describes how one provider tool reaches the wire.
type hostedToolSpec struct {
	// wireType is the API's "type" field, which carries the dated version.
	wireType string
	// name is what the model calls the tool.
	name string
	// beta is the anthropic-beta value the tool needs, if any.
	beta string
	// build adds the tool's own fields to the wire object.
	build func(dst *apiTool, args map[string]any)
}

// hostedTools maps a spec tool id onto its wire shape.
var hostedTools = map[string]hostedToolSpec{
	toolIDWebSearch: {
		wireType: "web_search_20250305",
		name:     "web_search",
		build: func(dst *apiTool, args map[string]any) {
			dst.MaxUses = intArg(args, "maxUses")
			dst.AllowedDomains = stringsArg(args, "allowedDomains")
			dst.BlockedDomains = stringsArg(args, "blockedDomains")
			dst.UserLocation = mapArg(args, "userLocation")
		},
	},
	toolIDWebFetch: {
		wireType: "web_fetch_20250910",
		name:     "web_fetch",
		beta:     "web-fetch-2025-09-10",
		build: func(dst *apiTool, args map[string]any) {
			dst.MaxUses = intArg(args, "maxUses")
			dst.AllowedDomains = stringsArg(args, "allowedDomains")
			dst.BlockedDomains = stringsArg(args, "blockedDomains")
			dst.MaxContentTokens = intArg(args, "maxContentTokens")
			if v, ok := args["citations"].(bool); ok && v {
				dst.Citations = &citations{Enabled: true}
			}
		},
	},
	toolIDCodeExecution: {
		wireType: "code_execution_20250825",
		name:     "code_execution",
		beta:     "code-execution-2025-08-25",
	},
	toolIDComputer: {
		wireType: "computer_20250124",
		name:     "computer",
		beta:     "computer-use-2025-01-24",
		build: func(dst *apiTool, args map[string]any) {
			dst.DisplayWidthPx = intArg(args, "displayWidthPx")
			dst.DisplayHeightPx = intArg(args, "displayHeightPx")
			dst.DisplayNumber = intArg(args, "displayNumber")
		},
	},
	toolIDTextEditor: {
		wireType: "text_editor_20250728",
		name:     "str_replace_based_edit_tool",
	},
	toolIDBash: {
		wireType: "bash_20250124",
		name:     "bash",
	},
	toolIDMemory: {
		wireType: "memory_20250818",
		name:     "memory",
		beta:     "context-management-2025-06-27",
	},
}

// intArg reads an int argument, tolerating the float64 a decoded JSON number
// becomes when the tool was built from a config file rather than in Go.
func intArg(args map[string]any, key string) *int {
	switch v := args[key].(type) {
	case int:
		return &v
	case int64:
		n := int(v)
		return &n
	case float64:
		n := int(v)
		return &n
	}
	return nil
}

// stringsArg reads a string slice argument.
func stringsArg(args map[string]any, key string) []string {
	switch v := args[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// mapArg reads an object argument.
func mapArg(args map[string]any, key string) map[string]any {
	v, _ := args[key].(map[string]any)
	return v
}
