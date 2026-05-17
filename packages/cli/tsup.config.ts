import { defineConfig } from "tsup";

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
});
