#!/usr/bin/env bun
import { readFileSync } from "node:fs";
import { join } from "node:path";

const pkg = JSON.parse(readFileSync(join(import.meta.dir, "package.json"), "utf8")) as {
    dependencies?: Record<string, string>;
    peerDependencies?: Record<string, string>;
    optionalDependencies?: Record<string, string>;
};
const externals = [
    ...Object.keys(pkg.dependencies ?? {}),
    ...Object.keys(pkg.peerDependencies ?? {}),
    // optionalDependencies must stay external: claude-agent-sdk resolves its
    // vendored cli.js relative to its own package dir — bundling breaks the spawn.
    ...Object.keys(pkg.optionalDependencies ?? {}),
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

console.log(`✓ built core (${result.outputs.length} files)`);
