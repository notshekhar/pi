package statusline

import (
	"sync"
	"time"
)

// Host vitals — CPU and memory for the dashboard layouts.
//
// Sampled on an interval into a cached snapshot, and never probed during a
// repaint: the status line is rendered inside the render loop, and a layout
// that read the OS there would put a syscall on the path of every keystroke.
//
// The sampler only runs while a layout that shows vitals is the active one,
// so a machine is never probed for numbers nobody is looking at.

// Vitals is the last snapshot.
type Vitals struct {
	// CPU is 0..1 aggregate utilisation. Valid is false until a delta is
	// known, and on platforms where it cannot be read at all — a layout shows
	// nothing rather than a confident zero.
	CPU      float64
	CPUValid bool
	// MemUsed and MemTotal are bytes. MemUsed is zero when it cannot be read.
	MemUsed, MemTotal uint64
}

// Sampler keeps Vitals fresh.
type Sampler struct {
	mu       sync.Mutex
	stop     chan struct{}
	snapshot Vitals
	previous cpuTimes
	primed   bool
}

// cpuTimes is one reading of the cumulative CPU counters.
type cpuTimes struct{ idle, total uint64 }

// Start begins sampling, calling onTick after each sample so the caller can
// repaint — the clock and the CPU figure change with no user action, which
// would otherwise never trigger a render.
//
// Idempotent: starting an already-running sampler does nothing.
func (s *Sampler) Start(onTick func(), interval time.Duration) {
	s.mu.Lock()
	if s.stop != nil {
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	s.stop = stop
	s.mu.Unlock()

	s.tick() // prime memory immediately, so the first paint is not empty
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.tick()
				if onTick != nil {
					onTick()
				}
			}
		}
	}()
}

// Stop ends sampling. Idempotent.
func (s *Sampler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop == nil {
		return
	}
	close(s.stop)
	s.stop, s.primed = nil, false
}

// Get is the last snapshot.
func (s *Sampler) Get() Vitals {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

// tick takes one sample.
//
// The CPU reading is delegated whole to the platform rather than being
// computed here from counters, because the two platforms do not measure the
// same thing: Linux has cumulative counters and needs a DELTA, macOS has a
// load average that is already a rate. Threading a rate through delta
// arithmetic gives a difference of zero forever — which is exactly the bug
// this shape exists to prevent, and which the first version had.
func (s *Sampler) tick() {
	s.mu.Lock()
	previous, primed := s.previous, s.primed
	s.mu.Unlock()

	ratio, valid, next := cpuRatio(previous, primed)
	used, total := readMemory()

	s.mu.Lock()
	defer s.mu.Unlock()
	if valid {
		s.snapshot.CPU, s.snapshot.CPUValid = clamp01(ratio), true
	}
	s.previous, s.primed = next, true
	s.snapshot.MemUsed, s.snapshot.MemTotal = used, total
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
