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
// Remembered so a banner recreated on /new or /clear keeps the notice, and so a
// notice that arrives (network) before the banner exists still lands on it.
let updateNotice: string | undefined;

/**
 * Set the "update available" line shown under the welcome masthead. Routed here
 * (instead of appended to chat history) so it sits with the banner at the top
 * rather than floating below the conversation when the async check resolves.
 */
export function setWelcomeUpdateNotice(text: string): void {
    updateNotice = text;
    activeBanner?.setUpdateNotice(text);
}

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
    if (updateNotice) banner.setUpdateNotice(updateNotice);
    history.addChild(banner);
    banner.start();
    deps.tui.requestRender();
}
