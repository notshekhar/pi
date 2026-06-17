import { describe, expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, existsSync, rmSync } from "node:fs";
import { tmpdir, homedir } from "node:os";
import { join } from "node:path";
import {
    sandbox,
    defaultSandboxConfig,
    isSandboxSupported,
    checkSandboxDependencies,
    type SandboxConfig,
} from "../src/index";

const shell = existsSync("/bin/bash") ? "/bin/bash" : "/bin/sh";

// The platform must support the sandbox AND its tooling must be installed
// (e.g. bwrap on Linux). CI Linux runners have no bwrap, so wrap() would throw
// "bwrap not found" — skip those cases there rather than assert a value.
const enforceable = isSandboxSupported() && checkSandboxDependencies().errors.length === 0;

function denyNetworkConfig(): SandboxConfig {
    return defaultSandboxConfig(); // network: "deny"
}

describe("sandbox.wrap (pure output)", () => {
    test("returns a [shell, -c, wrapped] argv when the sandbox is enforceable", async () => {
        if (!enforceable) return;
        const wrapped = await sandbox.wrap({ command: "echo hi", shell, cwd: process.cwd(), config: denyNetworkConfig() });
        expect(wrapped).not.toBeNull();
        expect(wrapped!.argv[0]).toBe(shell);
        expect(wrapped!.argv[1]).toBe("-c");
    });

    test("macOS profile denies-by-default and omits allow-network when network denied", async () => {
        if (process.platform !== "darwin") return;
        const wrapped = await sandbox.wrap({ command: "echo hi", shell, cwd: process.cwd(), config: denyNetworkConfig() });
        const line = wrapped!.argv[2];
        expect(line).toContain("sandbox-exec");
        expect(line).toContain("(deny default");
        expect(line).not.toContain("(allow network*)");
    });

    test("macOS profile allows network when network=allow", async () => {
        if (process.platform !== "darwin") return;
        const cfg: SandboxConfig = { ...defaultSandboxConfig(), network: "allow" };
        const wrapped = await sandbox.wrap({ command: "echo hi", shell, cwd: process.cwd(), config: cfg });
        expect(wrapped!.argv[2]).toContain("(allow network*)");
    });
});

// Functional: actually run sandbox-exec on macOS and confirm the FS boundary.
describe("macOS sandbox enforcement (functional)", () => {
    const run = async (command: string, cwd: string): Promise<number> => {
        const wrapped = (await sandbox.wrap({ command, shell, cwd, config: denyNetworkConfig() }))!;
        try {
            execFileSync(wrapped.argv[0], wrapped.argv.slice(1), { cwd, stdio: "ignore" });
            return 0;
        } catch (err) {
            return (err as { status?: number }).status ?? 1;
        }
    };

    test("write inside cwd succeeds, write to home is blocked", async () => {
        if (process.platform !== "darwin") return;
        const dir = mkdtempSync(join(tmpdir(), "pi-sbx-"));
        const denied = join(homedir(), `pi-sbx-denied-${Date.now()}.txt`);
        try {
            expect(await run(`touch ${dir}/ok.txt`, dir)).toBe(0);
            expect(existsSync(join(dir, "ok.txt"))).toBe(true);

            // Home is not in the writable allow-list → the write must fail and
            // the file must not appear.
            expect(await run(`touch ${denied}`, dir)).not.toBe(0);
            expect(existsSync(denied)).toBe(false);
        } finally {
            rmSync(dir, { recursive: true, force: true });
            rmSync(denied, { force: true });
        }
    });

    // Read-only mode (plan agent): even cwd must be unwritable.
    test("readOnly filesystem blocks writes to cwd itself", async () => {
        if (process.platform !== "darwin") return;
        const dir = mkdtempSync(join(tmpdir(), "pi-sbx-ro-"));
        const cfg: SandboxConfig = {
            filesystem: { allowWrite: [], denyWrite: [], denyRead: [], allowRead: [], readOnly: true },
            network: "deny",
        };
        const run = async (command: string): Promise<number> => {
            const wrapped = (await sandbox.wrap({ command, shell, cwd: dir, config: cfg }))!;
            try {
                execFileSync(wrapped.argv[0], wrapped.argv.slice(1), { cwd: dir, stdio: "ignore" });
                return 0;
            } catch (err) {
                return (err as { status?: number }).status ?? 1;
            }
        };
        try {
            // Reads work; writes to cwd are denied by the kernel.
            expect(await run("ls")).toBe(0);
            expect(await run("touch blocked.txt")).not.toBe(0);
            expect(existsSync(join(dir, "blocked.txt"))).toBe(false);
            // A redirect (the classic bash write) is also blocked.
            expect(await run("echo hi > out.txt")).not.toBe(0);
            expect(existsSync(join(dir, "out.txt"))).toBe(false);
        } finally {
            rmSync(dir, { recursive: true, force: true });
        }
    });
});
