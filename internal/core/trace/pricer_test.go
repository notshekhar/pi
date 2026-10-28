package trace

import (
	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
)

func init() {
	config.ConfigureCatalog()
	Pricer = func(model string, u session.Usage) (float64, bool) {
		usd, _, ok := agent.PriceUsage(model, u)
		return usd, ok
	}
}
