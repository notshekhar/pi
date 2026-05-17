import { spawn } from "node:child_process";
import { readFile, writeFile, mkdir, readdir } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { tool } from "ai";
import { z } from "zod";

export interface ToolContext {
  cwd: string;
  abortSignal?: AbortSignal;
}

function resolvePath(ctx: ToolContext, p: string): string {
  return resolve(ctx.cwd, p);
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
      description: "Run a shell command. Honors AbortSignal — kills process group on abort.",
      inputSchema: z.object({
        command: z.string(),
        timeout: z.number().int().positive().optional(),
      }),
      execute: async ({ command, timeout = 120_000 }) => {
        return new Promise<{ stdout: string; stderr: string; code: number | null }>((resolveFn, reject) => {
          const child = spawn("/bin/sh", ["-c", command], { cwd: ctx.cwd, detached: true });
          let stdout = "";
          let stderr = "";
          let timer: NodeJS.Timeout | null = null;

          const cleanup = () => {
            if (timer) clearTimeout(timer);
            if (ctx.abortSignal) ctx.abortSignal.removeEventListener("abort", onAbort);
          };

          const onAbort = () => {
            try {
              if (child.pid) process.kill(-child.pid, "SIGTERM");
            } catch {}
            cleanup();
            reject(new Error("aborted"));
          };
          ctx.abortSignal?.addEventListener("abort", onAbort);

          timer = setTimeout(() => {
            try {
              if (child.pid) process.kill(-child.pid, "SIGTERM");
            } catch {}
          }, timeout);

          child.stdout.on("data", (d) => (stdout += d.toString()));
          child.stderr.on("data", (d) => (stderr += d.toString()));
          child.on("error", (err) => {
            cleanup();
            reject(err);
          });
          child.on("close", (code) => {
            cleanup();
            resolveFn({ stdout, stderr, code });
          });
        });
      },
    }),

    ls: tool({
      description: "List directory contents.",
      inputSchema: z.object({ path: z.string().default(".") }),
      execute: async ({ path }) => {
        const full = resolvePath(ctx, path);
        const items = await readdir(full, { withFileTypes: true });
        return {
          entries: items.map((d) => ({ name: d.name, type: d.isDirectory() ? "dir" : "file" })),
        };
      },
    }),

    grep: tool({
      description: "Search file contents by pattern (recursive, ripgrep-like via shell).",
      inputSchema: z.object({
        pattern: z.string(),
        path: z.string().default("."),
        glob: z.string().optional(),
      }),
      execute: async ({ pattern, path, glob }) => {
        const args = ["-rni", "--color=never"];
        if (glob) args.push("--include", glob);
        args.push(pattern, path);
        return new Promise<{ matches: string }>((resolveFn) => {
          const child = spawn("grep", args, { cwd: ctx.cwd });
          let out = "";
          child.stdout.on("data", (d) => (out += d.toString()));
          child.on("close", () => resolveFn({ matches: out }));
          ctx.abortSignal?.addEventListener("abort", () => child.kill("SIGTERM"));
        });
      },
    }),

    find: tool({
      description: "Find files by name pattern.",
      inputSchema: z.object({
        pattern: z.string(),
        path: z.string().default("."),
      }),
      execute: async ({ pattern, path }) => {
        return new Promise<{ paths: string[] }>((resolveFn) => {
          const child = spawn("find", [path, "-name", pattern], { cwd: ctx.cwd });
          let out = "";
          child.stdout.on("data", (d) => (out += d.toString()));
          child.on("close", () => resolveFn({ paths: out.split("\n").filter(Boolean) }));
          ctx.abortSignal?.addEventListener("abort", () => child.kill("SIGTERM"));
        });
      },
    }),
  };
}

export type ToolSet = ReturnType<typeof createTools>;
