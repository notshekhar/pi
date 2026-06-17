/**
 * Domain allow/deny matching for the forward proxies. deniedHosts win over
 * allowedHosts; unmatched hosts are denied (no ask-callback in this build).
 *
 * Ported from anthropic-experimental/sandbox-runtime (Apache-2.0):
 * https://github.com/anthropic-experimental/sandbox-runtime — see NOTICE.md.
 * (matchesDomainPattern + filterNetworkRequest, minus the SandboxAskCallback.)
 */
import { isIP } from "node:net";
import { logForDebugging } from "./debug";
import { canonicalizeHost, isValidHost, stripBrackets } from "./net-utils";

export interface DomainRules {
    /** Allowed domains/patterns (e.g. "github.com", "*.npmjs.org"). */
    allow: string[];
    /** Denied domains/patterns; a bare "*" means deny-all. Checked first. */
    deny: string[];
}

/** Whether a hostname matches a single allow/deny pattern. */
export function matchesDomainPattern(hostname: string, pattern: string): boolean {
    const h = hostname.toLowerCase();
    // Bare "*" is deny-all (only valid in the deny list).
    if (pattern === "*") return true;
    if (pattern.startsWith("*.")) {
        // Never wildcard-match IP literals — an IPv6 zone-ID payload like
        // `::ffff:1.2.3.4%x.allowed.com` would otherwise pass .endsWith().
        if (isIP(stripBrackets(h))) return false;
        const baseDomain = pattern.substring(2).toLowerCase();
        return h.endsWith("." + baseDomain);
    }
    return h === pattern.toLowerCase();
}

/**
 * Build a `(port, host) => boolean` filter the proxies call per connection.
 * `getRules` is read live so allow/deny edits take effect without restarting
 * the proxies.
 */
export function makeHostFilter(getRules: () => DomainRules): (port: number, host: string) => boolean {
    return (port, host) => {
        // Reject control chars before matching — string suffix matching is
        // trivially fooled by e.g. `evil.com\x00.allowed.com`.
        if (!isValidHost(host)) {
            logForDebugging(`Denying malformed host: ${JSON.stringify(host)}:${port}`, { level: "error" });
            return false;
        }
        // Canonicalize so comparisons match what getaddrinfo() will dial
        // (`2852039166` => 169.254.169.254, `127.1` => 127.0.0.1, …).
        const canonicalHost = canonicalizeHost(host) ?? host;
        const rules = getRules();

        for (const deniedDomain of rules.deny) {
            if (matchesDomainPattern(canonicalHost, deniedDomain)) {
                logForDebugging(`Denied by rule: ${host}:${port}`);
                return false;
            }
        }
        for (const allowedDomain of rules.allow) {
            if (matchesDomainPattern(canonicalHost, allowedDomain)) {
                logForDebugging(`Allowed by rule: ${host}:${port}`);
                return true;
            }
        }
        // No match → deny (this build has no interactive ask-callback).
        logForDebugging(`No matching rule, denying: ${host}:${port}`);
        return false;
    };
}
