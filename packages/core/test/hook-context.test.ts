import { describe, expect, test } from "bun:test";
import { matchSessionHookContext, stripSessionHookContext, wrapSessionHookContext } from "../src/sessions/hook-context";

describe("session hook-context wrapper", () => {
    test("wrap → match round-trips context and user text", () => {
        const wrapped = wrapSessionHookContext("branch: main\nstatus: clean", "fix the bug");
        const m = matchSessionHookContext(wrapped);
        expect(m).not.toBeNull();
        expect(m!.context).toBe("branch: main\nstatus: clean");
        expect(m!.rest).toBe("fix the bug");
    });

    test("strip returns just the user's text", () => {
        const wrapped = wrapSessionHookContext("ctx", "hello world");
        expect(stripSessionHookContext(wrapped)).toBe("hello world");
    });

    test("plain (unwrapped) text passes through untouched", () => {
        expect(matchSessionHookContext("just a message")).toBeNull();
        expect(stripSessionHookContext("just a message")).toBe("just a message");
    });

    test("a message that merely mentions the tag is not treated as wrapped", () => {
        const text = "here is <session-start-hook-context> inline, not a wrapper";
        expect(matchSessionHookContext(text)).toBeNull();
        expect(stripSessionHookContext(text)).toBe(text);
    });

    test("user text containing newlines and the closing token survives", () => {
        const body = "line1\nline2\n</session-start-hook-context> literal";
        const wrapped = wrapSessionHookContext("c", body);
        expect(stripSessionHookContext(wrapped)).toBe(body);
    });
});
