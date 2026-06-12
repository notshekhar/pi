import { describe, expect, test } from "bun:test";
import { isHttpUrl } from "../src/tools/utils/fetch-url";

describe("isHttpUrl", () => {
    test("matches http and https, case-insensitive, trims", () => {
        expect(isHttpUrl("http://example.com")).toBe(true);
        expect(isHttpUrl("https://example.com/page")).toBe(true);
        expect(isHttpUrl("  HTTPS://EXAMPLE.com ")).toBe(true);
    });

    test("rejects local paths and other schemes", () => {
        expect(isHttpUrl("/Users/x/file.ts")).toBe(false);
        expect(isHttpUrl("./rel/path.md")).toBe(false);
        expect(isHttpUrl("file:///etc/hosts")).toBe(false);
        expect(isHttpUrl("ftp://host/x")).toBe(false);
        expect(isHttpUrl("notaurl")).toBe(false);
    });
});
