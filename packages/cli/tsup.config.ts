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
  // CLI is published with `@notshekhar/pi-core` as a real npm dep. tsup keeps
  // it external; consumers get it via npm install. External-agent SDKs are
  // dynamic-imported optional deps.
  external: ["@notshekhar/pi-core", "@cursor/sdk", "@anthropic-ai/claude-agent-sdk"],
  define: {
    __PI_VERSION__: JSON.stringify(pkg.version),
  },
});
