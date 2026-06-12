/**
 * URL fetching for the read tool: read also accepts http(s) URLs and returns
 * the page as readable text (HTML stripped to text). Kept small and
 * dependency-free — no headless browser, just fetch + a light HTML→text pass.
 */
import { DEFAULT_MAX_BYTES, formatSize, truncateHead } from "./truncate";

const FETCH_TIMEOUT_MS = 20_000;
const MAX_FETCH_BYTES = 5 * 1024 * 1024; // cap the download itself

export function isHttpUrl(value: string): boolean {
    return /^https?:\/\//i.test(value.trim());
}

/** Collapse HTML to plain text: drop script/style, unwrap tags, decode common entities. */
function htmlToText(html: string): string {
    const noScript = html
        .replace(/<script[\s\S]*?<\/script>/gi, " ")
        .replace(/<style[\s\S]*?<\/style>/gi, " ")
        .replace(/<!--[\s\S]*?-->/g, " ");
    const text = noScript
        .replace(/<\/(p|div|h[1-6]|li|tr|br|section|article|header|footer)>/gi, "\n")
        .replace(/<li[^>]*>/gi, "- ")
        .replace(/<[^>]+>/g, "");
    const decoded = text
        .replace(/&nbsp;/g, " ")
        .replace(/&amp;/g, "&")
        .replace(/&lt;/g, "<")
        .replace(/&gt;/g, ">")
        .replace(/&quot;/g, '"')
        .replace(/&#39;/g, "'")
        .replace(/&#x27;/g, "'");
    return decoded
        .split("\n")
        .map((l) => l.replace(/[ \t]+/g, " ").trim())
        .filter(Boolean)
        .join("\n");
}

/** Fetch a URL and return readable text, truncated like file reads. */
export async function fetchUrlAsText(url: string, abortSignal?: AbortSignal): Promise<string> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), FETCH_TIMEOUT_MS);
    const onParentAbort = () => controller.abort();
    abortSignal?.addEventListener("abort", onParentAbort, { once: true });
    try {
        const res = await fetch(url, {
            signal: controller.signal,
            redirect: "follow",
            headers: { "user-agent": "pi-agent/1.0 (+https://github.com/notshekhar/pi)" },
        });
        if (!res.ok) return `[fetch failed: ${res.status} ${res.statusText} for ${url}]`;

        const contentType = res.headers.get("content-type") ?? "";
        // Binary/non-text content: report metadata instead of dumping bytes.
        if (!/text|json|xml|html|javascript|csv|yaml|markdown/i.test(contentType) && contentType) {
            const len = res.headers.get("content-length");
            return `[non-text resource: ${contentType}${len ? `, ${formatSize(Number(len))}` : ""} at ${url}. Not fetched as text.]`;
        }

        const buf = Buffer.from(await res.arrayBuffer());
        if (buf.byteLength > MAX_FETCH_BYTES) {
            return `[resource too large: ${formatSize(buf.byteLength)} at ${url}. Limit ${formatSize(MAX_FETCH_BYTES)}.]`;
        }
        const raw = buf.toString("utf-8");
        const body = /html/i.test(contentType) || /^\s*<!doctype html|^\s*<html/i.test(raw) ? htmlToText(raw) : raw;

        const truncation = truncateHead(body);
        const header = `[fetched ${url} — ${contentType || "unknown type"}]\n\n`;
        if (truncation.truncated) {
            return `${header}${truncation.content}\n\n[truncated at ${formatSize(DEFAULT_MAX_BYTES)}]`;
        }
        return header + truncation.content;
    } catch (err) {
        if (controller.signal.aborted) return `[fetch timed out or aborted: ${url}]`;
        return `[fetch error for ${url}: ${err instanceof Error ? err.message : String(err)}]`;
    } finally {
        clearTimeout(timer);
        abortSignal?.removeEventListener("abort", onParentAbort);
    }
}
