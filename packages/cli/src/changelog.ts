/**
 * Changelog parsing — ported from pi-mono utils/changelog.ts.
 * Divergence: pi-mono's GitHub link normalization (repo-specific tag/blob
 * rewriting) is skipped; our CHANGELOG.md uses plain text and absolute URLs.
 */
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export interface ChangelogEntry {
  major: number;
  minor: number;
  patch: number;
  content: string;
}

/** CHANGELOG.md ships at the package root, next to dist/ and src/. */
export function getChangelogPath(): string {
  const here = dirname(fileURLToPath(import.meta.url));
  for (const candidate of [join(here, "..", "CHANGELOG.md"), join(here, "..", "..", "CHANGELOG.md")]) {
    if (existsSync(candidate)) return candidate;
  }
  return join(here, "..", "CHANGELOG.md");
}

/** Scan for `## [x.y.z]` headers and collect content until the next header. */
export function parseChangelog(changelogPath: string): ChangelogEntry[] {
  if (!existsSync(changelogPath)) return [];
  try {
    const lines = readFileSync(changelogPath, "utf-8").split("\n");
    const entries: ChangelogEntry[] = [];
    let currentLines: string[] = [];
    let currentVersion: { major: number; minor: number; patch: number } | null = null;

    const flush = () => {
      if (currentVersion && currentLines.length > 0) {
        entries.push({ ...currentVersion, content: currentLines.join("\n").trim() });
      }
    };

    for (const line of lines) {
      if (line.startsWith("## ")) {
        flush();
        const m = line.match(/##\s+\[?(\d+)\.(\d+)\.(\d+)\]?/);
        if (m) {
          currentVersion = {
            major: Number.parseInt(m[1], 10),
            minor: Number.parseInt(m[2], 10),
            patch: Number.parseInt(m[3], 10),
          };
          currentLines = [line];
        } else {
          currentVersion = null;
          currentLines = [];
        }
      } else if (currentVersion) {
        currentLines.push(line);
      }
    }
    flush();
    return entries;
  } catch {
    return [];
  }
}

export function compareVersions(v1: ChangelogEntry, v2: ChangelogEntry): number {
  if (v1.major !== v2.major) return v1.major - v2.major;
  if (v1.minor !== v2.minor) return v1.minor - v2.minor;
  return v1.patch - v2.patch;
}

/** Entries strictly newer than lastVersion ("x.y.z"). */
export function getNewEntries(entries: ChangelogEntry[], lastVersion: string): ChangelogEntry[] {
  const parts = lastVersion.split(".").map(Number);
  const last: ChangelogEntry = {
    major: parts[0] || 0,
    minor: parts[1] || 0,
    patch: parts[2] || 0,
    content: "",
  };
  return entries.filter((entry) => compareVersions(entry, last) > 0);
}
