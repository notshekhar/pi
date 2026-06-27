import { describe, expect, test } from "bun:test";
import { formatError } from "../src/interactive/format-error";

describe("formatError — syscall codes", () => {
    test("surfaces a flat EPERM code like Claude's (EPERM)", () => {
        const e = Object.assign(new Error("operation not permitted"), { code: "EPERM" });
        expect(formatError(e)).toBe("operation not permitted (EPERM)");
    });

    test("unwraps a syscall code buried in .cause (Bun/Node fetch failures)", () => {
        const e = Object.assign(new TypeError("fetch failed"), {
            cause: Object.assign(new Error("connect ECONNREFUSED"), { code: "ECONNREFUSED" }),
        });
        expect(formatError(e)).toBe("fetch failed (ECONNREFUSED)");
    });

    test("does not double-append a code already present in the message", () => {
        const e = Object.assign(new Error("EPERM: operation not permitted, open '/x'"), { code: "EPERM" });
        expect(formatError(e)).toBe("EPERM: operation not permitted, open '/x'");
    });
});

describe("formatError — provider errors stay unchanged", () => {
    test("pulls the provider message and HTTP status out of an API error", () => {
        const e = {
            name: "AI_APICallError",
            statusCode: 429,
            responseBody: JSON.stringify({ error: { message: "rate limit exceeded" } }),
        };
        expect(formatError(e)).toBe("rate limit exceeded (HTTP 429)");
    });

    test("unwraps RetryError via lastError", () => {
        const inner = Object.assign(new Error("connection timed out"), { code: "ETIMEDOUT" });
        const e = { name: "RetryError", lastError: inner };
        expect(formatError(e)).toBe("connection timed out (ETIMEDOUT)");
    });
});
