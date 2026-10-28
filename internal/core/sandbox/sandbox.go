// Package sandbox is loop's @notshekhar/loop-sandbox (packages/sandbox): the
// OS sandbox for the bash tool — Seatbelt (sandbox-exec) on macOS,
// bubblewrap on Linux — itself ported from
// anthropic-experimental/sandbox-runtime (Apache-2.0).
//
// Filesystem isolation is a pure command wrap. loop's per-domain network
// allowlist (host-side HTTP/SOCKS proxies) is not ported: an allowlist is
// enforced as "deny", which is stricter, and the caller is told so.
package sandbox

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Network is the network policy: "allow", "deny", or an allowlist.
type Network struct {
	Mode  string // allow | deny | list
	Allow []string
	Deny  []string
}

// Config is loop's SandboxConfig.
type Config struct {
	AllowWrite     []string
	DenyWrite      []string
	DenyRead       []string
	AllowRead      []string
	AllowGitConfig bool
	ReadOnly       bool
	Network        Network
}

// Supported is loop's isSandboxSupported.
func Supported() bool {
	switch runtime.GOOS {
	case "darwin":
		return true
	case "linux":
		return wslVersion() != "1"
	}
	return false
}

func wslVersion() string {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return ""
	}
	if m := regexp.MustCompile(`(?i)WSL(\d+)`).FindStringSubmatch(string(data)); m != nil {
		return m[1]
	}
	if strings.Contains(strings.ToLower(string(data)), "microsoft") {
		return "1"
	}
	return ""
}

// CheckDependencies reports missing tooling.
func CheckDependencies() []string {
	if !Supported() {
		return []string{"Unsupported platform"}
	}
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("bwrap"); err != nil {
			return []string{"bubblewrap (bwrap) not found"}
		}
	}
	return nil
}

// ─── shared helpers (sandbox-utils.ts) ───────────────────────────────────

var dangerousFiles = []string{".gitconfig", ".gitmodules", ".bashrc", ".bash_profile", ".zshrc", ".zprofile", ".profile", ".ripgreprc", ".mcp.json"}

func dangerousDirectories() []string {
	return []string{".vscode", ".idea", ".claude/commands", ".claude/agents"}
}

func containsGlobChars(p string) bool { return strings.ContainsAny(p, "*?[]") }

func isSymlinkOutsideBoundary(original, resolved string) bool {
	o, r := filepath.Clean(original), filepath.Clean(resolved)
	if r == o {
		return false
	}
	if strings.HasPrefix(o, "/tmp/") && r == "/private"+o {
		return false
	}
	if strings.HasPrefix(o, "/var/") && r == "/private"+o {
		return false
	}
	if r == "/" {
		return true
	}
	if len(strings.FieldsFunc(r, func(c rune) bool { return c == '/' })) <= 1 {
		return true
	}
	if strings.HasPrefix(o, r+"/") {
		return true
	}
	canonical := o
	if strings.HasPrefix(o, "/tmp/") || strings.HasPrefix(o, "/var/") {
		canonical = "/private" + o
	}
	if canonical != o && strings.HasPrefix(canonical, r+"/") {
		return true
	}
	startsOriginal := strings.HasPrefix(r, o+"/")
	startsCanonical := canonical != o && strings.HasPrefix(r, canonical+"/")
	isCanonical := canonical != o && r == canonical
	return !isCanonical && !startsOriginal && !startsCanonical
}

// normalizePath is normalizePathForSandbox.
func normalizePath(p string) string {
	cwd, _ := os.Getwd()
	h, _ := os.UserHomeDir()
	n := p
	switch {
	case p == "~":
		n = h
	case strings.HasPrefix(p, "~/"):
		n = h + p[1:]
	case !filepath.IsAbs(p):
		n = filepath.Join(cwd, p)
	}
	if containsGlobChars(n) {
		prefix := regexp.MustCompile(`[*?\[\]]`).Split(n, 2)[0]
		if prefix != "" && prefix != "/" {
			base := filepath.Dir(prefix)
			if strings.HasSuffix(prefix, "/") {
				base = strings.TrimSuffix(prefix, "/")
			}
			if real, err := filepath.EvalSymlinks(base); err == nil && !isSymlinkOutsideBoundary(base, real) {
				return real + n[len(base):]
			}
		}
		return n
	}
	if real, err := filepath.EvalSymlinks(n); err == nil && !isSymlinkOutsideBoundary(n, real) {
		n = real
	}
	return n
}

func defaultWritePaths() []string {
	h, _ := os.UserHomeDir()
	return []string{"/dev/stdout", "/dev/stderr", "/dev/null", "/dev/tty", "/dev/dtracehelper", "/dev/autofs_nowait",
		"/tmp/claude", "/private/tmp/claude", filepath.Join(h, ".npm/_logs"), filepath.Join(h, ".claude/debug")}
}

func proxyEnvVars() []string {
	tmp := os.Getenv("LOOP_SANDBOX_TMPDIR")
	if tmp == "" {
		tmp = os.Getenv("CLAUDE_CODE_TMPDIR")
	}
	if tmp == "" {
		tmp = "/tmp/claude"
	}
	return []string{"SANDBOX_RUNTIME=1", "TMPDIR=" + tmp}
}

var (
	globEscape  = regexp.MustCompile(`[.^$+{}()|\\]`)
	openBracket = regexp.MustCompile(`\[([^\]]*?)$`)
)

// globToRegex is loop's gitignore-style glob → regex.
func globToRegex(g string) string {
	s := globEscape.ReplaceAllStringFunc(g, func(m string) string { return `\` + m })
	s = openBracket.ReplaceAllString(s, `\[$1`)
	s = strings.ReplaceAll(s, "**/", "__GLOBSTAR_SLASH__")
	s = strings.ReplaceAll(s, "**", "__GLOBSTAR__")
	s = strings.ReplaceAll(s, "*", "[^/]*")
	s = strings.ReplaceAll(s, "?", "[^/]")
	s = strings.ReplaceAll(s, "__GLOBSTAR_SLASH__", "(.*/)?")
	s = strings.ReplaceAll(s, "__GLOBSTAR__", ".*")
	return "^" + s + "$"
}

// ─── shell-quote's quote ─────────────────────────────────────────────────

var (
	needsSingle  = regexp.MustCompile(`["\s\\]`)
	needsDouble  = regexp.MustCompile(`["'\s]`)
	doubleEscape = regexp.MustCompile("([\"\\\\$`!])")
	bareEscape   = regexp.MustCompile("([A-Za-z]:)?([#!\"$&'()*,:;<=>?@\\[\\\\\\]^`{|}~])")
)

// Quote is shell-quote's quote(), byte for byte.
func Quote(args []string) string {
	out := make([]string, len(args))
	for i, s := range args {
		switch {
		case s == "":
			out[i] = "''"
		case needsSingle.MatchString(s) && !strings.Contains(s, "'"):
			out[i] = "'" + s + "'"
		case needsDouble.MatchString(s):
			out[i] = `"` + doubleEscape.ReplaceAllString(s, `\$1`) + `"`
		default:
			out[i] = bareEscape.ReplaceAllString(s, `$1\$2`)
		}
	}
	return strings.Join(out, " ")
}

// ─── macOS (macos-sandbox-utils.ts) ──────────────────────────────────────

func escapePath(p string) string {
	b := strings.Builder{}
	b.WriteByte('"')
	for _, r := range p {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func ancestors(p string) []string {
	var out []string
	cur := filepath.Dir(p)
	for cur != "/" && cur != "." {
		out = append(out, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return out
}

func mandatoryDenyPatterns(allowGitConfig bool) []string {
	cwd, _ := os.Getwd()
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, f := range dangerousFiles {
		add(filepath.Join(cwd, f))
		add("**/" + f)
	}
	for _, d := range dangerousDirectories() {
		add(filepath.Join(cwd, d))
		add("**/" + d + "/**")
	}
	add(filepath.Join(cwd, ".git/hooks"))
	add("**/.git/hooks/**")
	if !allowGitConfig {
		add(filepath.Join(cwd, ".git/config"))
		add("**/.git/config")
	}
	return out
}

var globSplit = regexp.MustCompile(`[*?\[\]]`)

func moveBlockingRules(patterns []string, tag string) []string {
	var rules []string
	ops := []string{"file-write-unlink", "file-write-create"}
	for _, pat := range patterns {
		n := normalizePath(pat)
		if containsGlobChars(n) {
			for _, op := range ops {
				rules = append(rules, "(deny "+op, "  (regex "+escapePath(globToRegex(n))+")", `  (with message "`+tag+`"))`)
			}
			prefix := globSplit.Split(n, 2)[0]
			if prefix != "" && prefix != "/" {
				base := filepath.Dir(prefix)
				if strings.HasSuffix(prefix, "/") {
					base = strings.TrimSuffix(prefix, "/")
				}
				for _, op := range ops {
					rules = append(rules, "(deny "+op, "  (literal "+escapePath(base)+")", `  (with message "`+tag+`"))`)
				}
				for _, a := range ancestors(base) {
					for _, op := range ops {
						rules = append(rules, "(deny "+op, "  (literal "+escapePath(a)+")", `  (with message "`+tag+`"))`)
					}
				}
			}
			continue
		}
		for _, op := range ops {
			rules = append(rules, "(deny "+op, "  (subpath "+escapePath(n)+")", `  (with message "`+tag+`"))`)
		}
		for _, a := range ancestors(n) {
			for _, op := range ops {
				rules = append(rules, "(deny "+op, "  (literal "+escapePath(a)+")", `  (with message "`+tag+`"))`)
			}
		}
	}
	return rules
}

func pathRule(action, pattern, tag string) []string {
	n := normalizePath(pattern)
	if containsGlobChars(n) {
		return []string{"(" + action, "  (regex " + escapePath(globToRegex(n)) + ")", `  (with message "` + tag + `"))`}
	}
	return []string{"(" + action, "  (subpath " + escapePath(n) + ")", `  (with message "` + tag + `"))`}
}

func readRules(denyOnly, allowWithin, writeAllow []string, tag string) []string {
	rules := []string{"(allow file-read*)"}
	deniesRoot := false
	for _, p := range denyOnly {
		if normalizePath(p) == "/" {
			deniesRoot = true
		}
		rules = append(rules, pathRule("deny file-read*", p, tag)...)
	}
	if deniesRoot {
		rules = append(rules, `(allow file-read* (literal "/"))`)
	}
	var allowed []string
	for _, p := range allowWithin {
		n := normalizePath(p)
		if !containsGlobChars(n) {
			allowed = append(allowed, n)
		}
		rules = append(rules, pathRule("allow file-read*", p, tag)...)
	}
	for _, d := range denyOnly {
		if containsGlobChars(d) {
			continue
		}
		n := normalizePath(d)
		for _, a := range allowed {
			if strings.HasPrefix(n, a+"/") {
				rules = append(rules, "(deny file-read*", "  (subpath "+escapePath(n)+")", `  (with message "`+tag+`"))`)
				break
			}
		}
	}
	if len(denyOnly) > 0 {
		rules = append(rules, "(allow file-read-metadata", "  (vnode-type DIRECTORY))")
	}
	rules = append(rules, moveBlockingRules(denyOnly, tag)...)
	for _, p := range writeAllow {
		for _, op := range []string{"file-write-unlink", "file-write-create"} {
			rules = append(rules, pathRule("allow "+op, p, tag)...)
		}
	}
	return rules
}

func writeRules(allowOnly, denyWithin []string, allowGitConfig bool, tag string) []string {
	var rules []string
	for _, p := range allowOnly {
		rules = append(rules, pathRule("allow file-write*", p, tag)...)
	}
	deny := append(append([]string{}, denyWithin...), mandatoryDenyPatterns(allowGitConfig)...)
	for _, p := range deny {
		rules = append(rules, pathRule("deny file-write*", p, tag)...)
	}
	return append(rules, moveBlockingRules(deny, tag)...)
}

var sessionSuffix = fmt.Sprintf("_%09x_SBX", rand.Int63()&0xfffffffff)

func logTag(command string) string {
	head := command
	if len(head) > 100 {
		head = head[:100]
	}
	return "CMD64_" + base64.StdEncoding.EncodeToString([]byte(head)) + "_END_" + sessionSuffix
}

func macProfile(cfg Config, cwd string, needsNet bool, tag string) string {
	p := []string{"(version 1)", `(deny default (with message "` + tag + `"))`, "", "; LogTag: " + tag, "",
		"; Process permissions", "(allow process-exec)", "(allow process-fork)",
		"(allow process-info* (target same-sandbox))", "(allow signal (target same-sandbox))",
		"(allow mach-priv-task-port (target same-sandbox))", "", "(allow user-preference-read)", "",
		"; Mach IPC - specific services only (no wildcard)", "(allow mach-lookup"}
	for _, n := range []string{"com.apple.audio.systemsoundserver", "com.apple.distributed_notifications@Uv3",
		"com.apple.FontObjectsServer", "com.apple.fonts", "com.apple.logd", "com.apple.lsd.mapdb",
		"com.apple.PowerManagement.control", "com.apple.system.logger", "com.apple.system.notification_center",
		"com.apple.system.opendirectoryd.libinfo", "com.apple.system.opendirectoryd.membership",
		"com.apple.bsd.dirhelper", "com.apple.securityd.xpc", "com.apple.coreservices.launchservicesd"} {
		p = append(p, `  (global-name "`+n+`")`)
	}
	p = append(p, ")", "", "",
		"; POSIX IPC - shared memory + semaphores (Python multiprocessing)", "(allow ipc-posix-shm)", "(allow ipc-posix-sem)", "",
		"; IOKit - specific operations only", "(allow iokit-open", `  (iokit-registry-entry-class "IOSurfaceRootUserClient")`,
		`  (iokit-registry-entry-class "RootDomainUserClient")`, `  (iokit-user-client-class "IOSurfaceSendRight")`, ")",
		"(allow iokit-get-properties)", "", "; Safe system-sockets (no network access)",
		"(allow system-socket (require-all (socket-domain AF_SYSTEM) (socket-protocol 2)))", "",
		"; sysctl - read a curated set", "(allow sysctl-read")
	for _, n := range []string{"hw.activecpu", "hw.busfrequency_compat", "hw.byteorder", "hw.cacheconfig",
		"hw.cachelinesize_compat", "hw.cpufamily", "hw.cpufrequency", "hw.cpufrequency_compat", "hw.cputype",
		"hw.l1dcachesize_compat", "hw.l1icachesize_compat", "hw.l2cachesize_compat", "hw.l3cachesize_compat",
		"hw.logicalcpu", "hw.logicalcpu_max", "hw.machine", "hw.memsize", "hw.ncpu", "hw.nperflevels", "hw.packages",
		"hw.pagesize_compat", "hw.pagesize", "hw.physicalcpu", "hw.physicalcpu_max", "hw.tbfrequency_compat",
		"hw.vectorunit", "kern.argmax", "kern.bootargs", "kern.hostname", "kern.maxfiles", "kern.maxfilesperproc",
		"kern.maxproc", "kern.ngroups", "kern.osproductversion", "kern.osrelease", "kern.ostype", "kern.osvariant_status",
		"kern.osversion", "kern.secure_kernel", "kern.tcsm_available", "kern.tcsm_enable", "kern.usrstack64",
		"kern.version", "kern.willshutdown", "machdep.cpu.brand_string", "machdep.ptrauth_enabled",
		"security.mac.lockdown_mode_state", "sysctl.proc_cputype", "vm.loadavg"} {
		p = append(p, `  (sysctl-name "`+n+`")`)
	}
	for _, n := range []string{"hw.optional.arm", "hw.optional.arm.", "hw.optional.armv8_", "hw.perflevel",
		"kern.proc.all", "kern.proc.pgrp.", "kern.proc.pid.", "machdep.cpu.", "net.routetable."} {
		p = append(p, `  (sysctl-name-prefix "`+n+`")`)
	}
	p = append(p, ")", "", "(allow sysctl-write", `  (sysctl-name "kern.tcsm_enable")`, ")", "",
		"(allow distributed-notification-post)", "", `(allow mach-lookup (global-name "com.apple.SecurityServer"))`, "",
		"; File I/O on device files")
	for _, d := range []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/dtracehelper", "/dev/tty"} {
		p = append(p, `(allow file-ioctl (literal "`+d+`"))`)
	}
	p = append(p, "", "(allow file-ioctl file-read-data file-write-data", "  (require-all", `    (literal "/dev/null")`,
		"    (vnode-type CHARACTER-DEVICE)", "  )", ")", "", "; Network")
	if !needsNet {
		p = append(p, "(allow network*)")
	}
	p = append(p, "")
	writeAllow, denyWithin := writePaths(cfg, cwd)
	p = append(p, "; File read")
	p = append(p, readRules(cfg.DenyRead, cfg.AllowRead, writeAllow, tag)...)
	p = append(p, "", "; File write")
	p = append(p, writeRules(writeAllow, denyWithin, cfg.AllowGitConfig, tag)...)
	return strings.Join(p, "\n")
}

func writePaths(cfg Config, cwd string) (allowOnly, denyWithin []string) {
	if cfg.ReadOnly {
		return defaultWritePaths(), nil
	}
	return append(append(defaultWritePaths(), cwd), cfg.AllowWrite...), cfg.DenyWrite
}

// ─── Linux (linux-sandbox-utils.ts) ──────────────────────────────────────

func bwrapArgs(cfg Config, cwd string, needsNet bool) []string {
	args := []string{"--new-session", "--die-with-parent"}
	if needsNet {
		args = append(args, "--unshare-net")
	}
	allowOnly, denyWithin := writePaths(cfg, cwd)
	args = append(args, "--ro-bind", "/", "/")
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	for _, raw := range allowOnly {
		if p := normalizePath(raw); !strings.HasPrefix(p, "/dev/") && exists(p) {
			args = append(args, "--bind", p, p)
		}
	}
	for _, raw := range denyWithin {
		if p := normalizePath(raw); !strings.HasPrefix(p, "/dev/") && exists(p) {
			args = append(args, "--ro-bind", p, p)
		}
	}
	for _, raw := range cfg.DenyRead {
		p := normalizePath(raw)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.IsDir() {
			args = append(args, "--tmpfs", p)
		} else {
			args = append(args, "--ro-bind", "/dev/null", p)
		}
	}
	for _, raw := range cfg.AllowRead {
		if p := normalizePath(raw); exists(p) {
			args = append(args, "--ro-bind", p, p)
		}
	}
	return append(args, "--dev", "/dev", "--unshare-pid", "--proc", "/proc")
}

// ─── wrap ────────────────────────────────────────────────────────────────

// Wrapped is the spawn argv, plus a warning when part of the config could not
// be honoured as written.
type Wrapped struct {
	Argv    []string
	Warning string
}

// Wrap is sandbox.wrap: the command made to run under the OS sandbox.
func Wrap(command, shell, cwd string, cfg Config) (Wrapped, error) {
	if !Supported() {
		return Wrapped{}, errors.New("unsupported platform")
	}
	needsNet := cfg.Network.Mode != "allow"
	var warning string
	if cfg.Network.Mode == "list" {
		warning = "the per-domain network allowlist is not supported by pi yet — network is denied entirely"
	}
	var wrapped string
	if runtime.GOOS == "darwin" {
		tag := logTag(command)
		profile := macProfile(cfg, cwd, needsNet, tag)
		args := append([]string{"env"}, proxyEnvVars()...)
		args = append(args, "/usr/bin/sandbox-exec", "-p", profile, shell, "-c", command)
		wrapped = Quote(args)
	} else {
		bwrap, err := exec.LookPath("bwrap")
		if err != nil {
			return Wrapped{}, errors.New("bubblewrap (bwrap) not found")
		}
		inner := shell + " -c " + Quote([]string{"eval " + Quote([]string{command})})
		wrapped = Quote(append(append([]string{bwrap}, bwrapArgs(cfg, cwd, needsNet)...), "--", "/bin/sh", "-c", inner))
	}
	return Wrapped{Argv: []string{shell, "-c", wrapped}, Warning: warning}, nil
}
