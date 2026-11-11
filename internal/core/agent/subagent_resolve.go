package agent

import (
	"slices"

	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
)

// resolveSubagentTools is loop's: the target agent's tools (all candidates
// when it lists none), capped to the parent's, never `task`. A fork resolves
// to exactly the parent's tools; a named agent can only narrow them.
func resolveSubagentTools(candidates, target, cap []string) []string {
	base := candidates
	if len(target) > 0 {
		base = slices.DeleteFunc(slices.Clone(target), func(t string) bool { return !slices.Contains(candidates, t) })
	}
	return slices.DeleteFunc(slices.Clone(base), func(t string) bool {
		return t == "task" || (len(cap) > 0 && !slices.Contains(cap, t))
	})
}

// subagentModel is the config a subagent runs on and a warning when the one
// asked for could not be used: the agent's own model, else the
// subagentModel setting, else the parent's. A configured model that cannot
// be built (unknown, or its provider signed out) falls back to the parent's
// — a stale agent file must never brick delegation — and says so.
func subagentModel(parent config.Config, agentModel, settingModel string) (config.Config, string) {
	configured := agentModel
	if configured == "" {
		configured = settingModel
	}
	if configured == "" || configured == parent.FullID() {
		return parent, ""
	}
	cfg := parent
	cfg.Provider, cfg.ModelID = catalog.Parse(configured, parent.Provider)
	if _, err := config.LanguageModel(cfg); err != nil {
		return parent, "subagent model \"" + configured + "\" is not available (" + err.Error() + ") — using " + parent.FullID()
	}
	return cfg, ""
}
