import { existsSync } from "node:fs";
import { delimiter, join } from "node:path";
import { homedir } from "node:os";
import { spawn, spawnSync } from "node:child_process";

export function getBinDir(): string {
    return join(homedir(), ".pi", "bin");
}

export interface ShellConfig {
    shell: string;
    args: string[];
}

function findBashOnPath(): string | null {
    if (process.platform === "win32") {
        try {
            const r = spawnSync("where", ["bash.exe"], { encoding: "utf-8", timeout: 5000 });
            if (r.status === 0 && r.stdout) {
                const first = r.stdout.trim().split(/\r?\n/)[0];
                if (first && existsSync(first)) return first;
            }
        } catch {}
        return null;
    }
    try {
        const r = spawnSync("which", ["bash"], { encoding: "utf-8", timeout: 5000 });
        if (r.status === 0 && r.stdout) {
            const first = r.stdout.trim().split(/\r?\n/)[0];
            if (first) return first;
        }
    } catch {}
    return null;
}

export function getShellConfig(customShellPath?: string): ShellConfig {
    if (customShellPath) {
        if (existsSync(customShellPath)) return { shell: customShellPath, args: ["-c"] };
        throw new Error(`Custom shell path not found: ${customShellPath}`);
    }
    if (process.platform === "win32") {
        const paths: string[] = [];
        if (process.env.ProgramFiles) paths.push(`${process.env.ProgramFiles}\\Git\\bin\\bash.exe`);
        if (process.env["ProgramFiles(x86)"]) paths.push(`${process.env["ProgramFiles(x86)"]}\\Git\\bin\\bash.exe`);
        for (const p of paths) if (existsSync(p)) return { shell: p, args: ["-c"] };
        const onPath = findBashOnPath();
        if (onPath) return { shell: onPath, args: ["-c"] };
        throw new Error(
            "No bash shell found. Install Git for Windows, add bash to PATH, or set shellPath in settings.json.",
        );
    }
    if (existsSync("/bin/bash")) return { shell: "/bin/bash", args: ["-c"] };
    const onPath = findBashOnPath();
    if (onPath) return { shell: onPath, args: ["-c"] };
    return { shell: "sh", args: ["-c"] };
}

export function getShellEnv(): NodeJS.ProcessEnv {
    const binDir = getBinDir();
    const pathKey = Object.keys(process.env).find((k) => k.toLowerCase() === "path") ?? "PATH";
    const cur = process.env[pathKey] ?? "";
    const entries = cur.split(delimiter).filter(Boolean);
    const updated = entries.includes(binDir) ? cur : [binDir, cur].filter(Boolean).join(delimiter);
    return { ...process.env, [pathKey]: updated };
}

export function sanitizeBinaryOutput(str: string): string {
    return Array.from(str)
        .filter((char) => {
            const code = char.codePointAt(0);
            if (code === undefined) return false;
            if (code === 0x09 || code === 0x0a || code === 0x0d) return true;
            if (code <= 0x1f) return false;
            if (code >= 0xfff9 && code <= 0xfffb) return false;
            return true;
        })
        .join("");
}

const trackedDetachedChildPids = new Set<number>();
export function trackDetachedChildPid(pid: number): void {
    trackedDetachedChildPids.add(pid);
}
export function untrackDetachedChildPid(pid: number): void {
    trackedDetachedChildPids.delete(pid);
}
export function killTrackedDetachedChildren(): void {
    for (const pid of trackedDetachedChildPids) killProcessTree(pid);
    trackedDetachedChildPids.clear();
}

export function killProcessTree(pid: number): void {
    if (process.platform === "win32") {
        try {
            spawn("taskkill", ["/F", "/T", "/PID", String(pid)], { stdio: "ignore", detached: true });
        } catch {}
    } else {
        try {
            process.kill(-pid, "SIGKILL");
        } catch {
            try {
                process.kill(pid, "SIGKILL");
            } catch {}
        }
    }
}
