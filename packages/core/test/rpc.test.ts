import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, afterEach, beforeAll, describe, expect, mock, test } from "bun:test";
import { MockLanguageModelV3 } from "ai/test";

// getLoopDir() captures homedir() at module load, so point HOME at a temp dir
// BEFORE the first dynamic import of the server — the session store + auth land
// there instead of the real ~/.loop. Static imports are hoisted, so this runs
// in beforeAll with a dynamic import, not a top-level `import`.
const HOME = mkdtempSync(join(tmpdir(), "loop-rpc-home-"));
const CWD = mkdtempSync(join(tmpdir(), "loop-rpc-cwd-"));
const prevHome = process.env.HOME;
const MODEL = "anthropic/claude-sonnet-4-6";

/** A model turn the tests can swap per-case; default is a tiny text answer. */
type StreamPart = Record<string, unknown>;
function streamOf(parts: StreamPart[]) {
    let i = 0;
    return {
        stream: new ReadableStream({
            async pull(controller) {
                await new Promise((r) => setTimeout(r, 1));
                if (i < parts.length) controller.enqueue(parts[i++]);
                else controller.close();
            },
        }),
    };
}
function textTurn(text: string) {
    return streamOf([
        { type: "text-start", id: "t0" },
        ...text.split("").map((c) => ({ type: "text-delta", id: "t0", delta: c })),
        { type: "text-end", id: "t0" },
        { type: "finish", finishReason: "stop", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
    ]);
}
// eslint-disable-next-line @typescript-eslint/no-explicit-any
let doStreamImpl: (options: any) => Promise<any> = async () => textTurn("hello from mock");

// eslint-disable-next-line @typescript-eslint/no-explicit-any
let RpcServer: any, startSocketServer: any, RpcClient: any;

beforeAll(async () => {
    process.env.HOME = HOME;
    // The session store must land under the temp HOME too — the db path is
    // resolved lazily, so an explicit override beats load-order roulette.
    const { setDbPathForTests } = await import("../src/sessions");
    setDbPathForTests(join(HOME, ".loop", "loop.db"));
    // Mock the model resolver BEFORE importing the server, so runTurn's binding
    // resolves to the mock. Every turn streams whatever doStreamImpl returns.
    const realProviders = await import("../src/providers");
    const model = new MockLanguageModelV3({ doStream: (options) => doStreamImpl(options) });
    mock.module("../src/providers", () => ({ ...realProviders, getModel: async () => model }));
    ({ RpcServer, startSocketServer } = await import("../src/rpc/server"));
    ({ RpcClient } = await import("../src/rpc/client"));
});

afterEach(() => {
    doStreamImpl = async () => textTurn("hello from mock");
});

afterAll(async () => {
    mock.restore();
    // The RpcServer constructor init()s the process-global extension host; reset
    // it so a later test file gets a pristine (uninitialized) host.
    const { getExtensionHost } = await import("../src/extensions");
    await getExtensionHost().close();
    const { setDbPathForTests } = await import("../src/sessions");
    setDbPathForTests(null);
    process.env.HOME = prevHome;
    rmSync(HOME, { recursive: true, force: true });
    rmSync(CWD, { recursive: true, force: true });
});

async function until(cond: () => boolean, tries = 400, ms = 5) {
    for (let i = 0; i < tries; i++) {
        if (cond()) return;
        await new Promise((r) => setTimeout(r, ms));
    }
    throw new Error("condition not met in time");
}

/** Drive an in-process server through its real newline-delimited transport. */
function harness() {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const sent: any[] = [];
    const server = new RpcServer();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const { feed } = server.attach({ send: (m: any) => sent.push(m) });
    let id = 0;

    async function call(method: string, params?: unknown) {
        const myId = ++id;
        feed(JSON.stringify({ jsonrpc: "2.0", id: myId, method, params }) + "\n");
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        let res: any;
        await until(() => !!(res = sent.find((m) => m.id === myId)));
        return res;
    }
    /** All session.event notifications received so far, oldest first. */
    const events = () => sent.filter((m) => m.method === "session.event").map((m) => m.params);
    return { sent, feed, call, events };
}

describe("rpc: capabilities + framing", () => {
    test("server.info advertises the full method + event surface", async () => {
        const { call } = harness();
        const { result, error } = await call("server.info");
        expect(error).toBeUndefined();
        expect(result.methods).toContain("session.history");
        expect(result.methods).toContain("session.compact");
        // The whole turn stream is forwarded, not the old 7-event subset.
        for (const e of ["reasoning-delta", "subagent-delta", "step-usage", "tool-error"]) {
            expect(result.events).toContain(e);
        }
    });

    test("two requests in one chunk each get a response", async () => {
        const { sent, feed } = harness();
        feed(
            JSON.stringify({ jsonrpc: "2.0", id: 1, method: "server.info" }) +
                "\n" +
                JSON.stringify({ jsonrpc: "2.0", id: 2, method: "server.info" }) +
                "\n",
        );
        await until(() => sent.filter((m) => m.id === 1 || m.id === 2).length === 2);
        expect(sent.find((m) => m.id === 1)).toBeTruthy();
        expect(sent.find((m) => m.id === 2)).toBeTruthy();
    });

    test("a request split across two feeds is buffered until the newline", async () => {
        const { sent, feed } = harness();
        const line = JSON.stringify({ jsonrpc: "2.0", id: 7, method: "server.info" });
        feed(line.slice(0, 10));
        await new Promise((r) => setTimeout(r, 15));
        expect(sent.find((m) => m.id === 7)).toBeFalsy(); // no newline yet
        feed(line.slice(10) + "\n");
        await until(() => !!sent.find((m) => m.id === 7));
        expect(sent.find((m) => m.id === 7)).toBeTruthy();
    });

    test("malformed JSON yields a parse-error response with null id", async () => {
        const { sent, feed } = harness();
        feed("{ not json }\n");
        await until(() => sent.some((m) => m.error?.code === -32700));
        const res = sent.find((m) => m.error?.code === -32700);
        expect(res.id).toBeNull();
    });

    test("unknown method is a JSON-RPC error, not a throw", async () => {
        const { call } = harness();
        const { error } = await call("does.not.exist");
        expect(error).toBeDefined();
        expect(String(error.message)).toContain("does.not.exist");
    });
});

describe("rpc: session lifecycle", () => {
    test("create → list → open round-trips through disk", async () => {
        const a = harness();
        const created = await a.call("session.create", { cwd: CWD, provider: "anthropic", model: MODEL });
        const sessionId = created.result.sessionId as string;
        expect(sessionId).toBeTruthy();

        const list = await a.call("session.list", { cwd: CWD });
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        expect((list.result as any[]).some((s) => s.id === sessionId)).toBe(true);

        // A fresh server (no in-memory state) must reopen it from the JSONL file.
        const b = harness();
        const opened = await b.call("session.open", { sessionId });
        expect(opened.error).toBeUndefined();
        expect(opened.result.sessionId).toBe(sessionId);
        expect(opened.result.info.cwd).toBe(CWD);
    });

    test("session.history replays the current branch", async () => {
        const { call } = harness();
        const created = await call("session.create", { cwd: CWD, provider: "anthropic", model: MODEL });
        const sessionId = created.result.sessionId as string;
        const hist = await call("session.history", { sessionId });
        expect(hist.error).toBeUndefined();
        expect(hist.result.info.cwd).toBe(CWD);
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const entries = hist.result.entries as any[];
        expect(entries[0].type).toBe("session-info");
        expect(hist.result.leafId).toBe(entries[entries.length - 1].id);
    });

    test.each([["session.send"], ["session.cancel"], ["session.compact"], ["session.history"], ["cost.session"]])(
        "%s on an unopened session errors instead of crashing",
        async (method) => {
            const { call } = harness();
            const { result, error } = await call(method, { sessionId: "nope-nope", input: "x" });
            expect(result).toBeUndefined();
            expect(error).toBeDefined();
            expect(String(error.message)).toContain("nope-nope");
        },
    );
});

describe("rpc: turn streaming", () => {
    test("session.send streams the whole event sequence as notifications", async () => {
        const { call, events } = harness();
        doStreamImpl = async () => textTurn("streamed answer");
        const created = await call("session.create", { cwd: CWD, provider: "anthropic", model: MODEL });
        const sessionId = created.result.sessionId as string;

        const ack = await call("session.send", { sessionId, input: "hi" });
        expect(ack.result.ok).toBe(true); // returns immediately; events stream after

        await until(() => events().some((p) => p.part.type === "finish"));
        const mine = events().filter((p) => p.sessionId === sessionId);
        const deltas = mine.filter((p) => p.part.type === "text-delta");
        expect(deltas.length).toBeGreaterThan(0);
        expect(deltas.map((p) => p.part.data).join("")).toBe("streamed answer");
        expect(mine.some((p) => p.part.type === "finish")).toBe(true);
        // step-usage now rides the wire (was dropped by the old 7-event list).
        expect(mine.some((p) => p.part.type === "step-usage")).toBe(true);

        // The assistant answer is persisted and replays via history.
        const hist = await call("session.history", { sessionId });
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const assistant = (hist.result.entries as any[]).find((e) => e.type === "message" && e.role === "assistant");
        expect(JSON.stringify(assistant.content)).toContain("streamed answer");
    });

    test("session.cancel aborts the in-flight model stream", async () => {
        const { call } = harness();
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        let signal: AbortSignal | undefined;
        doStreamImpl = async (options) => {
            signal = options.abortSignal;
            // Long stream so cancel lands mid-flight.
            return textTurn("x".repeat(200));
        };
        const created = await call("session.create", { cwd: CWD, provider: "anthropic", model: MODEL });
        const sessionId = created.result.sessionId as string;

        await call("session.send", { sessionId, input: "go" });
        await until(() => !!signal); // model started
        const res = await call("session.cancel", { sessionId });
        expect(res.result.ok).toBe(true);
        expect(signal!.aborted).toBe(true);
    });

    test("a turn that throws is reported on the same error channel", async () => {
        const { call, events } = harness();
        doStreamImpl = async () => {
            throw new Error("boom-from-model");
        };
        const created = await call("session.create", { cwd: CWD, provider: "anthropic", model: MODEL });
        const sessionId = created.result.sessionId as string;
        await call("session.send", { sessionId, input: "hi" });

        await until(() => events().some((p) => p.sessionId === sessionId && p.part.type === "error"));
        const err = events().find((p) => p.sessionId === sessionId && p.part.type === "error");
        // Normalized shape: error rides `part.data`, like the emitter's error event.
        expect(String(err.part.data)).toContain("boom-from-model");
    });
});

describe("rpc: auth / catalog / cost", () => {
    test("auth.login validates its inputs then persists the key", async () => {
        const { call } = harness();
        const bad = await call("auth.login", { provider: "anthropic" }); // no apiKey
        expect(bad.error).toBeDefined();

        const ok = await call("auth.login", { provider: "anthropic", apiKey: "sk-test-123" });
        expect(ok.result.ok).toBe(true);
        const status = await call("auth.status");
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        expect((status.result.providers as any[]).map((p) => (typeof p === "string" ? p : p.id))).toContain(
            "anthropic",
        );
    });

    test("catalog.list returns models and honors the provider filter", async () => {
        const { call } = harness();
        const all = await call("catalog.list");
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const list = all.result as any[];
        expect(Array.isArray(list)).toBe(true);
        expect(list.length).toBeGreaterThan(0);
        const anth = await call("catalog.list", { provider: "anthropic" });
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        expect((anth.result as any[]).every((m) => m.provider === "anthropic")).toBe(true);
    });

    test("cost.lifetime returns a breakdown object", async () => {
        const { call } = harness();
        const res = await call("cost.lifetime");
        expect(res.error).toBeUndefined();
        expect(typeof res.result).toBe("object");
    });
});

describe("rpc: client over a unix socket (end to end)", () => {
    test("RpcClient calls, streams notifications, and rejects on error", async () => {
        const { server, socketPath } = startSocketServer();
        const client = new RpcClient(socketPath);
        try {
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            const info: any = await client.call("server.info");
            expect(info.methods).toContain("session.create");

            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            const events: any[] = [];
            client.on("session.event", (p: unknown) => events.push(p));

            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            const created: any = await client.call("session.create", {
                cwd: CWD,
                provider: "anthropic",
                model: MODEL,
            });
            doStreamImpl = async () => textTurn("over the socket");
            await client.call("session.send", { sessionId: created.sessionId, input: "hi" });
            await until(() => events.some((p) => p.part.type === "finish"));
            expect(events.some((p) => p.part.type === "text-delta")).toBe(true);

            // Errors surface as a rejected promise, not a hang.
            await expect(client.call("does.not.exist")).rejects.toBeDefined();
        } finally {
            client.close();
            server.close();
        }
    });
});
