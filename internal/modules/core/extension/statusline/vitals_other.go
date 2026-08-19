//go:build !linux && !darwin

package statusline

// Everywhere else, vitals are unavailable rather than guessed. A layout shows
// no CPU segment and no memory rather than a confident zero — see Vitals.
func readCPU() (cpuTimes, bool)        { return cpuTimes{}, false }
func readMemory() (used, total uint64) { return 0, 0 }
