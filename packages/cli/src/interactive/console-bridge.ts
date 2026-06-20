/**
 * Route stray console output and escaped errors into the chat so they never
 * tear the TUI renderer by writing to stdout/stderr directly.
 */
import type { TUI } from "@notshekhar/loop-tui";
import type { ChatHistory } from "./components/chat-history";
import { formatError } from "./format-error";

/** Returns a restore function — needed when the TUI hands the terminal back
 * (e.g. /update runs the installer with inherited stdio). */
export function installConsoleBridge(history: ChatHistory, tui: TUI): () => void {
    // Stray console output from libraries (warnings, deprecation notices)
    // bypasses the renderer and tears frames. Route it into the chat as
    // messages instead — errors red, the rest dim. stdout/stderr writes from
    // native code still bypass this, but console.* covers the practical cases.
    const origConsole = { log: console.log, warn: console.warn, error: console.error };
    const fmt = (args: unknown[]) => args.map((a) => (typeof a === "string" ? a : JSON.stringify(a))).join(" ");
    console.log = (...args: unknown[]) => {
        history.addSystem(fmt(args));
        tui.requestRender();
    };
    console.warn = (...args: unknown[]) => {
        history.addSystem(fmt(args));
        tui.requestRender();
    };
    // Errors get formatError per arg so a stray console.error(apiError) shows
    // the real message, not a JSON dump of the whole request.
    const fmtErr = (args: unknown[]) => args.map((a) => (typeof a === "string" ? a : formatError(a))).join(" ");
    console.error = (...args: unknown[]) => {
        history.addError(fmtErr(args));
        tui.requestRender();
    };
    const restore = () => Object.assign(console, origConsole);
    process.once("exit", restore);

    // Last-resort error surfacing: anything that escapes a handler renders in
    // chat instead of tearing the TUI via stderr or killing the process.
    // Display only — errors are never written to the session transcript.
    const surfaceError = (prefix: string) => (err: unknown) => {
        history.addError(`${prefix}: ${formatError(err)}`);
        tui.requestRender();
    };
    process.on("uncaughtException", surfaceError("uncaught"));
    process.on("unhandledRejection", surfaceError("unhandled"));

    return restore;
}
