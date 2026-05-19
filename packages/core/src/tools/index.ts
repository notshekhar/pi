import { spawn, spawnSync } from "node:child_process";
import { readFile, writeFile, mkdir, readdir } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, resolve, relative, isAbsolute } from "node:path";
import { tool } from "ai";
import { z } from "zod";

export interface ToolContext {
  cwd: string;
  abortSignal?: AbortSignal;
}

// ─── Shell resolution (pi-mono parity) ────────────────────────────────────
function findBashOnPath(): string | null {
  try {
    const r = spawnSync(process.platform === "win32" ? "where" : "which", ["bash"], {
      encoding: "utf8",
      timeout: 5000,
    });
    if (r.status === 0 && r.stdout) {
      const first = r.stdout.trim().split(/\r?\n/)[0];
      if (first && existsSync(first)) return first;
    }
  } catch {}
  return null;
}
function getShellConfig(customShellPath?: string): { shell: string; args: string[] } {
  if (customShellPath) {
    if (existsSync(customShellPath)) return { shell: customShellPath, args: ["-c"] };
    throw new Error(`shell path not found: ${customShellPath}`);
  }
  if (process.platform === "win32") {
    for (const p of [
      `${process.env.ProgramFiles}\\Git\\bin\\bash.exe`,
      `${process.env["ProgramFiles(x86)"]}\\Git\\bin\\bash.exe`,
    ]) {
      if (p && existsSync(p)) return { shell: p, args: ["-c"] };
    }
    const onPath = findBashOnPath();
    if (onPath) return { shell: onPath, args: ["-c"] };
    throw new Error("No bash shell found. Install Git for Windows or add bash to PATH.");
  }
  if (existsSync("/bin/bash")) return { shell: "/bin/bash", args: ["-c"] };
  const onPath = findBashOnPath();
  if (onPath) return { shell: onPath, args: ["-c"] };
  return { shell: "/bin/sh", args: ["-c"] };
}

// Kill whole process group/tree (pi-mono parity).
function killProcessTree(pid: number): void {
  try {
    if (process.platform === "win32") {
      spawnSync("taskkill", ["/pid", String(pid), "/T", "/F"]);
    } else {
      process.kill(-pid, "SIGTERM");
    }
  } catch {}
}

// Output truncation — last N bytes + last N lines, head dropped.
const BASH_MAX_BYTES = 64 * 1024;
const BASH_MAX_LINES = 2000;
function truncateOutput(buf: string): { text: string; truncated: boolean; totalLines: number } {
  const totalLines = buf.length === 0 ? 0 : buf.split("\n").length;
  let out = buf;
  let truncated = false;
  if (out.length > BASH_MAX_BYTES) {
    out = out.slice(out.length - BASH_MAX_BYTES);
    const nl = out.indexOf("\n");
    if (nl >= 0) out = out.slice(nl + 1);
    truncated = true;
  }
  const lines = out.split("\n");
  if (lines.length > BASH_MAX_LINES) {
    out = lines.slice(lines.length - BASH_MAX_LINES).join("\n");
    truncated = true;
  }
  return { text: out, truncated, totalLines };
}

function resolvePath(ctx: ToolContext, p: string): string {
  const abs = isAbsolute(p) ? p : resolve(ctx.cwd, p);
  // clamp escape attempts: searches must stay under cwd
  const rel = relative(ctx.cwd, abs);
  if (rel.startsWith("..")) throw new Error(`path escapes cwd: ${p}`);
  return abs;
}

function spawnCapture(
  bin: string,
  args: string[],
  opts: { cwd: string; timeout?: number; signal?: AbortSignal; maxBytes?: number },
): Promise<{ stdout: string; stderr: string; code: number | null; truncated: boolean }> {
  return new Promise((res, rej) => {
    const child = spawn(bin, args, { cwd: opts.cwd, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    let truncated = false;
    const maxBytes = opts.maxBytes ?? 256 * 1024;
    let timer: NodeJS.Timeout | null = null;
    const cleanup = () => {
      if (timer) clearTimeout(timer);
      opts.signal?.removeEventListener("abort", onAbort);
    };
    const onAbort = () => {
      if (!child.killed) child.kill("SIGTERM");
      cleanup();
      rej(new Error("aborted"));
    };
    opts.signal?.addEventListener("abort", onAbort);
    if (opts.timeout) {
      timer = setTimeout(() => {
        if (!child.killed) child.kill("SIGTERM");
      }, opts.timeout);
    }
    child.stdout.on("data", (d: Buffer) => {
      if (stdout.length >= maxBytes) {
        truncated = true;
        if (!child.killed) child.kill("SIGTERM");
        return;
      }
      stdout += d.toString();
    });
    child.stderr.on("data", (d: Buffer) => {
      if (stderr.length < 16 * 1024) stderr += d.toString();
    });
    child.on("error", (err) => {
      cleanup();
      rej(err);
    });
    child.on("close", (code) => {
      cleanup();
      res({ stdout, stderr, code, truncated });
    });
  });
}

async function hasBin(name: string): Promise<boolean> {
  try {
    const r = await spawnCapture("which", [name], { cwd: "/", timeout: 2000 });
    return r.code === 0 && r.stdout.trim().length > 0;
  } catch {
    return false;
  }
}

export function createTools(ctx: ToolContext) {
  return {
    read: tool({
      description: "Read a file. Optional line offset and limit.",
      inputSchema: z.object({
        path: z.string(),
        offset: z.number().int().nonnegative().optional(),
        limit: z.number().int().positive().optional(),
      }),
      execute: async ({ path, offset = 0, limit }) => {
        const buf = await readFile(resolvePath(ctx, path), "utf8");
        const lines = buf.split("\n");
        const end = limit ? Math.min(offset + limit, lines.length) : lines.length;
        return { content: lines.slice(offset, end).join("\n"), lines: lines.length };
      },
    }),

    write: tool({
      description: "Write entire file contents. Creates parent directories as needed.",
      inputSchema: z.object({ path: z.string(), content: z.string() }),
      execute: async ({ path, content }) => {
        const full = resolvePath(ctx, path);
        await mkdir(dirname(full), { recursive: true });
        await writeFile(full, content);
        return { ok: true, bytes: Buffer.byteLength(content) };
      },
    }),

    edit: tool({
      description: "Replace exactly one occurrence of oldString with newString in a file.",
      inputSchema: z.object({
        path: z.string(),
        oldString: z.string(),
        newString: z.string(),
      }),
      execute: async ({ path, oldString, newString }) => {
        const full = resolvePath(ctx, path);
        const buf = await readFile(full, "utf8");
        const count = buf.split(oldString).length - 1;
        if (count === 0) throw new Error(`oldString not found in ${path}`);
        if (count > 1) throw new Error(`oldString matched ${count} times in ${path}; need unique match`);
        await writeFile(full, buf.replace(oldString, newString));
        return { ok: true };
      },
    }),

    bash: tool({
      description:
        "Execute a bash command. stdout+stderr merged. Process tree killed on abort/timeout. Output truncated to 64KB / 2000 lines (tail).",
      inputSchema: z.object({
        command: z.string().describe("Bash command to execute"),
        timeout: z.number().positive().optional().describe("Timeout in seconds (no default — runs until done or aborted)"),
      }),
      execute: async ({ command, timeout }) => {
        const { shell, args } = getShellConfig();
        if (!existsSync(ctx.cwd)) {
          throw new Error(`Working directory does not exist: ${ctx.cwd}`);
        }
        return new Promise<{ output: string; exitCode: number | null; truncated?: boolean; totalLines?: number }>(
          (resolveFn, reject) => {
            const child = spawn(shell, [...args, command], {
              cwd: ctx.cwd,
              detached: process.platform !== "win32",
              stdio: ["ignore", "pipe", "pipe"],
            });
            let buf = "";
            let timedOut = false;
            let timer: NodeJS.Timeout | null = null;
            const cleanup = () => {
              if (timer) clearTimeout(timer);
              ctx.abortSignal?.removeEventListener("abort", onAbort);
            };
            const onAbort = () => {
              if (child.pid) killProcessTree(child.pid);
            };
            ctx.abortSignal?.addEventListener("abort", onAbort, { once: true });
            if (timeout && timeout > 0) {
              timer = setTimeout(() => {
                timedOut = true;
                if (child.pid) killProcessTree(child.pid);
              }, timeout * 1000);
            }
            const onData = (d: Buffer) => {
              buf += d.toString();
              if (buf.length > BASH_MAX_BYTES * 4) {
                // hard cap: keep only the recent slice to avoid unbounded RAM
                buf = buf.slice(buf.length - BASH_MAX_BYTES * 2);
              }
            };
            child.stdout?.on("data", onData);
            child.stderr?.on("data", onData);
            child.on("error", (err) => {
              cleanup();
              reject(err);
            });
            child.on("close", (code) => {
              cleanup();
              const { text, truncated, totalLines } = truncateOutput(buf);
              if (ctx.abortSignal?.aborted) {
                reject(new Error(`aborted${text ? `\n\n${text}` : ""}`));
                return;
              }
              if (timedOut) {
                reject(new Error(`timeout after ${timeout}s${text ? `\n\n${text}` : ""}`));
                return;
              }
              const note = truncated ? `\n\n[Output truncated. Total lines: ${totalLines}]` : "";
              if (code !== 0 && code !== null) {
                reject(new Error(`Command exited with code ${code}${text ? `\n\n${text}${note}` : ""}`));
                return;
              }
              resolveFn({
                output: (text || "(no output)") + note,
                exitCode: code,
                ...(truncated ? { truncated, totalLines } : {}),
              });
            });
          },
        );
      },
    }),

    ls: tool({
      description: "List directory contents (single level). Use 'find' for recursive search.",
      inputSchema: z.object({ path: z.string().default(".") }),
      execute: async ({ path }) => {
        const full = resolvePath(ctx, path);
        const items = await readdir(full, { withFileTypes: true });
        return {
          entries: items
            .filter((d) => d.name !== ".git" && d.name !== "node_modules")
            .map((d) => ({ name: d.name, type: d.isDirectory() ? "dir" : "file" })),
        };
      },
    }),

    grep: tool({
      description:
        "Search file contents for a regex pattern using ripgrep. Respects .gitignore. Stays under the project cwd. Defaults: search ., recursive, hidden files included, limit 100 matches.",
      inputSchema: z.object({
        pattern: z.string().describe("Regex pattern (or literal if literal=true)"),
        path: z.string().default(".").describe("Directory under cwd to search"),
        glob: z.string().optional().describe("Glob filter, e.g. '*.ts' or '**/*.spec.ts'"),
        ignoreCase: z.boolean().optional(),
        literal: z.boolean().optional().describe("Treat pattern as literal string"),
        context: z.number().int().min(0).max(20).optional().describe("Context lines before/after each match"),
        limit: z.number().int().positive().max(2000).optional().describe("Max matches (default 100)"),
      }),
      execute: async ({ pattern, path, glob, ignoreCase, literal, context, limit }) => {
        if (!(await hasBin("rg"))) {
          throw new Error("ripgrep (rg) not found. Install it: brew install ripgrep / apt install ripgrep");
        }
        const searchPath = resolvePath(ctx, path);
        const cap = Math.max(1, limit ?? 100);
        const args = [
          "--line-number",
          "--color=never",
          "--hidden",
          "--no-require-git",
          "--max-count",
          String(cap),
        ];
        if (ignoreCase) args.push("--ignore-case");
        if (literal) args.push("--fixed-strings");
        if (glob) args.push("--glob", glob);
        if (context && context > 0) args.push("--context", String(context));
        args.push("--", pattern, searchPath);
        const { stdout, stderr, code, truncated } = await spawnCapture("rg", args, {
          cwd: ctx.cwd,
          timeout: 15_000,
          signal: ctx.abortSignal,
          maxBytes: 128 * 1024,
        });
        if (code !== 0 && code !== 1) {
          throw new Error(stderr.trim() || `rg exited with code ${code}`);
        }
        if (!stdout.trim()) return { matches: "", count: 0 };
        const lines = stdout.split("\n").filter(Boolean);
        return {
          matches: stdout,
          count: lines.length,
          ...(truncated ? { note: "output truncated at 128KB — refine pattern or use limit" } : {}),
        };
      },
    }),

    find: tool({
      description:
        "Find files by glob pattern using fd. Respects .gitignore. Stays under the project cwd. Default limit 1000 results.",
      inputSchema: z.object({
        pattern: z.string().describe("Glob pattern, e.g. '*.ts', '**/*.json', 'src/**/*.spec.ts'"),
        path: z.string().default(".").describe("Directory under cwd to search"),
        limit: z.number().int().positive().max(5000).optional(),
      }),
      execute: async ({ pattern, path, limit }) => {
        if (!(await hasBin("fd"))) {
          throw new Error("fd not found. Install it: brew install fd / apt install fd-find");
        }
        const searchPath = resolvePath(ctx, path);
        const cap = limit ?? 1000;
        const args = [
          "--glob",
          "--color=never",
          "--hidden",
          "--no-require-git",
          "--max-results",
          String(cap),
        ];
        let pat = pattern;
        if (pattern.includes("/")) {
          args.push("--full-path");
          if (!pattern.startsWith("/") && !pattern.startsWith("**/") && pattern !== "**") {
            pat = `**/${pattern}`;
          }
        }
        args.push("--", pat, searchPath);
        const { stdout, stderr, code } = await spawnCapture("fd", args, {
          cwd: ctx.cwd,
          timeout: 15_000,
          signal: ctx.abortSignal,
          maxBytes: 128 * 1024,
        });
        if (code !== 0 && code !== 1) {
          throw new Error(stderr.trim() || `fd exited with code ${code}`);
        }
        const paths = stdout.split("\n").filter(Boolean).map((p) => relative(ctx.cwd, p));
        return { paths, count: paths.length, ...(paths.length >= cap ? { note: `limit ${cap} reached` } : {}) };
      },
    }),
  };
}

export type ToolSet = ReturnType<typeof createTools>;
