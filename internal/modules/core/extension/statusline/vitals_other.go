//go:build !linux && !darwin

package statusline

// Everywhere else, vitals are unavailable rather than guessed. A layout shows
// no CPU segment and no memory rather than a confident zero — see Vitals.
//
// All three functions, not two. `cpuRatio` was missing here while darwin and
// linux each defined their own, so the package did not COMPILE on any other
// platform — which is how a Windows build failed at `undefined: cpuRatio`
// rather than degrading to "no vitals" the way this file exists to make it.
func readCPU() (cpuTimes, bool)        { return cpuTimes{}, false }
func readMemory() (used, total uint64) { return 0, 0 }

func cpuRatio(_ cpuTimes, _ bool) (ratio float64, valid bool, next cpuTimes) {
	return 0, false, cpuTimes{}
}
