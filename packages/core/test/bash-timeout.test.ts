import { describe, expect, test } from "bun:test";
import { resolveBashTimeout, DEFAULT_BASH_TIMEOUT_SEC, MAX_BASH_TIMEOUT_SEC } from "../src/tools/bash";

describe("resolveBashTimeout", () => {
    test("falls back to the default when no timeout is given (the unbounded-run bug)", () => {
        expect(resolveBashTimeout(undefined)).toBe(DEFAULT_BASH_TIMEOUT_SEC);
    });

    test("falls back to the default for a non-positive value", () => {
        expect(resolveBashTimeout(0)).toBe(DEFAULT_BASH_TIMEOUT_SEC);
        expect(resolveBashTimeout(-5)).toBe(DEFAULT_BASH_TIMEOUT_SEC);
    });

    test("honors a caller-supplied value within range", () => {
        expect(resolveBashTimeout(30)).toBe(30);
        expect(resolveBashTimeout(300)).toBe(300);
    });

    test("clamps anything above the max", () => {
        expect(resolveBashTimeout(99_999)).toBe(MAX_BASH_TIMEOUT_SEC);
        expect(resolveBashTimeout(MAX_BASH_TIMEOUT_SEC + 1)).toBe(MAX_BASH_TIMEOUT_SEC);
    });
});
