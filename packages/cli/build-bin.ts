#!/usr/bin/env bun
// Standalone binary build via `bun build --compile`.
// Bundles ALL deps (no externals) for the current platform.
//
// Output: dist/bin/<target>/ containing `loop` (or `loop.exe`) and `package.json`,
// plus dist/bin/loop-<target>.tar.gz tarball ready to upload to GH Releases.

import { readFileSync, mkdirSync, existsSync, rmSync, copyFileSync } from "node:fs";
import { join } from "node:path";
import { $ } from "bun";

const pkg = JSON.parse(readFileSync(join(import.meta.dir, "package.json"), "utf8")) as { version: string };
// Embedded so the standalone binary can serve /changelog without a file on disk.
const changelog = readFileSync(join(import.meta.dir, "CHANGELOG.md"), "utf8");

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
        platform === "darwin" ? "darwin" : platform === "linux" ? "linux" : platform === "win32" ? "windows" : null;
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
const binPath = join(stageDir, `loop${ext}`);
const pkgJsonPath = join(stageDir, "package.json");

if (existsSync(stageDir)) rmSync(stageDir, { recursive: true });
mkdirSync(stageDir, { recursive: true });

console.log(`▶ building ${binPath} (v${pkg.version})`);

await $`bun build ${join(import.meta.dir, "src/cli.ts")} \
  --compile \
  --target=${target} \
  --minify \
  --define __LOOP_VERSION__=${JSON.stringify(pkg.version)} \
  --define __LOOP_CHANGELOG__=${JSON.stringify(changelog)} \
  --outfile ${binPath}`;

// Ship package.json alongside the binary — version metadata for installers.
copyFileSync(join(import.meta.dir, "package.json"), pkgJsonPath);

// Tarball for release. Windows users still get .tar.gz; install.sh expands it.
// chdir + relative paths so neither GNU tar (D:\... → tries host:path) nor
// bsdtar (no --force-local flag) trip on Windows absolute paths.
const tarball = join(import.meta.dir, "dist", "bin", `loop-${shortTarget}.tar.gz`);
const binDir = join(import.meta.dir, "dist", "bin");
const tarballRel = `loop-${shortTarget}.tar.gz`;
if (existsSync(tarball)) rmSync(tarball);
await $`tar -czf ${tarballRel} ${shortTarget}`.cwd(binDir);

console.log(`✓ built ${binPath}`);
console.log(`✓ packaged ${tarball}`);
