package serve

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// IsLoopbackHost is loop's isLoopbackHost.
func IsLoopbackHost(h string) bool {
	return h == "127.0.0.1" || h == "::1" || h == "localhost" || h == "::ffff:127.0.0.1"
}

// LANAddresses is the machine's non-internal IPv4 addresses.
func LANAddresses() []string {
	var out []string
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}

// Tailnet is loop's TailnetIdentity.
type Tailnet struct {
	DNSName string
	IPs     []string
}

const macTailscale = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// TailnetIdentity asks the tailscale CLI who this machine is on the tailnet.
func TailnetIdentity() *Tailnet {
	bins := []string{"tailscale"}
	if _, err := os.Stat(macTailscale); err == nil {
		bins = append(bins, macTailscale)
	}
	for _, bin := range bins {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
		cancel()
		if err != nil || len(out) == 0 {
			continue
		}
		var st struct {
			BackendState string
			Self         *struct {
				DNSName      string
				TailscaleIPs []string
			}
		}
		if json.Unmarshal(out, &st) != nil {
			continue
		}
		if st.BackendState != "Running" || st.Self == nil {
			return nil
		}
		return &Tailnet{DNSName: strings.TrimSuffix(st.Self.DNSName, "."), IPs: st.Self.TailscaleIPs}
	}
	return nil
}

// TerminalQR is loop's terminalQr: two modules per row (▀ ▄ █) inside a
// two-module quiet zone, black on white so it scans on any theme.
func TerminalQR(text string) string {
	q, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return ""
	}
	q.DisableBorder = true
	bm := q.Bitmap()
	size := len(bm)
	dark := func(x, y int) bool { return x >= 0 && y >= 0 && x < size && y < size && bm[y][x] }
	const quiet = 2
	var lines []string
	for y := -quiet; y < size+quiet; y += 2 {
		var b strings.Builder
		for x := -quiet; x < size+quiet; x++ {
			top, bottom := dark(x, y), dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		lines = append(lines, "\x1b[30;47m"+b.String()+"\x1b[0m")
	}
	return strings.Join(lines, "\n")
}
