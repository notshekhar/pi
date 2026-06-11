import { describe, expect, test } from "bun:test";
import { parseChangelogText, getNewEntries, compareVersions } from "../src/changelog";

const SAMPLE = `# Changelog

## [1.2.0] - 2026-01-02

### Added
- thing two

## [1.1.9] - 2026-01-01

- thing one

## not-a-version header

stray text
`;

describe("parseChangelogText", () => {
    test("parses version sections in order", () => {
        const entries = parseChangelogText(SAMPLE);
        expect(entries.map((e) => `${e.major}.${e.minor}.${e.patch}`)).toEqual(["1.2.0", "1.1.9"]);
        expect(entries[0].content).toContain("thing two");
    });

    test("empty/garbage input yields no entries", () => {
        expect(parseChangelogText("")).toEqual([]);
        expect(parseChangelogText("no headers here")).toEqual([]);
    });
});

describe("getNewEntries", () => {
    const entries = parseChangelogText(SAMPLE);
    test("strictly newer than lastVersion", () => {
        expect(getNewEntries(entries, "1.1.9").map((e) => e.patch)).toEqual([0]);
        expect(getNewEntries(entries, "1.2.0")).toEqual([]);
        expect(getNewEntries(entries, "1.0.0").length).toBe(2);
    });

    test("malformed lastVersion treated as 0.0.0", () => {
        expect(getNewEntries(entries, "garbage").length).toBe(2);
    });
});

describe("compareVersions", () => {
    const v = (major: number, minor: number, patch: number) => ({ major, minor, patch, content: "" });
    test("orders correctly across fields", () => {
        expect(compareVersions(v(1, 0, 0), v(0, 9, 9))).toBeGreaterThan(0);
        expect(compareVersions(v(0, 3, 19), v(0, 3, 20))).toBeLessThan(0);
        expect(compareVersions(v(2, 1, 3), v(2, 1, 3))).toBe(0);
    });
});
