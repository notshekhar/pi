import { defineConfig } from "tsup";
import { readFileSync } from "node:fs";

const pkg = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8")) as {
  version: string;
};

export default defineConfig({
  entry: ["src/cli.ts"],
  format: ["esm"],
  target: "node20",
  outDir: "dist",
  clean: true,
  dts: false,
  splitting: false,
  sourcemap: true,
  shims: true,
  banner: { js: "#!/usr/bin/env node" },
  tsconfig: "./tsconfig.json",
  define: {
    __PI_VERSION__: JSON.stringify(pkg.version),
  },
});
