#!/usr/bin/env bun
// Standalone binary build via `bun build --compile`.
// Bundles ALL deps (no externals) + native modules for current platform.
// Cross-compile not supported because pi pulls in NAPI native modules
// (@mariozechner/clipboard, koffi) that ship platform-specific .node files —
// build per target on a matching CI runner.
//
// Output: dist/bin/<target>/ containing `pi` (or `pi.exe`) and `package.json`,
// plus dist/bin/pi-<target>.tar.gz tarball ready to upload to GH Releases.
//
// pi-coding-agent's config.js calls `dirname(process.execPath)` to find
// package.json at runtime — so it MUST sit next to the binary.

import { readFileSync, mkdirSync, existsSync, rmSync, copyFileSync } from "node:fs";
import { join } from "node:path";
import { $ } from "bun";

const pkg = JSON.parse(readFileSync(join(import.meta.dir, "package.json"), "utf8")) as { version: string };

const VALID_TARGETS = new Set([
  "bun-darwin-arm64",
  "bun-darwin-x64",
  "bun-linux-x64",
  "bun-linux-arm64",
  "bun-windows-x64",
]);

function currentTarget(): string {
  const platform = process.platform;
  const arch = process.arch;
  const os =
    platform === "darwin" ? "darwin" :
    platform === "linux" ? "linux" :
    platform === "win32" ? "windows" :
    null;
  if (!os) throw new Error(`Unsupported platform: ${platform}`);
  const a = arch === "arm64" ? "arm64" : arch === "x64" ? "x64" : null;
  if (!a) throw new Error(`Unsupported arch: ${arch}`);
  return `bun-${os}-${a}`;
}

const argTarget = process.argv[2];
const target = argTarget ?? currentTarget();
if (!VALID_TARGETS.has(target)) {
  console.error(`Invalid target: ${target}. Valid: ${[...VALID_TARGETS].join(", ")}`);
  process.exit(1);
}

const shortTarget = target.replace("bun-", "");
const isWin = target.includes("windows");
const ext = isWin ? ".exe" : "";

const stageDir = join(import.meta.dir, "dist", "bin", shortTarget);
const binPath = join(stageDir, `pi${ext}`);
const pkgJsonPath = join(stageDir, "package.json");

if (existsSync(stageDir)) rmSync(stageDir, { recursive: true });
mkdirSync(stageDir, { recursive: true });

console.log(`▶ building ${binPath} (v${pkg.version})`);

await $`bun build ${join(import.meta.dir, "src/cli.ts")} \
  --compile \
  --target=${target} \
  --minify \
  --define __PI_VERSION__=${JSON.stringify(pkg.version)} \
  --outfile ${binPath}`;

// Ship package.json alongside binary — pi-coding-agent reads it at runtime
// via dirname(process.execPath).
copyFileSync(join(import.meta.dir, "package.json"), pkgJsonPath);

// Tarball for release. Windows users still get .tar.gz; install.sh expands it.
const tarball = join(import.meta.dir, "dist", "bin", `pi-${shortTarget}.tar.gz`);
if (existsSync(tarball)) rmSync(tarball);
await $`tar -czf ${tarball} -C ${join(import.meta.dir, "dist", "bin")} ${shortTarget}`;

console.log(`✓ built ${binPath}`);
console.log(`✓ packaged ${tarball}`);
