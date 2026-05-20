#!/usr/bin/env bun
import { readFileSync } from "node:fs";
import { join } from "node:path";

const pkg = JSON.parse(readFileSync(join(import.meta.dir, "package.json"), "utf8")) as {
  dependencies?: Record<string, string>;
  peerDependencies?: Record<string, string>;
};
const externals = [
  ...Object.keys(pkg.dependencies ?? {}),
  ...Object.keys(pkg.peerDependencies ?? {}),
  "node:*",
];

const result = await Bun.build({
  entrypoints: [join(import.meta.dir, "src/index.ts")],
  outdir: join(import.meta.dir, "dist"),
  target: "node",
  format: "esm",
  minify: { whitespace: true, identifiers: false, syntax: true },
  external: externals,
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}

// Emit .d.ts via tsc so consumers of @notshekhar/pi-core get full types.
// (Bun.build does not produce declaration files.)
const tsc = Bun.spawnSync({
  cmd: ["bunx", "tsc", "--emitDeclarationOnly", "--declaration", "--outDir", "dist",
        "--rootDir", "src", "--target", "ES2022", "--module", "ESNext",
        "--moduleResolution", "Bundler", "--esModuleInterop", "--resolveJsonModule",
        "--skipLibCheck", "--types", "bun,node", "src/index.ts"],
  cwd: import.meta.dir,
  stdout: "inherit",
  stderr: "inherit",
});
if (tsc.exitCode !== 0) {
  console.error("tsc declaration emit failed");
  process.exit(1);
}

console.log(`✓ built core (${result.outputs.length} files + .d.ts)`);
