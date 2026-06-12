/**
 * Startup-time chat banners and the trust → SessionStart hooks sequence.
 * Pulled out of app.ts so the app file stays an orchestrator.
 */
import type { SelectItem, TUI } from "@notshekhar/pi-tui";
import chalk from "chalk";
import {
    getTrustDecision,
    getTrustOptions,
    hasProjectTrustInputs,
    loadHooksConfig,
    loadProjectSkills,
    loadWorkspaceContext,
    runHooks,
    setTrust,
    settingsStore,
    trustForSession,
} from "@notshekhar/pi-core";
import type { ChatHistory } from "./components/chat-history";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";
import { checkForUpdate } from "../commands";
import { getNewEntries, loadChangelogEntries } from "../changelog";

/**
 * What's-new: show changelog entries the user hasn't seen yet (pi-mono
 * parity: fresh installs just record the version; resumed sessions skip).
 */
export function showWhatsNew(history: ChatHistory, version: string | undefined, resumed: boolean): void {
    if (!version || resumed) return;
    const lastSeen = settingsStore.get("lastChangelogVersion") as string | undefined;
    if (!lastSeen) {
        settingsStore.set("lastChangelogVersion", version);
        return;
    }
    if (lastSeen !== version) {
        const fresh = getNewEntries(loadChangelogEntries(), lastSeen);
        if (fresh.length > 0) {
            history.addSystem(chalk.dim(`Updated to v${version} — what's new:`));
            history.addMarkdown(fresh.map((e) => e.content).join("\n\n"));
        }
        settingsStore.set("lastChangelogVersion", version);
    }
}

/**
 * Silent background update check; suggest upgrade if a newer release exists.
 * Fire-and-forget so startup never blocks on the network.
 */
export function startUpdateCheck(history: ChatHistory, tui: TUI, version: string | undefined): void {
    if (!version) return;
    void checkForUpdate(version).then((latest) => {
        if (latest) {
            history.addSystem(`Update available: v${version} → ${latest}. Run /update (or \`pi update\`) to upgrade.`);
            tui.requestRender();
        }
    });
}

/** Workspace context + project skills summary lines. */
export async function showWorkspaceBanners(history: ChatHistory, cwd: string): Promise<void> {
    if ((settingsStore.get("workspaceContext") as boolean) !== false) {
        const ws = loadWorkspaceContext(cwd);
        if (ws.files.length > 0) {
            history.addSystem(chalk.dim(`workspace context (${ws.files.length}):`));
            for (const f of ws.files) {
                history.addSystem(chalk.dim(`  • ${f.replace(process.env.HOME ?? "", "~")}`));
            }
        } else {
            history.addSystem(chalk.dim("workspace context: none (AGENTS.md, CLAUDE.md not found)"));
        }
    }
    if ((settingsStore.get("skills") as boolean) !== false) {
        const sk = await loadProjectSkills(cwd);
        if (sk.skills.length > 0) {
            history.addSystem(chalk.dim(`skills (${sk.skills.length}):`));
            for (const s of sk.skills) {
                history.addSystem(chalk.dim(`  • ${s.name} — ${s.description.slice(0, 80)}`));
            }
        }
    }
}

/**
 * Project trust → SessionStart hooks. First open of a folder that ships
 * .pi/.claude resources prompts before any project hook/skill can run; the
 * decision gates project resource loading (executable hooks, project skills).
 */
export async function runStartupTrustAndHooks(state: AppState, deps: AppDeps): Promise<void> {
    const { tui, history, selectOnce } = deps;

    if (hasProjectTrustInputs(state.cwd) && getTrustDecision(state.cwd) === null) {
        const opts = getTrustOptions(state.cwd);
        history.addSystem(
            chalk.yellow(`Trust this project folder?\n${state.cwd}`) +
                chalk.dim("\nTrusting lets pi load this repo's .pi/.claude settings, hooks, and skills."),
        );
        tui.requestRender();
        const items: SelectItem[] = opts.map((o) => ({ value: o.label, label: o.label, description: "" }));
        const pick = await selectOnce(items, "Project trust");
        const chosen = opts.find((o) => o.label === pick?.value);
        if (chosen) {
            if (chosen.remember) setTrust(chosen.savePath, chosen.trusted);
            else if (chosen.trusted) trustForSession(state.cwd); // session-only: in-memory, not persisted
            history.addSystem(
                chalk.dim(
                    chosen.trusted ? "✓ project trusted" : "✗ project not trusted — project hooks/skills disabled",
                ),
            );
        } else {
            history.addSystem(chalk.dim("trust prompt dismissed — treating project as untrusted for now"));
        }
        tui.requestRender();
    }

    // Active hooks summary (after trust is resolved so project hooks count).
    // Display command shortened to its script basename — full plugin paths
    // are too long for the startup banner.
    const shortCmd = (cmd: unknown): string => {
        // Malformed config entries can lack `command` — banner must not throw.
        if (typeof cmd !== "string" || !cmd) return "(invalid hook entry)";
        const script = cmd.match(/[^\s"']+\.(?:sh|js|ts|py|cmd|mjs|cjs)\b/)?.[0];
        if (script) return script.split("/").pop()!;
        return cmd.length > 48 ? `${cmd.slice(0, 45)}…` : cmd;
    };
    const hooksCfg = loadHooksConfig(state.cwd);
    const hookEvents = Object.entries(hooksCfg).filter(([, groups]) => groups?.length);
    if (hookEvents.length > 0) {
        const total = hookEvents.reduce(
            (n, [, groups]) => n + groups!.reduce((m, g) => m + (g.hooks?.length ?? 0), 0),
            0,
        );
        history.addHook(`hooks (${total}):`);
        for (const [ev, groups] of hookEvents) {
            const cmds = groups!.flatMap((g) => g.hooks ?? []).map((h) => shortCmd(h.command));
            history.addSystem(chalk.dim(`    • ${ev}: ${cmds.join(", ")}`));
        }
        tui.requestRender();
    }

    // SessionStart hooks (now that trust is resolved): messages render in chat;
    // additionalContext rides the first user prompt.
    const h = await runHooks(
        "SessionStart",
        "startup",
        { session_id: state.session?.id, transcript_path: state.session?.path, source: "startup" },
        state.cwd,
    );
    for (const m of h.messages) history.addHook(m);
    for (const s of h.terminalSequences) process.stdout.write(s);
    if (h.additionalContext) {
        state.pendingInjection = state.pendingInjection
            ? `${state.pendingInjection}\n\n${h.additionalContext}`
            : h.additionalContext;
    }
    if (h.messages.length || h.additionalContext) tui.requestRender();
}
