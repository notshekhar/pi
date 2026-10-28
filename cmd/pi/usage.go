package main

import (
	ai "github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/config"
)

// turnUsage is what one finished turn cost.
type turnUsage struct {
	in, out, cached int64
	cost            float64
	// ctxWindow is the model's window, 0 when the catalog does not know it.
	ctxWindow int
}

// measureUsage prices a turn's token counts against the catalog. Cache reads
// are billed at the cache rate, so they are taken back off the input price.
func measureUsage(cfg config.Config, final *ai.Result) turnUsage {
	var u turnUsage
	if final == nil {
		return u
	}
	if v := final.Usage.InputTokens.Total; v != nil {
		u.in = *v
	}
	if v := final.Usage.OutputTokens.Total; v != nil {
		u.out = *v
	}
	if v := final.Usage.InputTokens.CacheRead; v != nil {
		u.cached = *v
	}
	if m, ok := config.ModelInfo(cfg.Provider, cfg.ModelID); ok {
		u.ctxWindow = m.Context
		u.cost = float64(u.in)*m.Cost.Input/1e6 + float64(u.out)*m.Cost.Output/1e6
		if m.Cost.CacheRead > 0 {
			u.cost -= float64(u.cached) * (m.Cost.Input - m.Cost.CacheRead) / 1e6
		}
	}
	return u
}
