#!/usr/bin/env bun
// Compile pi to a single bun binary + bundle the assets that
// @earendil-works/pi-coding-agent expects to live next to it.
//
// Usage:
//   bun scripts/compile.ts                         # current host target
//   bun scripts/compile.ts bun-linux-x64-modern    # explicit bun target
//   bun scripts/compile.ts --all                   # all release targets
//
// Output layout: out/<target>/{pi, package.json, theme/, assets/, export-html/}

import { existsSync, mkdirSync, rmSync } from "node:fs";
import { cp, readFile, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { $ } from "bun";

const ROOT = resolve(import.meta.dir, "..");
const ENTRY = join(ROOT, "packages/cli/src/cli.ts");
const OUT_DIR = join(ROOT, "out");

const ALL_TARGETS = [
  "bun-linux-x64-modern",
  "bun-linux-arm64",
  "bun-darwin-x64",
  "bun-darwin-arm64",
  "bun-windows-x64-modern",
] as const;

function osArchFromBunTarget(t: string): string {
  // bun-darwin-arm64 → darwin-arm64; trims modern suffix
  return t.replace(/^bun-/, "").replace(/-modern$/, "");
}

function resolvePiCodingAgentDir(): string {
  // Walk into the bun-style node_modules layout and find the latest pi-coding-agent.
  const buns = join(ROOT, "node_modules/.bun");
  if (existsSync(buns)) {
    const candidates = require("node:fs")
      .readdirSync(buns)
      .filter((d: string) => d.startsWith("@earendil-works+pi-coding-agent@"));
    if (candidates.length) {
      const dir = join(buns, candidates[0], "node_modules/@earendil-works/pi-coding-agent");
      if (existsSync(join(dir, "package.json"))) return dir;
    }
  }
  // Fallback: flat layout (npm/yarn).
  const flat = join(ROOT, "node_modules/@earendil-works/pi-coding-agent");
  if (existsSync(join(flat, "package.json"))) return flat;
  throw new Error("could not locate @earendil-works/pi-coding-agent in node_modules");
}

async function buildOne(target: string, version: string): Promise<void> {
  const outDir = join(OUT_DIR, target);
  rmSync(outDir, { recursive: true, force: true });
  mkdirSync(outDir, { recursive: true });

  const isWindows = target.includes("windows");
  const exe = isWindows ? "pi.exe" : "pi";
  const outFile = join(outDir, exe);

  console.log(`▶ Compiling ${target}`);
  await $`bun build --compile --minify \
    --target=${target} \
    --define __PI_VERSION__="\"${version}\"" \
    ${ENTRY} --outfile ${outFile}`;

  // pi-coding-agent expects package.json + theme/ + assets/ + export-html/
  // next to process.execPath when running as a bun-compiled binary.
  const pkgDir = resolvePiCodingAgentDir();
  const distDir = join(pkgDir, "dist");

  console.log("  · copying pi-coding-agent assets");
  await writeFile(
    join(outDir, "package.json"),
    JSON.stringify({ name: "pi", version, type: "module" }, null, 2) + "\n",
  );
  await cp(join(distDir, "modes/interactive/theme"), join(outDir, "theme"), { recursive: true });
  await cp(join(distDir, "modes/interactive/assets"), join(outDir, "assets"), { recursive: true });
  await cp(join(distDir, "core/export-html"), join(outDir, "export-html"), { recursive: true });

  console.log(`  · ${outFile}`);
}

async function main() {
  if (!existsSync(OUT_DIR)) mkdirSync(OUT_DIR, { recursive: true });

  const cliVersion = JSON.parse(
    await readFile(join(ROOT, "packages/cli/package.json"), "utf8"),
  ).version as string;

  const args = process.argv.slice(2);
  let targets: readonly string[];
  if (args.includes("--all")) {
    targets = ALL_TARGETS;
  } else if (args.length > 0) {
    targets = args.filter((a) => !a.startsWith("--"));
    if (targets.length === 0) targets = ALL_TARGETS;
  } else {
    targets = ["bun"]; // host target
  }

  for (const t of targets) {
    await buildOne(t, cliVersion);
  }
  console.log("✓ done");
  // Surface os-arch names so the packaging step can iterate.
  console.log(targets.map((t) => (t === "bun" ? "host" : osArchFromBunTarget(t))).join(","));
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
