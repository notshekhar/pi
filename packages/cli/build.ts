#!/usr/bin/env bun
import { readFileSync, chmodSync } from "node:fs";
import { join } from "node:path";

const pkg = JSON.parse(readFileSync(join(import.meta.dir, "package.json"), "utf8")) as { version: string };

const result = await Bun.build({
  entrypoints: [join(import.meta.dir, "src/cli.ts")],
  outdir: join(import.meta.dir, "dist"),
  target: "node",
  format: "esm",
  // keep dynamic imports as separate chunks so `pi --version` doesn't eval the TUI stack
  splitting: true,
  minify: { whitespace: true, identifiers: false, syntax: true },
  external: ["@notshekhar/pi-core", "@earendil-works/pi-tui", "chalk", "highlight.js"],
  banner: "#!/usr/bin/env node",
  define: { __PI_VERSION__: JSON.stringify(pkg.version) },
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}

chmodSync(join(import.meta.dir, "dist/cli.js"), 0o755);
console.log("✓ built cli");
