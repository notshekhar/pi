# Sandbox — attribution

The code in this directory is **ported from**
[`anthropic-experimental/sandbox-runtime`](https://github.com/anthropic-experimental/sandbox-runtime),
licensed under the **Apache License 2.0**.

We vendor (copy + adapt) it rather than depend on `@anthropic-ai/sandbox-runtime`
so we own the implementation and can modify it freely. Ported files carry a
header pointing back here.

## Divergences from upstream

This is a staged port.

### Stage 1 — filesystem isolation (done, macOS-tested)

- `platform.ts` — platform / WSL detection (verbatim).
- `sandbox-utils.ts` — path normalization, glob helpers, default write paths,
  proxy env vars, dangerous-file lists (verbatim subset).
- `sandbox-schemas.ts` — filesystem/network restriction config types (verbatim).
- `macos-sandbox-utils.ts` — Seatbelt profile generation + `sandbox-exec`
  command wrapping (verbatim, minus the log-stream violation monitor).
- `linux-sandbox-utils.ts` — bubblewrap filesystem isolation + network on/off
  via `--unshare-net`. The bwrap invocation is **UNVERIFIED** (needs a Linux box).
- `index.ts` — our own `SandboxManager` runtime over the above.

### Stage 2 — network domain allowlist (done on macOS, tested)

- `net-utils.ts` — host validation / canonicalization / direct dial (subset of
  upstream `parent-proxy.ts`).
- `domain-filter.ts` — allow/deny pattern matching (from upstream manager).
- `http-proxy.ts` — filtered forward proxy (CONNECT tunnel + plain HTTP).
- `socks-proxy.ts` — filtered SOCKS5 proxy (via `@pondwader/socks5-server`).
- Wired in `index.ts`: `network: { allow, deny }` starts the proxies lazily,
  bakes their ports into the Seatbelt profile + proxy env vars, and live-swaps
  rules per command.

Intentionally omitted from the network stack: MITM / TLS termination,
upstream/corporate parent-proxy chaining, the per-session proxy-auth token, and
the per-request `filterRequest` callback. Host-level allow/deny is fully
enforced without them.

### Stage 3 — Linux network bridge + seccomp + Windows (written, UNVERIFIED)

- `linux-sandbox-utils.ts` — now includes `initializeLinuxNetworkBridge` (host
  `socat` `UNIX-LISTEN → TCP:proxy`) and binds the sockets into the namespace
  with an inner `socat` re-export + proxy env, so the Linux network allowlist is
  wired. **Cannot be exercised on macOS** — must be tested on a Linux box.
- `seccomp.ts` + `vendor/seccomp-src/*.c` + `vendor/seccomp/build.ts` — the BPF
  loader is vendored. `bun run build:seccomp` (Linux, needs `gcc` +
  `libseccomp-dev`) compiles `apply-seccomp`; once present the Linux wrapper
  applies it automatically. Inert (skipped) when not built.
- `windows-sandbox-utils.ts` — honest **NOT-IMPLEMENTED** stub. Windows needs
  the native WFP `srt-win` component (not vendored); `isSandboxSupported()`
  returns false on Windows so this path is never taken by default.

### Still not done

- Violation monitoring (`sandbox-violation-store`, macOS `log stream`).
- MITM/TLS termination, upstream parent-proxy, proxy-auth token, `filterRequest`
  (intentionally omitted from the network stack).

A copy of the upstream Apache-2.0 LICENSE accompanies this notice.
