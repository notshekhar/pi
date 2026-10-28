package main

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/shells"
	"github.com/notshekhar/pi/internal/rpc"
	"github.com/notshekhar/pi/internal/rpc/serve"
	"github.com/notshekhar/pi/internal/tui"
)

// /rc — loop's remoteControl: make this machine pairable (the server
// `loop serve` runs), print the link and a QR code for the phone, and stream
// this session's turns to every client. A client's message to the session on
// screen runs HERE, as if typed; /rc off stops it.

// liveServe is the /rc server plus this TUI as its live-session provider.
type liveServe struct {
	h         *serve.Handle
	t         *repl
	terminal  bool
	runningID string
}

type tuiLive struct{ t *repl }

func (l tuiLive) Session() *session.Session { return l.t.run.Session }
func (l tuiLive) Send(input string)         { l.t.app.Events <- tui.Event{Kind: tui.EvSubmit, Text: input} }
func (l tuiLive) Cancel()                   { l.t.app.Events <- tui.Event{Kind: tui.EvInterrupt} }

// Get is the TUI's session when it is the one asked for (and saved).
func (l *liveServe) Get(id string) rpc.LiveSession {
	if s := l.t.run.Session; s != nil && s.Saved() && s.ID == id {
		return tuiLive{l.t}
	}
	return nil
}

// publish forwards a turn event; the first one marks the session running.
func (l *liveServe) publish(id, name string, data any) {
	if id == "" {
		return
	}
	if l.runningID != id {
		l.runningID = id
		l.h.RPC.SetLiveRunning(id, true)
	}
	l.h.RPC.PublishLive(id, name, data)
}

// turnEnded settles the running flag after a turn.
func (l *liveServe) turnEnded() {
	if l.runningID != "" {
		l.h.RPC.SetLiveRunning(l.runningID, false)
		l.runningID = ""
	}
}

// portAnswers reports something already accepting connections on port.
func portAnswers(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (t *repl) rcPrintReach(fresh bool) {
	rc := t.live
	if rc == nil {
		return
	}
	code := rc.h.PairingCode(fresh)
	var tailnetURL string
	if !serve.IsLoopbackHost(rc.h.Hostname) {
		if tn := serve.TailnetIdentity(); tn != nil && tn.DNSName != "" {
			tailnetURL = fmt.Sprintf("http://%s:%d/?token=%s", tn.DNSName, rc.h.Port, rc.h.Token)
		}
	}
	pairing := rc.h.URL
	if len(rc.h.NetworkURLs) > 0 {
		pairing = rc.h.NetworkURLs[0]
	}
	if tailnetURL != "" {
		pairing = tailnetURL
	}
	th := t.app.Theme()
	accent := func(s string) string { return th.Fg(tui.SlotAccent, s) }
	dim := func(s string) string { return th.Fg(tui.SlotDim, s) }
	u, _ := url.Parse(pairing)
	lines := []string{accent("remote control on") + dim(" — scan with the loop app:"), ""}
	lines = append(lines, strings.Split(serve.TerminalQR(pairing), "\n")...)
	lines = append(lines, "", "  local     "+rc.h.URL)
	for _, n := range rc.h.NetworkURLs {
		lines = append(lines, "  network   "+n)
	}
	if tailnetURL != "" {
		lines = append(lines, "  tailnet   "+tailnetURL)
	}
	lines = append(lines,
		"  code      "+accent(code.Code[:3]+" "+code.Code[3:])+dim("  type host "+u.Host+" and this code in the app (one use, 5 min; /rc for a new one)"),
		dim("or paste a link into another loop's /hosts"))
	if rc.terminal {
		lines = append(lines, dim("terminal: offered to paired devices"))
	} else {
		lines = append(lines, dim(`terminal: this machine only — turn on "terminal for other devices" in /settings, then /rc off and /rc`))
	}
	lines = append(lines, th.Fg(tui.SlotWarning, "Anyone with this link fully controls this machine. ")+dim("/rc off stops it."))
	t.do(func() { t.app.PrintRaw(lines...) })
}

func (t *repl) remoteControl(args string) {
	sub := strings.TrimSpace(args)
	if sub == "off" || sub == "stop" {
		if t.live == nil {
			t.dim("remote control is not on")
			return
		}
		t.live.h.Stop()
		t.live = nil
		t.dim("remote control off")
		return
	}
	if t.live != nil {
		t.rcPrintReach(true)
		return
	}
	go func() {
		// The same consent `loop serve` asks for: this exposes the machine to
		// whoever holds the link.
		if !config.LoadSettings().ServeOn() {
			pick := t.choose("Remote control",
				tui.Item{Value: "on", Label: "Turn on remote control", Description: "anyone with the link controls this machine, terminal included"},
				tui.Item{Value: "chat", Label: "Turn on, without the terminal", Description: "paired devices get chat only, no shell here"},
				tui.Item{Value: "no", Label: "Cancel"})
			if pick == nil || (pick.Value != "on" && pick.Value != "chat") {
				return
			}
			_ = config.SettingsStore().Set("serve", true)
			if pick.Value == "chat" {
				_ = config.SettingsStore().Set("serveTerminal", false)
			}
		}
		bind := "0.0.0.0"
		if strings.Contains(sub, "--local") {
			bind = "127.0.0.1"
		}
		terminal := serve.RemoteTerminalFor(strings.Contains(sub, "--terminal") && !strings.Contains(sub, "--no-terminal"),
			strings.Contains(sub, "--no-terminal"), config.LoadSettings().ServeTerminal)
		live := &liveServe{t: t, terminal: terminal}
		h, _ := newRPCHost(t.cfg.CWD)
		h.Live = live
		var lastErr error
		for port := serve.DefaultPort; port < serve.DefaultPort+5 && live.h == nil; port++ {
			// macOS lets 127.0.0.1:N bind beside another process's *:N; skip
			// a port something already answers on.
			if portAnswers(port) {
				continue
			}
			handle, err := serve.Start(serve.Options{Host: bind, Port: port, RemoteTerminal: terminal, Version: version, RPC: h})
			if err != nil {
				lastErr = err
				continue
			}
			live.h = handle
		}
		if live.h == nil {
			t.fail("could not start remote control: %v", lastErr)
			return
		}
		t.live = live
		if len(serve.LANAddresses()) == 0 && bind == "0.0.0.0" {
			t.dim("no network address found — reachable from this machine only")
		}
		t.rcPrintReach(false)
	}()
}

// serveCommand is `pi serve [--host H] [--port N] [--terminal|--no-terminal]`
// — loop serve: the RPC server over WebSocket plus the browser UI.
func serveCommand(cwd string, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	host := fs.String("host", "0.0.0.0", "bind address")
	port := fs.Int("port", serve.DefaultPort, "port")
	terminal := fs.Bool("terminal", false, "offer the terminal to other devices")
	noTerminal := fs.Bool("no-terminal", false, "never offer the terminal to other devices")
	_ = fs.Parse(args)
	if !config.LoadSettings().ServeOn() {
		fmt.Fprintln(os.Stderr, "serve is off — anyone with the link controls this machine. Turn it on in /settings (serve), then run again.")
		os.Exit(1)
	}
	h, rh := newRPCHost(cwd)
	config.ConfigureCatalog()
	handle, err := serve.Start(serve.Options{Host: *host, Port: *port, Version: version, RPC: h,
		RemoteTerminal: serve.RemoteTerminalFor(*terminal, *noTerminal, config.LoadSettings().ServeTerminal)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
	fmt.Printf("loop serve on %s:%d\n\n%s\n\n  local     %s\n", *host, handle.Port, serve.TerminalQR(first(handle.NetworkURLs, handle.URL)), handle.URL)
	for _, u := range handle.NetworkURLs {
		fmt.Printf("  network   %s\n", u)
	}
	code := handle.PairingCode(false)
	fmt.Printf("  code      %s %s  (one use, 5 min)\n\nAnyone with this link fully controls this machine. Ctrl+C stops it.\n", code.Code[:3], code.Code[3:])
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
	handle.Stop()
	rh.close()
	shells.KillAll()
	db.Close()
}

func first(list []string, fallback string) string {
	if len(list) > 0 {
		return list[0]
	}
	return fallback
}
