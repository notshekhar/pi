/**
 * Bump the loop version across the packages that ship as one unit — core, cli,
 * tui — in a single step, so the three never drift. `sandbox` versions
 * independently and is left alone.
 *
 *   bun run bump patch          # 0.7.5 → 0.7.6
 *   bun run bump minor          # 0.7.5 → 0.8.0
 *   bun run bump major          # 0.7.5 → 1.0.0
 *   bun run bump 0.9.1          # set an explicit version
 *
 * Only the `version` line is rewritten (targeted replace), so formatting and key
 * order in each package.json are untouched. Prints the next tag to push.
 */
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// In lockstep with the loop release version. `sandbox` is intentionally absent.
const PACKAGES = ["core", "cli", "tui"] as const;
const ROOT = join(import.meta.dir, "..");
const pkgPath = (name: string) => join(ROOT, "packages", name, "package.json");
const VERSION_RE = /("version":\s*")[^"]+(")/;

function readVersion(path: string): string {
    const m = readFileSync(path, "utf8").match(/"version":\s*"([^"]+)"/);
    if (!m) throw new Error(`no version field in ${path}`);
    return m[1];
}

function bump(version: string, level: "patch" | "minor" | "major"): string {
    const parts = version.split(".").map(Number);
    if (parts.length !== 3 || parts.some((n) => !Number.isInteger(n) || n < 0)) {
        throw new Error(`current version is not semver: ${version}`);
    }
    const [major, minor, patch] = parts;
    if (level === "major") return `${major + 1}.0.0`;
    if (level === "minor") return `${major}.${minor + 1}.0`;
    return `${major}.${minor}.${patch + 1}`;
}

const arg = process.argv[2];
if (!arg) {
    console.error("usage: bun run bump <patch|minor|major|x.y.z>");
    process.exit(1);
}

// cli is the source of truth for the current version.
const current = readVersion(pkgPath("cli"));
let next: string;
if (/^\d+\.\d+\.\d+$/.test(arg)) {
    next = arg;
} else if (arg === "patch" || arg === "minor" || arg === "major") {
    next = bump(current, arg);
} else {
    console.error(`invalid argument "${arg}" — use patch|minor|major or an explicit x.y.z`);
    process.exit(1);
}

for (const name of PACKAGES) {
    const path = pkgPath(name);
    const text = readFileSync(path, "utf8");
    if (!VERSION_RE.test(text)) throw new Error(`no version field to rewrite in ${path}`);
    writeFileSync(path, text.replace(VERSION_RE, `$1${next}$2`));
}

console.log(`bumped ${current} → ${next}  (${PACKAGES.join(", ")})`);
console.log(`next: add a CHANGELOG entry, commit, then  git tag v${next} && git push origin main v${next}`);
