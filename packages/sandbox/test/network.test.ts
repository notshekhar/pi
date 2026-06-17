import { afterAll, describe, expect, test } from "bun:test";
import net from "node:net";
import { createServer as createEchoServer } from "node:net";
import { matchesDomainPattern, makeHostFilter } from "../src/domain-filter";
import { createHttpProxyServer } from "../src/http-proxy";
import { sandbox, defaultSandboxConfig, type SandboxConfig } from "../src/index";

const shell = "/bin/bash";

afterAll(async () => {
    await sandbox.reset();
});

describe("matchesDomainPattern", () => {
    test("exact match", () => {
        expect(matchesDomainPattern("github.com", "github.com")).toBe(true);
        expect(matchesDomainPattern("evil.com", "github.com")).toBe(false);
    });
    test("wildcard *.domain matches subdomains, not the apex or IPs", () => {
        expect(matchesDomainPattern("api.github.com", "*.github.com")).toBe(true);
        expect(matchesDomainPattern("github.com", "*.github.com")).toBe(false);
        expect(matchesDomainPattern("1.2.3.4", "*.github.com")).toBe(false);
    });
    test("bare * is deny-all", () => {
        expect(matchesDomainPattern("anything.example", "*")).toBe(true);
    });
});

describe("makeHostFilter", () => {
    const filter = makeHostFilter(() => ({ allow: ["*.github.com", "localhost"], deny: ["bad.github.com"] }));
    test("allows allowlisted hosts", () => {
        expect(filter(443, "api.github.com")).toBe(true);
        expect(filter(80, "localhost")).toBe(true);
    });
    test("deny wins over allow", () => {
        expect(filter(443, "bad.github.com")).toBe(false);
    });
    test("unmatched host is denied", () => {
        expect(filter(443, "example.com")).toBe(false);
    });
    test("malformed hosts (control chars / zone IDs) are denied", () => {
        expect(filter(443, "evil.com\x00.github.com")).toBe(false);
        expect(filter(443, "::ffff:1.2.3.4%x.github.com")).toBe(false);
    });
    test("canonicalizes IP shorthand before matching deny", () => {
        const f = makeHostFilter(() => ({ allow: [], deny: ["169.254.169.254"] }));
        expect(f(80, "2852039166")).toBe(false); // = 169.254.169.254
    });
});

// End-to-end: drive the HTTP CONNECT proxy with a raw socket against a local
// echo server. No internet — "localhost" resolves to 127.0.0.1.
describe("HTTP proxy CONNECT filtering (functional)", () => {
    test("allowed host tunnels through; denied host gets 403", async () => {
        const echo = createEchoServer((sock) => sock.pipe(sock));
        await new Promise<void>((r) => echo.listen(0, "127.0.0.1", r));
        const echoPort = (echo.address() as net.AddressInfo).port;

        const proxy = createHttpProxyServer({ filter: makeHostFilter(() => ({ allow: ["localhost"], deny: [] })) });
        await new Promise<void>((r) => proxy.listen(0, "127.0.0.1", r));
        const proxyPort = (proxy.address() as net.AddressInfo).port;

        const connect = (authority: string): Promise<string> =>
            new Promise((resolve, reject) => {
                const c = net.connect(proxyPort, "127.0.0.1", () => {
                    c.write(`CONNECT ${authority} HTTP/1.1\r\nHost: ${authority}\r\n\r\n`);
                });
                let buf = "";
                c.on("data", (d) => {
                    buf += d.toString("latin1");
                    if (buf.includes("\r\n\r\n")) {
                        resolve(buf.split("\r\n")[0]);
                        c.destroy();
                    }
                });
                c.on("error", reject);
                c.setTimeout(3000, () => {
                    c.destroy();
                    reject(new Error("timeout"));
                });
            });

        try {
            expect(await connect(`localhost:${echoPort}`)).toContain("200");
            expect(await connect(`127.0.0.2:${echoPort}`)).toContain("403");
        } finally {
            echo.close();
            await new Promise<void>((r) => proxy.close(() => r()));
        }
    });
});

describe("sandbox.wrap with network allowlist (macOS)", () => {
    test("starts a proxy and bakes its port + HTTP_PROXY into the wrapped command", async () => {
        if (process.platform !== "darwin") return;
        const cfg: SandboxConfig = { ...defaultSandboxConfig(), network: { allow: ["*.github.com"] } };
        const wrapped = await sandbox.wrap({ command: "echo hi", shell, cwd: process.cwd(), config: cfg });
        expect(wrapped).not.toBeNull();
        // shell-quote escapes "=" and ":", so match on quote-agnostic fragments.
        const line = wrapped!.argv[2];
        expect(line).toContain("HTTP_PROXY");
        expect(line).toContain("localhost"); // proxy URL host
        expect(line).toContain("network-outbound"); // localhost proxy-port allow rule
        expect(line).not.toContain("(allow network*)"); // restricted, not open
    });
});
