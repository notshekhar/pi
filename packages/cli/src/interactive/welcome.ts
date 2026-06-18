/**
 * Builds + shows the startup welcome banner and keeps a single live instance so
 * its animation timer is stopped before a new one is created (on /new, /clear).
 */
import os from "node:os";
import { DEFAULT_AGENT_NAME } from "@notshekhar/loop-core";
import { WelcomeBanner } from "./components/welcome-banner";
import type { ChatHistory } from "./components/chat-history";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";

let activeBanner: WelcomeBanner | null = null;

function username(): string {
    try {
        return os.userInfo().username || process.env.USER || process.env.USERNAME || "there";
    } catch {
        return process.env.USER || process.env.USERNAME || "there";
    }
}

function shortenPath(p: string): string {
    const home = process.env.HOME ?? process.env.USERPROFILE ?? "";
    return home && p.startsWith(home) ? `~${p.slice(home.length)}` : p;
}

/** Render the masthead into chat history; replaces any prior live banner. */
export function showWelcomeBanner(history: ChatHistory, state: AppState, deps: AppDeps): void {
    activeBanner?.stop();
    const banner = new WelcomeBanner(deps.tui, {
        name: username(),
        model: state.modelId,
        session: state.session?.id ?? "unsaved",
        agent: state.agent !== DEFAULT_AGENT_NAME ? state.agent : null,
        cwd: shortenPath(state.cwd),
        version: deps.version,
    });
    activeBanner = banner;
    history.addChild(banner);
    banner.start();
    deps.tui.requestRender();
}
