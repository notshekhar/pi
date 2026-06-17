/**
 * Filesystem & network restriction config (internal structures built from
 * permission rules). These intentionally support BOTH allowlist and denylist
 * semantics so we can flip between them via config later.
 *
 * Ported from anthropic-experimental/sandbox-runtime (Apache-2.0):
 * https://github.com/anthropic-experimental/sandbox-runtime — see NOTICE.md.
 */
/**
 * Read restriction config using a "deny then allow-back" pattern.
 *
 * - `undefined` = no restrictions (allow all reads)
 * - `{denyOnly: []}` = no restrictions (empty deny list = allow all reads)
 * - `{denyOnly: [...]}` = deny reads from these paths, allow all others
 * - `{denyOnly: [...], allowWithinDeny: [...]}` = deny denyOnly paths but
 *   re-allow reads within allowWithinDeny (most-specific rule wins).
 *
 * Maximally permissive by default — only explicitly denied paths are blocked.
 */
export interface FsReadRestrictionConfig {
    denyOnly: string[];
    allowWithinDeny?: string[];
}
/**
 * Write restriction config using an "allow-only" pattern.
 *
 * - `undefined` = no restrictions (allow all writes)
 * - `{allowOnly: [], denyWithinAllow: []}` = maximally restrictive (deny ALL writes)
 * - `{allowOnly: [...], denyWithinAllow: [...]}` = allow writes only to allowOnly
 *   paths, minus denyWithinAllow.
 *
 * Maximally restrictive by default — empty allowOnly means NO writable paths
 * (unlike read's empty denyOnly).
 */
export interface FsWriteRestrictionConfig {
    allowOnly: string[];
    denyWithinAllow: string[];
}
/**
 * Network restriction config — an "allow-only" pattern like writes.
 *
 * - `allowedHosts` = explicitly allowed hosts
 * - `deniedHosts` = explicitly denied hosts (checked first)
 *
 * Empty `allowedHosts` means no host matches an allow rule. deniedHosts are
 * checked first and deny unconditionally; a host matching neither falls through
 * to the ask callback (Stage 2) or is denied when none is registered.
 */
export interface NetworkRestrictionConfig {
    allowedHosts?: string[];
    deniedHosts?: string[];
}
export type NetworkHostPattern = {
    host: string;
    port: number | undefined;
};
export type SandboxAskCallback = (params: NetworkHostPattern) => Promise<boolean>;
