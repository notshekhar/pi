#!/usr/bin/env bun
import { readFileSync, chmodSync } from "node:fs";
import { join } from "node:path";

const pkg = JSON.parse(readFileSync(join(import.meta.dir, "package.json"), "utf8")) as { version: string };
// Embedded so release binaries (single-file bun --compile, no CHANGELOG.md
// on disk) can still serve /changelog and the what's-new banner.
const changelog = readFileSync(join(import.meta.dir, "CHANGELOG.md"), "utf8");

const result = await Bun.build({
  entrypoints: [join(import.meta.dir, "src/cli.ts")],
  outdir: join(import.meta.dir, "dist"),
  target: "node",
  format: "esm",
  // keep dynamic imports as separate chunks so `pi --version` doesn't eval the TUI stack
  splitting: true,
  minify: { whitespace: true, identifiers: false, syntax: true },
  external: ["@notshekhar/pi-core", "@notshekhar/pi-tui", "chalk", "highlight.js"],
  banner: "#!/usr/bin/env node",
  define: { __PI_VERSION__: JSON.stringify(pkg.version), __PI_CHANGELOG__: JSON.stringify(changelog) },
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}

chmodSync(join(import.meta.dir, "dist/cli.js"), 0o755);
console.log("✓ built cli");
