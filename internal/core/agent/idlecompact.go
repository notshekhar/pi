package agent

import (
	"context"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/config"
)

// Loop's idle-compact.ts: compact a big conversation just before its prompt
// cache expires, while nobody is using it.
//
// Once a provider's prompt cache lapses (five minutes for Anthropic's
// default, about as long for OpenAI's and xAI's automatic caching), the next
// message re-reads the WHOLE conversation at full price. Summarizing it while
// the cache is still warm reads it at the cached price instead, and the
// message the user comes back with starts from the summary plus the recent
// turns. Any new turn in the session cancels it (RunTurn calls
// CancelIdleCompact at its top); a summary already being written then is
// thrown away rather than saved after the new message.

// cacheMargin is how long before the cache lapses to start: room for the
// summary call to land.
const cacheMargin = 45 * time.Second

// defaultCacheTTL is Anthropic's default cache lifetime, and roughly
// OpenAI's and xAI's automatic one.
const defaultCacheTTL = 5 * time.Minute

// DefaultIdleCompactMinPercent is the share of the window a conversation
// must fill before idle compaction bothers.
const DefaultIdleCompactMinPercent = 70

// autoCachingProviders cache automatically, with no breakpoints of ours.
var autoCachingProviders = map[string]bool{"openai": true, "xai": true}

var idleDelayForTests time.Duration

// SetIdleCompactDelayForTests fires after d instead of the cache lifetime
// (0 restores it).
func SetIdleCompactDelayForTests(d time.Duration) { idleDelayForTests = d }

// IdleCompactDelay is the idle delay for a provider, or false when it has no
// prompt cache to beat.
func IdleCompactDelay(cfg config.Config) (time.Duration, bool) {
	_, explicit := explicitCacheShape(cfg)
	if !explicit && !autoCachingProviders[config.EffectiveSDKProvider(cfg.Provider)] {
		return 0, false
	}
	if idleDelayForTests > 0 {
		return idleDelayForTests, true
	}
	return defaultCacheTTL - cacheMargin, true
}

type idleEntry struct {
	timer  *time.Timer
	cancel context.CancelFunc
}

var (
	idleMu      sync.Mutex
	idlePending = map[string]*idleEntry{}
)

// CancelIdleCompact drops this session's idle compaction, waiting or already
// writing.
func CancelIdleCompact(sessionID string) {
	idleMu.Lock()
	defer idleMu.Unlock()
	if e := idlePending[sessionID]; e != nil {
		e.timer.Stop()
		e.cancel()
		delete(idlePending, sessionID)
	}
}

// ScheduleIdleCompact runs run for this session once delay passes with no new
// turn. run gets the context CancelIdleCompact cancels and must check it
// before saving.
func ScheduleIdleCompact(sessionID string, delay time.Duration, run func(ctx context.Context)) {
	CancelIdleCompact(sessionID)
	ctx, cancel := context.WithCancel(context.Background())
	e := &idleEntry{cancel: cancel}
	idleMu.Lock()
	defer idleMu.Unlock()
	idlePending[sessionID] = e
	e.timer = time.AfterFunc(delay, func() {
		defer func() {
			idleMu.Lock()
			if idlePending[sessionID] == e {
				delete(idlePending, sessionID)
			}
			idleMu.Unlock()
			cancel()
		}()
		run(ctx)
	})
}

// HasPendingIdleCompact reports one waiting or writing.
func HasPendingIdleCompact(sessionID string) bool {
	idleMu.Lock()
	defer idleMu.Unlock()
	return idlePending[sessionID] != nil
}

// IdleSummaryInstructions is what the summary request appends to the cached
// conversation.
const IdleSummaryInstructions = `This conversation has gone idle, and it is being compacted before its prompt cache expires. Do not continue the conversation and do not call any tool.

Write a summary of the conversation above that lets you pick it up later with nothing else: the user's goal and constraints, what has been done and decided (with exact file paths, names, commands and results), the current state, and what is left. If an earlier summary appears above, fold it in rather than repeating it. The most recent messages will also be kept word for word after your summary, so focus on everything that came before them.

Use these sections, in markdown:
## Goal
## Done so far
## Key details
## Current state
## Next steps`
