//go:build linux

package statusline

import (
	"os"
	"strconv"
	"strings"
)

// Linux keeps both numbers in /proc, so they are an in-process file read —
// no subprocess, and exact.

// cpuRatio is utilisation across the interval: 1 − Δidle/Δtotal.
//
// Two readings are needed because /proc/stat's counters are cumulative since
// boot — one reading can only say what the average has been since the machine
// started, which is not what anyone looks at a dashboard for.
func cpuRatio(previous cpuTimes, primed bool) (ratio float64, valid bool, next cpuTimes) {
	now, ok := readCPU()
	if !ok {
		return 0, false, previous
	}
	if !primed {
		return 0, false, now
	}
	dTotal := now.total - previous.total
	if dTotal == 0 {
		return 0, false, now
	}
	idle := float64(now.idle-previous.idle) / float64(dTotal)
	return 1 - idle, true, now
}

// readCPU sums the jiffies in /proc/stat's aggregate line.
func readCPU() (cpuTimes, bool) {
	body, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, false
	}
	line, _, _ := strings.Cut(string(body), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, false
	}
	var out cpuTimes
	for i, field := range fields[1:] {
		v, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuTimes{}, false
		}
		out.total += v
		// Fields are user, nice, system, idle, iowait, … — idle is the
		// fourth, and iowait counts as idle too: the CPU is not working
		// during it, which is what the number is meant to say.
		if i == 3 || i == 4 {
			out.idle += v
		}
	}
	return out, true
}

// readMemory reports used and total bytes.
//
// Used is total minus AVAILABLE, not minus free: Linux spends nearly all
// spare memory on cache, so "free" on a healthy machine is a number near zero
// that says nothing about pressure.
func readMemory() (used, total uint64) {
	body, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var available uint64
	for _, line := range strings.Split(string(body), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total = kb * 1024
		case "MemAvailable":
			available = kb * 1024
		}
	}
	if total > available {
		used = total - available
	}
	return used, total
}
