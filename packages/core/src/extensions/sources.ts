/**
 * Parse a `loop install <spec>` argument into a dependency spec Bun's resolver
 * understands plus a best-effort name for the install directory. Three source
 * kinds: npm package, GitHub repo, and a local path (used by `loop link` and
 * accepted by install too). The authoritative name comes from the installed
 * package.json afterwards — this is only the provisional dir name.
 */
import { isAbsolute, resolve } from "node:path";

export type SourceKind = "npm" | "github" | "local" | "builtin";

export interface ParsedSource {
    kind: SourceKind;
    /** What to hand `bun add` (or, for local, the absolute path). */
    spec: string;
    /** Provisional name (refined from the installed package.json). */
    name: string;
}

const GITHUB_URL_RE = /^(?:https?:\/\/)?(?:www\.)?github\.com\/([\w.-]+)\/([\w.-]+?)(?:\.git)?(?:#.*)?$/i;
const GITHUB_SHORT_RE = /^github:([\w.-]+)\/([\w.-]+)(?:#.*)?$/i;
// owner/repo shorthand — only when it isn't a scoped npm name (@scope/pkg).
const OWNER_REPO_RE = /^([\w.-]+)\/([\w.-]+)(?:#.*)?$/;
const NPM_NAME_RE = /^(@[\w.-]+\/)?[\w.-]+(@[\w.\-^~*x>=<| ]+)?$/;

function repoName(repo: string): string {
    return repo.replace(/\.git$/, "");
}

export function parseSource(input: string): ParsedSource {
    const raw = input.trim();
    if (!raw) throw new Error("empty extension spec");

    // Local path: ./x, ../x, /abs, ~/x, or file:
    if (raw.startsWith(".") || raw.startsWith("/") || raw.startsWith("~") || raw.startsWith("file:")) {
        const p = raw.startsWith("file:") ? raw.slice("file:".length) : raw;
        const abs = isAbsolute(p) ? p : resolve(process.cwd(), p.replace(/^~/, process.env.HOME ?? "~"));
        return { kind: "local", spec: abs, name: abs.split("/").filter(Boolean).pop() ?? "extension" };
    }

    let m = raw.match(GITHUB_SHORT_RE);
    if (m) return { kind: "github", spec: `github:${m[1]}/${repoName(m[2])}`, name: repoName(m[2]) };

    m = raw.match(GITHUB_URL_RE);
    if (m) return { kind: "github", spec: `github:${m[1]}/${repoName(m[2])}`, name: repoName(m[2]) };

    // Scoped or plain npm name (possibly with @version) — checked before the
    // bare owner/repo shorthand so "@scope/pkg" isn't mistaken for a repo.
    if (raw.startsWith("@") || !OWNER_REPO_RE.test(raw)) {
        if (!NPM_NAME_RE.test(raw)) throw new Error(`unrecognized extension spec: ${input}`);
        const name = raw.replace(/@[\w.\-^~*x>=<| ]+$/, "").trim() || raw;
        return { kind: "npm", spec: raw, name };
    }

    // owner/repo → GitHub shorthand
    m = raw.match(OWNER_REPO_RE)!;
    return { kind: "github", spec: `github:${m[1]}/${repoName(m[2])}`, name: repoName(m[2]) };
}
