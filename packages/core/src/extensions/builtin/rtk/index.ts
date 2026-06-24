/**
 * RTK (Rust Token Killer, github.com/rtk-ai/rtk) as a loop extension. RTK is an
 * external CLI that compresses verbose command output (git/npm/cargo/test
 * runners/…) for 60-90% fewer tokens. Integration is command rewriting:
 * `git status` → `rtk git status`. Upstream does this via a PreToolUse hook;
 * loop's native seam is `api.tools.onCall("bash")`, which rewrites the command
 * before it runs.
 *
 * `rtk rewrite "<cmd>"` is the single source of truth (same as the official
 * hook) — we don't duplicate its command table. Exit protocol:
 *   0 + stdout  rewrite found → use it (skip if unchanged / already rtk)
 *   1           no equivalent → leave unchanged
 *   2           deny rule     → leave unchanged (loop's own guard handles it)
 *   3 + stdout  ask rule      → rewrite; loop's permission flow still prompts
 *
 * Requires the `rtk` binary on PATH. Absent → the extension is a silent no-op,
 * so enabling it without rtk installed never breaks bash.
 */
import { spawnSync } from "node:child_process";
import type { LoopAPI } from "../../api";

/** Is `rtk` runnable on this machine? Cached at activate. */
function rtkAvailable(): boolean {
    try {
        return spawnSync("rtk", ["--version"], { stdio: "ignore", timeout: 3000 }).status === 0;
    } catch {
        return false;
    }
}

/** Ask rtk to rewrite a command. Returns the new command, or null to leave it. */
function rtkRewrite(command: string): string | null {
    // Heredocs confuse line-oriented rewriting; rtk skips them and so do we.
    if (command.includes("<<")) return null;
    try {
        const res = spawnSync("rtk", ["rewrite", command], { encoding: "utf8", timeout: 5000 });
        const code = res.status ?? -1;
        if (code !== 0 && code !== 3) return null; // 1=no match, 2=deny, error → leave alone
        const rewritten = (res.stdout ?? "").trim();
        if (!rewritten || rewritten === command) return null;
        return rewritten;
    } catch {
        return null;
    }
}

export default {
    activate(api: LoopAPI) {
        const available = rtkAvailable();

        api.commands.register({
            name: "rtk",
            description: "RTK token optimizer status (rewrites bash commands to compress output)",
            handler: (ctx) => {
                if (!available) {
                    ctx.emit(
                        "help",
                        "rtk binary not found on PATH. Install it: https://github.com/rtk-ai/rtk — then /reload.",
                    );
                    return;
                }
                const on = api.settings.getOwn<boolean>("enabled", true) !== false;
                ctx.emit("help", `rtk rewriting: ${on ? "on" : "off"}. Toggle: /rtk-toggle`);
            },
        });
        api.commands.register({
            name: "rtk-toggle",
            description: "Turn RTK command rewriting on/off",
            handler: (ctx) => {
                const on = api.settings.getOwn<boolean>("enabled", true) !== false;
                api.settings.setOwn("enabled", !on);
                ctx.emit("help", `rtk rewriting ${!on ? "on" : "off"}.`);
            },
        });

        // Nothing to rewrite if rtk isn't installed — leave bash untouched.
        if (!available) return;

        api.tools.onCall("bash", (input) => {
            if (api.settings.getOwn<boolean>("enabled", true) === false) return;
            const command = (input as { command?: string } | undefined)?.command;
            if (typeof command !== "string" || !command) return;
            const rewritten = rtkRewrite(command);
            if (!rewritten) return;
            return { ...(input as object), command: rewritten };
        });
    },
};
