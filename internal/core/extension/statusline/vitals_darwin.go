//go:build darwin

package statusline

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// macOS has no /proc, and the exact numbers live behind Mach calls
// (host_processor_info, host_statistics64) that need cgo — which this binary
// does not use. So each is read the cheapest honest way available:
//
//	total memory  sysctl hw.memsize, in-process and free
//	used memory   `vm_stat`, one short-lived subprocess per sample
//	CPU           `sysctl -n vm.loadavg` via syscall, in-process
//
// loop's sampler refuses to spawn anything per tick, but its reason is
// Bun-specific: repeated subprocess churn inflates that allocator's
// high-water mark, RSS that never returns to the OS. Go has no such problem,
// and one `vm_stat` a second is a couple of milliseconds.

// pageSize is the VM page size, needed to turn vm_stat's page counts into
// bytes. Read once — it does not change.
var pageSize = uint64(syscall.Getpagesize())

// cpuRatio derives utilisation from the 1-minute load average.
//
// Not the same quantity as Linux's: load counts runnable threads, so it can
// exceed the core count and it lags. Divided by the core count it lands in
// the same 0..1 range and reads the same way — "how busy is this machine" —
// which is what the dashboard is for. The alternative was showing nothing at
// all on macOS, where the exact figure is behind a Mach call needing cgo.
//
// It is already a RATE, so there is nothing to difference: valid from the
// first reading, and `previous` is unused.
func cpuRatio(_ cpuTimes, _ bool) (ratio float64, valid bool, next cpuTimes) {
	raw, err := syscall.Sysctl("vm.loadavg")
	if err != nil {
		return 0, false, cpuTimes{}
	}
	load, ok := parseLoadavg(raw)
	if !ok {
		return 0, false, cpuTimes{}
	}
	cores := float64(numCPU())
	if cores <= 0 {
		cores = 1
	}
	return load / cores, true, cpuTimes{}
}

// parseLoadavg reads the struct loadavg syscall.Sysctl hands back.
//
// The value is a `struct loadavg { fixpt_t ldavg[3]; long fscale; }` in the
// host's byte order, which Sysctl returns as a string of raw bytes. The three
// averages are 32-bit fixed point over fscale.
func parseLoadavg(raw string) (float64, bool) {
	b := []byte(raw)
	if len(b) < 20 {
		return 0, false
	}
	le32 := func(at int) uint64 {
		return uint64(b[at]) | uint64(b[at+1])<<8 | uint64(b[at+2])<<16 | uint64(b[at+3])<<24
	}
	one := le32(0)
	// fscale is a long at offset 16 on 64-bit darwin.
	scale := le32(16)
	if scale == 0 {
		return 0, false
	}
	return float64(one) / float64(scale), true
}

// readMemory reports used and total bytes.
func readMemory() (used, total uint64) {
	if raw, err := syscall.Sysctl("hw.memsize"); err == nil {
		b := []byte(raw)
		for i := 0; i < len(b) && i < 8; i++ {
			total |= uint64(b[i]) << (8 * i)
		}
	}
	free := freePages() * pageSize
	if total > free {
		used = total - free
	}
	return used, total
}

// freePages is what vm_stat calls free plus speculative and inactive — the
// pages the system can hand out without evicting anything a program is using.
//
// Not "free" alone: macOS keeps almost nothing strictly free, so reporting
// against it would show every Mac as permanently out of memory.
func freePages() uint64 {
	ctx, cancel := timeout(2 * time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "vm_stat").Output()
	if err != nil {
		return 0
	}
	var pages uint64
	for _, line := range strings.Split(string(out), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Pages free", "Pages speculative", "Pages inactive":
		default:
			continue
		}
		n, err := strconv.ParseUint(strings.Trim(strings.TrimSpace(value), "."), 10, 64)
		if err != nil {
			continue
		}
		pages += n
	}
	return pages
}
