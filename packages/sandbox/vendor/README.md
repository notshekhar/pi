# Vendored native sources (Linux seccomp)

`seccomp-src/*.c` and `seccomp/build.ts` are copied verbatim from
[`anthropic-experimental/sandbox-runtime`](https://github.com/anthropic-experimental/sandbox-runtime)
(Apache-2.0). They are **data/source**, not compiled by our TypeScript build.

## Building (Linux only)

Seccomp adds a syscall-filter layer on top of bubblewrap. It must be compiled
on a Linux host with a C toolchain:

```bash
# requires: gcc, libseccomp-dev
cd packages/sandbox
bun run build:seccomp
```

This compiles `apply-seccomp` into `vendor/seccomp/<arch>/`. Once present,
`src/seccomp.ts` detects it and the Linux sandbox wraps commands with it
automatically. Until built, the sandbox runs **without** the seccomp layer
(bubblewrap filesystem + the socat network bridge still apply).

> ⚠️ UNVERIFIED in this repo: cannot be compiled or exercised on macOS. Verify
> on a real Linux box before relying on the seccomp layer.

Windows isolation (`srt-win`, a WFP-based Rust crate) is **not** vendored — it
is a separate native component requiring MSVC. See upstream `vendor/srt-win`.
