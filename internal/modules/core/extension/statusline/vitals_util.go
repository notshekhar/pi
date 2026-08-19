package statusline

import (
	"context"
	"runtime"
	"time"
)

// numCPU is the core count, for turning a load average into a ratio.
func numCPU() int { return runtime.NumCPU() }

// timeout is a bounded context, so a hung probe cannot wedge the sampler.
func timeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
