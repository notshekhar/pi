import { describe, expect, test } from "bun:test";
import { EventEmitter } from "node:events";
import { getExtensionHost } from "../src/extensions";
import { applyCommandOps } from "../src/extensions/host";
import { isCompatible } from "../src/extensions/manifest";
import { parseSource } from "../src/extensions/sources";
import { withToolHooks } from "../src/agent/tool-hooks";
import { asTurnEmitter } from "../src/agent/events";
import { CommandRegistry, registerBuiltins } from "../src/commands";
import { providerModelToInfo, collectProviderModelInfos } from "../src/extensions/providers";
import type { ProviderPlugin } from "../src/extensions/api";
import {
    runBeforeTurn,
    applySystemPrompt,
    applyAssembleTools,
    applyProviderOptions,
    runAfterTurn,
} from "../src/agent/turn-middleware";

// The load-bearing invariant: with no extensions installed, every extension
// seam must be a behavioral no-op. These tests run against a host that has
// never loaded anything (no init / empty registry), which is exactly the
// clean-install state.
describe("extensions — zero-regression when none are loaded", () => {
    test("host getters are empty without any loaded extension", () => {
        const host = getExtensionHost();
        const tools = host.getTools();
        expect(tools.add.size).toBe(0);
        expect(tools.remove.size).toBe(0);
        expect(host.getToolResultMiddleware()).toHaveLength(0);
        expect(host.getModelInfos()).toHaveLength(0);
        expect(host.getAgents()).toHaveLength(0);
        expect(host.getProvider("anything")).toBeUndefined();
    });

    test("applyCommands(empty host) leaves the builtin command set unchanged", async () => {
        const reg = new CommandRegistry();
        await registerBuiltins(reg, { cwd: process.cwd() });
        const before = reg
            .list()
            .map((c) => c.name)
            .sort();
        getExtensionHost().applyCommands(reg);
        const after = reg
            .list()
            .map((c) => c.name)
            .sort();
        expect(after).toEqual(before);
    });

    test("withToolHooks is a pass-through when no result middleware is supplied", async () => {
        const emitter = asTurnEmitter(new EventEmitter());
        const objOut = { content: "hi", n: 1 };
        const tools = {
            strTool: { description: "s", execute: async () => "RESULT-STRING" },
            objTool: { description: "o", execute: async () => objOut },
        };
        const wrapped = withToolHooks(tools as never, {
            cwd: process.cwd(),
            sessionId: "s",
            transcriptPath: "/tmp/x",
            emitter,
            resultMiddleware: getExtensionHost().getToolResultMiddleware(), // empty
        }) as unknown as Record<string, { execute: (i: unknown, o: unknown) => Promise<unknown> }>;

        // String output is returned verbatim; object output keeps its identity
        // (no spread / corruption), matching the pre-extensions behavior.
        expect(await wrapped.strTool.execute({}, {})).toBe("RESULT-STRING");
        expect(await wrapped.objTool.execute({}, {})).toBe(objOut);
    });

    test("withToolHooks passes tool input through unchanged with no call middleware", async () => {
        const emitter = asTurnEmitter(new EventEmitter());
        let seen: unknown;
        const tools = { echo: { description: "e", execute: async (i: unknown) => ((seen = i), "ok") } };
        const wrapped = withToolHooks(tools as never, {
            cwd: process.cwd(),
            sessionId: "s",
            transcriptPath: "/tmp/x",
            emitter,
            callMiddleware: getExtensionHost().getToolCallMiddleware(), // empty
        }) as unknown as Record<string, { execute: (i: unknown, o: unknown) => Promise<unknown> }>;
        const input = { command: "ls" };
        await wrapped.echo.execute(input, {});
        expect(seen).toBe(input); // exact input forwarded to execute
    });
});

describe("extensions — provider/model mapping (pure)", () => {
    test("providerModelToInfo fills catalog defaults and namespaces the id", () => {
        const info = providerModelToInfo("acme", { id: "fast" });
        expect(info.id).toBe("acme/fast");
        expect(info.provider).toBe("acme");
        expect(info.name).toBe("fast");
        expect(info.contextWindow).toBe(128_000);
        expect(info.maxOutput).toBe(8_192);
        expect(info.cost).toEqual({ input: 0, output: 0, cacheRead: 0, cacheWrite: 0 });
        expect(info.reasoning).toBe(false);
        expect(info.modalities).toEqual(["text"]);
        expect(info.available).toBe(true);
    });

    test("providerModelToInfo passes through explicit fields", () => {
        const info = providerModelToInfo("acme", {
            id: "pro",
            name: "Acme Pro",
            contextWindow: 64_000,
            maxOutput: 4_000,
            reasoning: true,
            modalities: ["text", "image"],
            cost: { input: 1, output: 2 },
        });
        expect(info.name).toBe("Acme Pro");
        expect(info.contextWindow).toBe(64_000);
        expect(info.reasoning).toBe(true);
        expect(info.modalities).toEqual(["text", "image"]);
        expect(info.cost).toEqual({ input: 1, output: 2, cacheRead: 0, cacheWrite: 0 });
    });

    test("collectProviderModelInfos merges providers + direct adds, last writer wins", () => {
        const plugins: ProviderPlugin[] = [
            { id: "acme", models: [{ id: "fast" }, { id: "pro" }] },
            { id: "beta", models: [{ id: "x" }] },
        ];
        const extra = [providerModelToInfo("acme", { id: "pro", name: "Overridden Pro" })];
        const infos = collectProviderModelInfos(plugins, extra);
        const ids = infos.map((i) => i.id).sort();
        expect(ids).toEqual(["acme/fast", "acme/pro", "beta/x"]);
        expect(infos.find((i) => i.id === "acme/pro")?.name).toBe("Overridden Pro");
    });

    test("collectProviderModelInfos is empty for no providers/adds (zero-regression)", () => {
        expect(collectProviderModelInfos([], [])).toHaveLength(0);
    });
});

describe("extensions — turn middleware is a no-op when none are loaded", () => {
    const tc = {
        sessionId: "s",
        transcriptPath: "/t",
        cwd: ".",
        agent: "default",
        modelId: "x/y",
        provider: "x",
        model: "y",
        tools: [],
        isSubagent: false,
    };

    test("runBeforeTurn allows the turn", async () => {
        expect(await runBeforeTurn({ input: "hi", cwd: ".", sessionId: "s", agent: "default", modelId: "x/y" })).toBe(
            true,
        );
    });

    test("applySystemPrompt returns the prompt unchanged", async () => {
        expect(await applySystemPrompt("PROMPT", tc)).toBe("PROMPT");
    });

    test("applyAssembleTools returns the same tools object (by identity)", async () => {
        const tools = { read: {}, write: {} };
        expect(await applyAssembleTools(tools, tc)).toBe(tools);
    });

    test("applyProviderOptions returns the options unchanged", () => {
        const opts = { anthropic: { foo: 1 } };
        expect(applyProviderOptions(opts, tc)).toBe(opts);
        expect(applyProviderOptions(undefined, tc)).toBeUndefined();
    });

    test("runAfterTurn resolves without effect", async () => {
        await expect(runAfterTurn(tc)).resolves.toBeUndefined();
    });
});

describe("extensions — agents/grants are no-ops when none are loaded", () => {
    test("getToolGrants is empty for any agent", () => {
        expect(getExtensionHost().getToolGrants("plan")).toHaveLength(0);
        expect(getExtensionHost().getToolGrants("default")).toHaveLength(0);
    });

    test("getAgentTools is unchanged: default = all, plan = its fixed set", async () => {
        const { getAgentTools } = await import("../src/agent/agents");
        expect(getAgentTools("default")).toBeUndefined();
        // plan's set is fixed; extensions add nothing here.
        const plan = getAgentTools("plan");
        expect(plan).toContain("read");
        expect(plan).not.toContain("write");
    });
});

describe("extensions — source parsing", () => {
    test("github short form preserves the #ref", () => {
        expect(parseSource("github:owner/repo#v2.1")).toEqual({
            kind: "github",
            spec: "github:owner/repo#v2.1",
            name: "repo",
        });
    });

    test("owner/repo shorthand preserves the #ref", () => {
        expect(parseSource("owner/repo#feature-branch")).toEqual({
            kind: "github",
            spec: "github:owner/repo#feature-branch",
            name: "repo",
        });
    });

    test("github URL preserves the #ref and strips .git", () => {
        expect(parseSource("https://github.com/owner/repo.git#main")).toEqual({
            kind: "github",
            spec: "github:owner/repo#main",
            name: "repo",
        });
    });

    test("refless specs are unchanged", () => {
        expect(parseSource("owner/repo").spec).toBe("github:owner/repo");
        expect(parseSource("github:owner/repo").spec).toBe("github:owner/repo");
    });

    test("npm names (scoped, versioned) still parse as npm", () => {
        expect(parseSource("@scope/pkg@1.2.3")).toEqual({ kind: "npm", spec: "@scope/pkg@1.2.3", name: "@scope/pkg" });
        expect(parseSource("plain-pkg").kind).toBe("npm");
    });
});

describe("extensions — API compat check (0.x minors are breaking)", () => {
    const withEngines = (range?: string) => ({ name: "x", loop: range ? { engines: { loop: range } } : {} });

    test("unspecified engines is compatible", () => {
        expect(isCompatible({ name: "x" })).toBe(true);
        expect(isCompatible(withEngines(undefined))).toBe(true);
    });

    test("matching 0.x minor is compatible; mismatched minor is not", () => {
        expect(isCompatible(withEngines("^0.3"))).toBe(true);
        expect(isCompatible(withEngines("0.3.0"))).toBe(true);
        expect(isCompatible(withEngines("^0.1"))).toBe(false);
    });

    test("bare ^0 accepts any 0.x; major mismatch is rejected", () => {
        expect(isCompatible(withEngines("^0"))).toBe(true);
        expect(isCompatible(withEngines("^1.0"))).toBe(false);
    });
});

describe("extensions — command override merging", () => {
    test("override without a description keeps the existing command's description", async () => {
        const reg = new CommandRegistry();
        await registerBuiltins(reg, { cwd: process.cwd() });
        const original = reg.get("cost")!;
        const handler = () => {};
        applyCommandOps(reg, [{ kind: "override", name: "cost", cmd: { handler } }]);
        const overridden = reg.get("cost")!;
        expect(overridden.handler).toBe(handler);
        expect(overridden.description).toBe(original.description);
        expect(overridden.name).toBe("cost");
    });

    test("override with a description uses it", () => {
        const reg = new CommandRegistry();
        reg.register({ name: "x", description: "old", handler: () => {} });
        applyCommandOps(reg, [{ kind: "override", name: "x", cmd: { description: "new", handler: () => {} } }]);
        expect(reg.get("x")!.description).toBe("new");
    });
});

describe("extensions — lifecycle is safe with nothing loaded", () => {
    test("close() resolves and leaves nothing loaded (idempotent)", async () => {
        const host = getExtensionHost();
        await expect(host.close()).resolves.toBeUndefined();
        expect(host.isLoaded("anything")).toBe(false);
        // safe to call again
        await expect(host.close()).resolves.toBeUndefined();
    });

    test("getWarnings starts empty", () => {
        expect(getExtensionHost().getWarnings()).toHaveLength(0);
    });
});
