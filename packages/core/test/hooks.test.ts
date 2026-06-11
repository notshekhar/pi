import { describe, expect, test } from "bun:test";
import { matcherTest } from "../src/agent/hooks";

describe("matcherTest", () => {
    test("empty or * matches everything", () => {
        expect(matcherTest(undefined, "bash")).toBe(true);
        expect(matcherTest("", "bash")).toBe(true);
        expect(matcherTest("*", "bash")).toBe(true);
    });

    test("exact name and | lists", () => {
        expect(matcherTest("bash", "bash")).toBe(true);
        expect(matcherTest("bash", "read")).toBe(false);
        expect(matcherTest("read|write|edit", "write")).toBe(true);
        expect(matcherTest("read|write|edit", "bash")).toBe(false);
    });

    test("regex matchers", () => {
        expect(matcherTest("ba.*", "bash")).toBe(true);
        expect(matcherTest("^(read|grep)$", "grep")).toBe(true);
        expect(matcherTest("^(read|grep)$", "bash")).toBe(false);
    });

    test("invalid regex never throws, never matches", () => {
        expect(matcherTest("([", "bash")).toBe(false);
    });

    test("events without a matcher target match any group", () => {
        expect(matcherTest("whatever", undefined)).toBe(true);
    });
});
