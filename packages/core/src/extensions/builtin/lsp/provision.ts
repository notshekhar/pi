/**
 * Auto-installs npm-based language servers into ~/.loop/servers/<key>/ on first
 * use, so diagnostics work without the user pre-installing anything. The install
 * runs with loop's own runtime as the package manager: process.execPath is the
 * loop binary, and BUN_BE_BUN=1 makes it behave as the bun CLI — so `install`
 * works with no separate node/bun on the machine.
 */
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const SERVERS_DIR = join(homedir(), ".loop", "servers");
const INSTALL_TIMEOUT_MS = 90_000;

const inFlight = new Map<string, Promise<string | null>>();

export function ensureProvisioned(key: string, npm: Record<string, string>, npmBin: string): Promise<string | null> {
    const dir = join(SERVERS_DIR, key);
    const binPath = join(dir, npmBin);
    if (existsSync(binPath)) return Promise.resolve(binPath);

    const existing = inFlight.get(key);
    if (existing) return existing;

    const job = install(dir, npm, binPath).finally(() => inFlight.delete(key));
    inFlight.set(key, job);
    return job;
}

async function install(dir: string, npm: Record<string, string>, binPath: string): Promise<string | null> {
    try {
        mkdirSync(dir, { recursive: true });
        writeFileSync(
            join(dir, "package.json"),
            JSON.stringify({ name: "loop-lsp-server", private: true, dependencies: npm }, null, 2),
        );
        const ok = await run(process.execPath, ["install"], dir);
        return ok && existsSync(binPath) ? binPath : null;
    } catch {
        return null;
    }
}

function run(command: string, args: string[], cwd: string): Promise<boolean> {
    return new Promise((resolve) => {
        const proc = spawn(command, args, {
            cwd,
            stdio: "ignore",
            env: { ...process.env, BUN_BE_BUN: "1" },
        });
        const timer = setTimeout(() => {
            proc.kill();
            resolve(false);
        }, INSTALL_TIMEOUT_MS);
        proc.on("error", () => {
            clearTimeout(timer);
            resolve(false);
        });
        proc.on("exit", (code) => {
            clearTimeout(timer);
            resolve(code === 0);
        });
    });
}
