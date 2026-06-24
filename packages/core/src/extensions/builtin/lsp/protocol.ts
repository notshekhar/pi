/**
 * Minimal Language Server Protocol types + path<->URI conversion. We model only
 * the slice we use (initialize, document sync, publishDiagnostics). LSP speaks
 * JSON-RPC framed with `Content-Length` headers over a child process's stdio.
 */
import { pathToFileURL, fileURLToPath } from "node:url";

export type DiagnosticSeverity = 1 | 2 | 3 | 4; // Error | Warning | Information | Hint

export interface Position {
    line: number; // zero-based
    character: number; // zero-based
}

export interface Range {
    start: Position;
    end: Position;
}

export interface Diagnostic {
    range: Range;
    severity?: DiagnosticSeverity;
    code?: string | number;
    source?: string;
    message: string;
}

export interface PublishDiagnosticsParams {
    uri: string;
    version?: number;
    diagnostics: Diagnostic[];
}

export interface JsonRpcMessage {
    jsonrpc: "2.0";
    id?: number | string;
    method?: string;
    params?: unknown;
    result?: unknown;
    error?: { code: number; message: string; data?: unknown };
}

export function pathToUri(absPath: string): string {
    return pathToFileURL(absPath).toString();
}

export function uriToPath(uri: string): string {
    try {
        return fileURLToPath(uri);
    } catch {
        return uri.replace(/^file:\/\//, "");
    }
}

export const SEVERITY_LABEL: Record<DiagnosticSeverity, string> = {
    1: "error",
    2: "warning",
    3: "info",
    4: "hint",
};
