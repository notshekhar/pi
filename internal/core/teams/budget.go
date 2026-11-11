package teams

import "github.com/notshekhar/pi/internal/core/config"

// The team budget (threadTeamsBudgetUsd): what a whole team may spend before
// it is stopped. One reading, shared by the tools (which refuse to restart a
// team over it) and the stop that enforces it.

// BudgetUSD is the budget, or 0 when none is set.
func BudgetUSD() float64 {
	if b := config.LoadSettings().ThreadTeamsBudgetUsd; b != nil && *b > 0 {
		return *b
	}
	return 0
}

// IsOverBudget is whether the team has spent its budget.
func IsOverBudget(teamID string) bool {
	budget := BudgetUSD()
	if budget == 0 {
		return false
	}
	cost, err := TeamCost(teamID)
	return err == nil && cost.USD >= budget
}

// DefaultMaxThreads is how many threads may work at once unless
// threadTeamsMaxThreads says otherwise.
const DefaultMaxThreads = 5

// maxThreadsCap bounds the setting.
const maxThreadsCap = 20

// MaxThreads is how many threads may work at once.
func MaxThreads() int {
	if n := config.LoadSettings().ThreadTeamsMaxThreads; n > 0 {
		return min(maxThreadsCap, n)
	}
	return DefaultMaxThreads
}

// Enabled is the threadTeams setting. Off unless turned on.
func Enabled() bool { return config.LoadSettings().ThreadTeamsOn() }
