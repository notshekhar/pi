import { spawn } from "node:child_process";

/**
 * Best-effort browser launch. Returns true if the underlying command was
 * spawned (we can't reliably tell if a tab actually opened). Falls back
 * silently — the caller should still print the URL.
 */
export function openBrowser(url: string): boolean {
    const platform = process.platform;
    let cmd: string;
    let args: string[];
    if (platform === "darwin") {
        cmd = "open";
        args = [url];
    } else if (platform === "win32") {
        cmd = "cmd";
        args = ["/c", "start", "", url];
    } else {
        cmd = "xdg-open";
        args = [url];
    }
    try {
        const child = spawn(cmd, args, { stdio: "ignore", detached: true });
        child.unref();
        return true;
    } catch {
        return false;
    }
}
