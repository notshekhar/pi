/**
 * Network host validation, canonicalization, and direct dialing — the
 * security-critical helpers shared by the HTTP and SOCKS proxies.
 *
 * Ported from anthropic-experimental/sandbox-runtime (Apache-2.0):
 * https://github.com/anthropic-experimental/sandbox-runtime — see NOTICE.md.
 * (Subset of parent-proxy.ts; corporate/upstream-proxy chaining is not ported.)
 */
import type { Socket } from "node:net";
import { connect as netConnect, isIP } from "node:net";

const CONNECT_TIMEOUT_MS = 30_000;

/** Remove surrounding square brackets from an IPv6 literal. */
export function stripBrackets(host: string): string {
    return host.startsWith("[") && host.endsWith("]") ? host.slice(1, -1) : host;
}

/**
 * Hostname validation: accepts DNS names and IP literals (no zone IDs).
 * Blocks control characters (CRLF injection, null-byte DNS truncation) and
 * zone-identifier allowlist bypasses before they reach the wire or the matcher.
 *
 * IPv6 zone IDs (`fe80::1%eth0`) are rejected: `::ffff:1.2.3.4%x.allowed.com`
 * would otherwise pass `isIP`, pass a `.endsWith('.allowed.com')` check, then
 * connect to 1.2.3.4 when the OS discards the bogus scope.
 */
export function isValidHost(h: string): boolean {
    if (!h || h.length > 255) return false;
    const bare = stripBrackets(h);
    if (bare.includes("%")) return false;
    if (isIP(bare)) return true;
    // DNS label charset; underscore allowed for real-world records (_dmarc, …).
    return /^[A-Za-z0-9._-]+$/.test(bare);
}

/**
 * Canonicalize a host via the WHATWG URL parser so allowlist comparisons agree
 * with what getaddrinfo() will dial: inet_aton shorthand (`127.1`,
 * `2130706433`), hex/octal octets, IPv6 compression, trailing dots, case,
 * brackets. Returns undefined for invalid hosts.
 */
export function canonicalizeHost(h: string): string | undefined {
    try {
        const bare = stripBrackets(h);
        const bracketed = isIP(bare) === 6 ? `[${bare}]` : bare;
        const out = new URL(`http://${bracketed}/`).hostname;
        return stripBrackets(out).replace(/\.$/, "");
    } catch {
        return undefined;
    }
}

/** Dial `host:port` directly with a bounded timeout. */
export function dialDirect(host: string, port: number, timeoutMs = CONNECT_TIMEOUT_MS): Promise<Socket> {
    return new Promise((resolve, reject) => {
        const s = netConnect(port, host);
        let settled = false;
        const done = (err?: Error) => {
            if (settled) return;
            settled = true;
            s.setTimeout(0);
            if (err) {
                s.destroy();
                reject(err);
            } else {
                resolve(s);
            }
        };
        s.setTimeout(timeoutMs, () => done(new Error("connect timed out")));
        s.once("connect", () => done());
        s.once("error", done);
        s.once("close", () => done(new Error("socket closed before connect")));
    });
}
