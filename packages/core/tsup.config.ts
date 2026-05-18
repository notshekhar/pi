import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm"],
  target: "node20",
  outDir: "dist",
  clean: true,
  dts: false,
  splitting: false,
  sourcemap: true,
  tsconfig: "./tsconfig.json",
  // External agent SDKs are loaded via dynamic `import()` at runtime; they're
  // not used unless the user picks a claude-agent/cursor-agent model. Keep
  // them out of the bundle so the build doesn't choke on their d.ts subpath
  // imports and the binary doesn't pay the size cost.
  external: ["@anthropic-ai/claude-agent-sdk", "@cursor/sdk"],
});
