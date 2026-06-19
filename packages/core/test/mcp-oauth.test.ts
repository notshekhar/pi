import { describe, expect, test } from "bun:test";
import { LoopOAuthProvider, oauthClientOptions } from "../src/mcp/oauth";
import type { HttpServerConfig } from "../src/mcp/config";

const REDIRECT = "http://127.0.0.1:8976/callback";

describe("MCP OAuth: pre-registered client support", () => {
    test("a configured clientId is handed to the SDK, skipping dynamic registration", () => {
        const p = new LoopOAuthProvider("test-figma", REDIRECT, undefined, {
            clientId: "abc123",
            clientSecret: "shh",
            scopes: ["mcp:connect"],
        });
        expect(p.clientInformation()).toEqual({ client_id: "abc123", client_secret: "shh" });
    });

    test("a confidential client (secret) negotiates client_secret_post + requested scope", () => {
        const p = new LoopOAuthProvider("test-figma", REDIRECT, undefined, {
            clientId: "abc123",
            clientSecret: "shh",
            scopes: ["mcp:connect", "files:read"],
        });
        expect(p.clientMetadata.token_endpoint_auth_method).toBe("client_secret_post");
        expect(p.clientMetadata.scope).toBe("mcp:connect files:read");
    });

    test("a public client (no secret) stays PKCE-only (token_endpoint_auth_method=none)", () => {
        const p = new LoopOAuthProvider("test-pub-" + Date.now(), REDIRECT);
        expect(p.clientMetadata.token_endpoint_auth_method).toBe("none");
        expect(p.clientMetadata.scope).toBeUndefined();
        // No configured client and nothing stored → undefined, so the SDK runs
        // dynamic registration as before.
        expect(p.clientInformation()).toBeUndefined();
    });

    test("oauthClientOptions resolves ${env:VAR} secrets from the environment", () => {
        process.env.FIGMA_TEST_SECRET = "from-env";
        const cfg: HttpServerConfig = {
            type: "http",
            url: "https://mcp.figma.com/mcp",
            auth: "oauth",
            clientId: "client-x",
            clientSecret: "${env:FIGMA_TEST_SECRET}",
            scopes: ["mcp:connect"],
        };
        const opts = oauthClientOptions(cfg);
        expect(opts.clientId).toBe("client-x");
        expect(opts.clientSecret).toBe("from-env");
        expect(opts.scopes).toEqual(["mcp:connect"]);
        delete process.env.FIGMA_TEST_SECRET;
    });
});
