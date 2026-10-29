package mcp

import (
	"os"
	"regexp"
	"strconv"
	"time"
)

// ServerConfig is one server entry, in loop's settings.json shape. Exactly
// one of Command or URL is set.
type ServerConfig struct {
	Name string `json:"-"`
	// Type is "stdio" (or empty) for a subprocess, "http"/"sse" for a
	// remote. The two remote kinds share a URL shape; a connect that fails
	// because the server speaks the other one is retried on it.
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Auth "oauth" marks a server known to want a browser login; most only
	// reveal it with a 401, which works the same.
	Auth string `json:"auth,omitempty"`
	// Enabled defaults true; false keeps the entry but skips connecting.
	Enabled      *bool    `json:"enabled,omitempty"`
	AllowedTools []string `json:"allowedTools,omitempty"`
	DeniedTools  []string `json:"deniedTools,omitempty"`
	// A pre-registered OAuth client, for servers that refuse dynamic
	// registration. ClientSecret takes ${env:VAR}.
	ClientID     string   `json:"clientId,omitempty"`
	ClientSecret string   `json:"clientSecret,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// Disabled reports an entry switched off (loop: enabled === false).
func (c ServerConfig) Disabled() bool { return c.Enabled != nil && !*c.Enabled }

// SetDisabled flips the entry the way loop stores it.
func (c *ServerConfig) SetDisabled(off bool) {
	if off {
		f := false
		c.Enabled = &f
	} else {
		c.Enabled = nil
	}
}

// IsHTTP reports a remote server.
func (c ServerConfig) IsHTTP() bool {
	return c.Type == "http" || c.Type == "sse" || (c.Command == "" && c.URL != "")
}

// remoteType is the transport a remote server is first tried on.
func (c ServerConfig) remoteType() string {
	if c.Type == "sse" {
		return "sse"
	}
	return "http"
}

// ToolAllowed is loop's isToolAllowed: deny wins; an allow list, when set,
// is exhaustive (an empty one allows nothing). Names are the server's own.
func (c ServerConfig) ToolAllowed(tool string) bool {
	for _, d := range c.DeniedTools {
		if d == tool {
			return false
		}
	}
	if c.AllowedTools == nil {
		return true
	}
	for _, a := range c.AllowedTools {
		if a == tool {
			return true
		}
	}
	return false
}

// envPlaceholder is loop's ${env:VAR}: a token kept out of the config file.
var envPlaceholder = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// ResolveSecrets substitutes ${env:VAR} placeholders from the environment.
func ResolveSecrets(value string) string {
	return envPlaceholder.ReplaceAllStringFunc(value, func(m string) string {
		return os.Getenv(envPlaceholder.FindStringSubmatch(m)[1])
	})
}

func resolveSecretMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = ResolveSecrets(v)
	}
	return out
}

// envList is Env, resolved, as KEY=VALUE pairs for a subprocess.
func (c ServerConfig) envList() []string {
	out := make([]string, 0, len(c.Env))
	for k, v := range resolveSecretMap(c.Env) {
		out = append(out, k+"="+v)
	}
	return out
}

// envDuration is a LOOP_MCP_* millisecond override (loop's brandEnv +
// envMs): unset or invalid keeps the default, and 0 is a real value.
func envDuration(suffix string, fallback time.Duration) time.Duration {
	raw := os.Getenv("LOOP_" + suffix)
	if raw == "" {
		return fallback
	}
	ms, err := strconv.ParseFloat(raw, 64)
	if err != nil || ms < 0 {
		return fallback
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// positiveEnvDuration is envDuration for the settings where loop reads
// `Number(env) || default`: 0 means "default", not "off".
func positiveEnvDuration(suffix string, fallback time.Duration) time.Duration {
	if d := envDuration(suffix, fallback); d > 0 {
		return d
	}
	return fallback
}
