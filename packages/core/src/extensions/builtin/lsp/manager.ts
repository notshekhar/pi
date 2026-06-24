/**
 * Per-workspace owner of language-server processes. One manager per cwd (cached
 * as a module singleton), each lazily spawning at most one server per language
 * family on first use. Servers persist across edits and are torn down on the
 * extension's deactivate() (and as a safety net on process exit).
 */
import { LspClient } from "./client";
import type { Diagnostic } from "./protocol";
import { type LanguageKey, languageKeyFor, resolveOrProvisionServer } from "./servers";

const DIAGNOSTICS_TIMEOUT_MS = 1200;

export class LspManager {
    private readonly clients = new Map<LanguageKey, Promise<LspClient | null>>();

    constructor(private readonly cwd: string) {}

    async diagnose(absPath: string, content: string): Promise<Diagnostic[]> {
        const key = languageKeyFor(absPath);
        if (!key) return [];
        const client = await this.clientFor(key);
        if (!client) return [];
        try {
            return await client.diagnose(absPath, content, DIAGNOSTICS_TIMEOUT_MS);
        } catch {
            return [];
        }
    }

    private clientFor(key: LanguageKey): Promise<LspClient | null> {
        const cached = this.clients.get(key);
        if (cached) {
            return cached.then((c) => {
                if (c && !c.isAlive) {
                    this.clients.delete(key);
                    return this.clientFor(key);
                }
                return c;
            });
        }
        const job = this.startServer(key);
        this.clients.set(key, job);
        return job;
    }

    private async startServer(key: LanguageKey): Promise<LspClient | null> {
        const spec = await resolveOrProvisionServer(key, this.cwd);
        if (!spec) return null;
        const client = new LspClient(spec, this.cwd);
        try {
            await client.start();
        } catch {
            return null;
        }
        return client;
    }

    async shutdown(): Promise<void> {
        const clients = await Promise.all([...this.clients.values()]);
        await Promise.all(clients.map((c) => c?.shutdown()));
        this.clients.clear();
    }
}

const managers = new Map<string, LspManager>();
let exitHookInstalled = false;

export function getLspManager(cwd: string): LspManager {
    let mgr = managers.get(cwd);
    if (!mgr) {
        mgr = new LspManager(cwd);
        managers.set(cwd, mgr);
    }
    if (!exitHookInstalled) {
        exitHookInstalled = true;
        const cleanup = () => {
            for (const m of managers.values()) void m.shutdown();
        };
        process.on("exit", cleanup);
        process.on("SIGINT", cleanup);
        process.on("SIGTERM", cleanup);
    }
    return mgr;
}

/** Shut down every manager — called from the extension's deactivate(). */
export async function shutdownAllManagers(): Promise<void> {
    const all = [...managers.values()];
    managers.clear();
    await Promise.all(all.map((m) => m.shutdown()));
}
