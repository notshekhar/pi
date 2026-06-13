/**
 * Drives the interactive OAuth login for one MCP server. Core stays
 * UI-agnostic: the caller supplies `openUrl` (the browser opener lives in the
 * CLI) and we resolve once tokens are stored.
 */
import { auth } from "@ai-sdk/mcp";
import { isHttpServer, type McpServerConfig } from "./config";
import { clearMcpAuth, PiOAuthProvider } from "./oauth";
import { startCallbackServer } from "./oauth-callback";

const LOGIN_TIMEOUT_MS = 180_000;

/**
 * Runs discovery → registration → browser consent → token exchange. Throws on
 * failure (bad config, timeout, denied consent); resolves when tokens are
 * persisted and the server is ready to connect.
 */
export async function authorizeServer(
    name: string,
    cfg: McpServerConfig,
    openUrl: (url: string) => void,
): Promise<void> {
    if (!isHttpServer(cfg)) {
        throw new Error(`server "${name}" is not an HTTP server — OAuth only applies to http/sse servers`);
    }

    // The server advertises its authorization server via the 401
    // WWW-Authenticate `resource_metadata` URL. Without it, auth() falls back to
    // treating the MCP URL as the issuer and rejects the real (e.g. Keycloak)
    // auth server. The transport passes this automatically on a live connect;
    // our explicit login must fetch it ourselves.
    const resourceMetadataUrl = await discoverResourceMetadataUrl(cfg.url);

    const callback = await startCallbackServer();
    try {
        // Start clean: the dynamically-registered client is bound to the
        // redirect URI it was created with. Re-registering against this run's
        // live callback URI avoids a redirect_uri mismatch if a stale client
        // (e.g. from a background connect) was registered on a different port.
        clearMcpAuth(name);
        const provider = new PiOAuthProvider(name, callback.redirectUri, (url) => openUrl(url.toString()));

        // First pass: no auth code yet → provider.redirectToAuthorization fires,
        // the browser opens, and auth() returns "REDIRECT".
        const first = await auth(provider, { serverUrl: cfg.url, resourceMetadataUrl });
        if (first === "AUTHORIZED") return; // already had a valid session

        const { code, state } = await callback.waitForCode(LOGIN_TIMEOUT_MS);

        // Second pass: exchange the code for tokens (saved via saveTokens).
        const result = await auth(provider, {
            serverUrl: cfg.url,
            authorizationCode: code,
            callbackState: state,
            resourceMetadataUrl,
        });
        if (result !== "AUTHORIZED") {
            throw new Error("authorization did not complete");
        }
    } finally {
        callback.close();
    }
}

/**
 * Reads the `resource_metadata` URL from the server's 401 WWW-Authenticate
 * header (RFC 9728). Returns undefined when the server doesn't advertise one —
 * the caller then relies on the SDK's default well-known discovery.
 */
async function discoverResourceMetadataUrl(serverUrl: string): Promise<URL | undefined> {
    let response: Response;
    try {
        response = await fetch(serverUrl, {
            method: "POST",
            headers: { "content-type": "application/json", accept: "application/json, text/event-stream" },
            body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "ping" }),
        });
    } catch {
        return undefined;
    }
    const header = response.headers.get("www-authenticate");
    const match = header?.match(/resource_metadata="([^"]+)"/i);
    if (!match) return undefined;
    try {
        return new URL(match[1]);
    } catch {
        return undefined;
    }
}
