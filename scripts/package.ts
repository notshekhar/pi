#!/usr/bin/env bun
// Pack each out/<target>/ directory into pi-<version>-<os>-<arch>.tar.gz
// plus a .sha256, ready for upload to a GitHub release.

import { existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { join, resolve } from "node:path";
import { $ } from "bun";

const ROOT = resolve(import.meta.dir, "..");
const OUT_DIR = join(ROOT, "out");
const REL_DIR = join(ROOT, "release");

const version = JSON.parse(
  readFileSync(join(ROOT, "packages/cli/package.json"), "utf8"),
).version as string;

rmSync(REL_DIR, { recursive: true, force: true });
mkdirSync(REL_DIR, { recursive: true });

function sha256(file: string): string {
  return createHash("sha256").update(readFileSync(file)).digest("hex");
}

const entries = readdirSync(OUT_DIR).filter((d) => {
  const p = join(OUT_DIR, d);
  return statSync(p).isDirectory();
});

if (entries.length === 0) {
  console.error("no targets in out/ — run `bun run compile --all` first");
  process.exit(1);
}

for (const dir of entries) {
  const target = dir === "bun" ? "host" : dir.replace(/^bun-/, "").replace(/-modern$/, "");
  const tarName = `pi-v${version}-${target}.tar.gz`;
  const tarPath = join(REL_DIR, tarName);
  const sumPath = `${tarPath}.sha256`;
  console.log(`▶ ${tarName}`);
  // -C into out/<dir> so the tarball contains pi + theme/ + assets/ at the top.
  await $`tar -czf ${tarPath} -C ${join(OUT_DIR, dir)} .`;
  writeFileSync(sumPath, `${sha256(tarPath)}  ${tarName}\n`);
}

console.log(`✓ ${entries.length} archive(s) in ${REL_DIR}`);
