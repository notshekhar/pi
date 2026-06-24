/**
 * LSP Diagnostics extension — the reference port of loop's built-in LSP feature
 * to the extension API. After a `write` or `edit`, it reads the resulting file,
 * runs it through the right language server, and appends a `<diagnostics>` block
 * to the tool result so the agent sees type/lint errors it just introduced.
 *
 * Demonstrates the extension surfaces a real feature needs: a tool-result
 * middleware (`onResult`), settings (`getOwn`), external-process management with
 * deps provisioned via the embedded Bun runtime, and lifecycle teardown
 * (`deactivate`).
 */
import { readFile } from "node:fs/promises";
import { isAbsolute, join, relative } from "node:path";
import type { LoopAPI } from "../../api";
import { getLspManager, shutdownAllManagers } from "./manager";
import { type Diagnostic, type DiagnosticSeverity, SEVERITY_LABEL } from "./protocol";

/** Errors + warnings only — hints/info are noise for the agent. */
const REPORTED_SEVERITIES = new Set<DiagnosticSeverity>([1, 2]);

function formatDiagnostics(cwd: string, absPath: string, diags: Diagnostic[]): string {
    const rel = relative(cwd, absPath).replace(/\\/g, "/") || absPath;
    const errors = diags.filter((d) => (d.severity ?? 1) === 1).length;
    const warnings = diags.length - errors;
    const counts = [
        errors && `${errors} error${errors > 1 ? "s" : ""}`,
        warnings && `${warnings} warning${warnings > 1 ? "s" : ""}`,
    ]
        .filter(Boolean)
        .join(", ");
    const lines = diags.map((d) => {
        const sev = SEVERITY_LABEL[d.severity ?? 1];
        const line = d.range.start.line + 1;
        const col = d.range.start.character + 1;
        const code = d.code !== undefined ? ` (${d.code})` : "";
        const msg = d.message.replace(/\s+/g, " ").trim();
        return `  ${rel}:${line}:${col}  ${sev}  ${msg}${code}`;
    });
    return [`<diagnostics>`, `${counts} after this change:`, ...lines, `</diagnostics>`].join("\n");
}

export default {
    activate(api: LoopAPI) {
        // After write/edit, diagnose the file on disk (works for both: the tool
        // has already written it). Appends a diagnostics block, or leaves the
        // result untouched when clean / unsupported / disabled. Never throws — a
        // diagnostics failure must not break an edit.
        api.tools.onResult(["write", "edit"], async (result, ctx) => {
            if (api.settings.getOwn<boolean>("enabled", true) === false) return;
            if (ctx.signal?.aborted) return;
            const rawPath = (ctx.input as { path?: string } | undefined)?.path;
            if (!rawPath) return;
            const absPath = isAbsolute(rawPath) ? rawPath : join(ctx.cwd, rawPath);
            try {
                const content = await readFile(absPath, "utf8");
                const all = await getLspManager(ctx.cwd).diagnose(absPath, content);
                const reported = all.filter((d) => REPORTED_SEVERITIES.has(d.severity ?? 1));
                if (reported.length === 0) return;
                return `${result}\n${formatDiagnostics(ctx.cwd, absPath, reported)}`;
            } catch {
                return; // fail-open
            }
        });
    },

    async deactivate() {
        await shutdownAllManagers();
    },
};
