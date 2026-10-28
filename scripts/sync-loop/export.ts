/**
 * Regenerates pi's embedded copies of loop's own tables, so the two agents
 * ship the same models, tool definitions and internal docs.
 *
 *   bun scripts/sync-loop/export.ts [path-to-loop]
 *
 * Runs with a throwaway HOME so nothing personal (your agents, datasources)
 * leaks into the generated text; pi fills those parts in at runtime.
 *
 * Writes:
 *   internal/core/catalog/data/{generated,fallbacks}.json
 *   internal/core/tools/defs/loop-tools.json
 *   internal/core/docs/data/docs.json
 */
import { copyFileSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const loop = resolve(process.argv[2] ?? join(import.meta.dir, "..", "..", "..", "loop"));
const core = join(loop, "packages", "core", "src");
const repo = join(import.meta.dir, "..", "..");

// The home directory is resolved once at startup, so a clean one has to be
// set before this process exists: re-run under a throwaway HOME.
if (process.env.PI_SYNC_CLEAN_HOME !== "1") {
    const home = mkdtempSync(join(tmpdir(), "pi-sync-"));
    const child = Bun.spawnSync([process.execPath, import.meta.path, loop], {
        env: { ...process.env, HOME: home, PI_SYNC_CLEAN_HOME: "1" },
        stdout: "inherit",
        stderr: "inherit",
    });
    process.exit(child.exitCode ?? 1);
}

// ── catalog ──────────────────────────────────────────────────────────────
const fallbacks = await import(join(core, "catalog", "fallbacks.ts"));
const sdks = ["openai", "anthropic", "google", "openai-compatible"] as const;
writeFileSync(
    join(repo, "internal", "core", "catalog", "data", "fallbacks.json"),
    JSON.stringify(
        {
            fallback: fallbacks.FALLBACK_MODELS,
            kimiCode: fallbacks.KIMI_CODE_MODELS,
            sdk: Object.fromEntries(sdks.map((sdk) => [sdk, fallbacks.fallbackModelsForSdk(sdk)])),
        },
        null,
        2,
    ) + "\n",
);
copyFileSync(join(core, "catalog", "generated", "models.json"), join(repo, "internal", "core", "catalog", "data", "generated.json"));

// ── tool definitions (description + JSON schema, as the model sees them) ──
const { asSchema } = await import(Bun.resolveSync("ai", core));
const t = await import(join(core, "tools", "index.ts"));
const defs: Record<string, { description: string; schema: unknown }> = {};
const add = async (name: string, tool: { description?: string; inputSchema: unknown }) => {
    defs[name] = { description: tool.description ?? "", schema: await asSchema(tool.inputSchema as never).jsonSchema };
};
for (const [name, tool] of Object.entries(t.createTools({ cwd: "/" }))) await add(name, tool as never);
const noop = { emit() {} };
await add("websearch", t.createWebsearchTool({}));
await add("ask", t.createAskTool({}));
await add("todo", t.createTodoTool({ sessionId: "x", session: { append: async () => {} }, emitter: noop } as never));
await add("skill", t.createSkillTool({ skills: [] }));
await add("plan", t.createPlanTool());
await add("artifact", t.createArtifactTool({}));
await add("enter_plan_mode", t.createEnterPlanModeTool({ sessionId: "x", cwd: "/" }));
await add("exit_plan_mode", t.createExitPlanModeTool({ sessionId: "x", cwd: "/" }));
await add("mcp_tools", t.createMcpToolsTool({ tools: () => ({}) }));
const { createTaskTool } = await import(join(core, "agent", "subagent.ts"));
await add("task", createTaskTool({ session: {}, cwd: "/", emitter: noop, tracker: {}, modelId: "x/x", parentTools: [] } as never));
writeFileSync(join(repo, "internal", "core", "tools", "defs", "loop-tools.json"), JSON.stringify(defs, null, 2) + "\n");

// ── internal docs (read loop://docs) ───────────────────────────────────────
const docs = await import(join(core, "docs", "index.ts"));
writeFileSync(
    join(repo, "internal", "core", "docs", "data", "docs.json"),
    JSON.stringify(docs.listDocs().map((d: { name: string; summary: string; content: string }) => d), null, 2) + "\n",
);
// ── /trace page assets (styles + client scripts, verbatim) ─────────────────
const traceDir = join(repo, "internal", "core", "trace", "data");
mkdirSync(traceDir, { recursive: true });
writeFileSync(join(traceDir, "styles.css"), (await import(join(core, "trace", "styles.ts"))).TRACE_CSS);
writeFileSync(join(traceDir, "markdown.js"), (await import(join(core, "trace", "markdown.ts"))).TRACE_MD_JS);
writeFileSync(join(traceDir, "client.js"), (await import(join(core, "trace", "client.ts"))).TRACE_CLIENT_JS);

console.log(`synced from ${loop}: ${Object.keys(defs).length} tools, ${docs.listDocs().length} docs`);
process.exit(0);
