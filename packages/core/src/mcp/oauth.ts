/**
 * OAuth 2.0 for MCP servers. Implements the AI SDK's OAuthClientProvider,
 * backed by ~/.loop/mcp-auth.json so tokens survive restarts. The AI SDK's
 * `auth()` helper drives discovery, dynamic client registration, PKCE, and
 * token exchange — this provider just persists each piece and hands the
 * authorization URL to a caller-supplied opener (the browser).
 */
import Configstore from "configstore";
import { join } from "node:path";
import type { OAuthClientInformation, OAuthClientMetadata, OAuthClientProvider, OAuthTokens } from "@ai-sdk/mcp";
import { getLoopDir } from "../auth/storage";
import { resolveSecrets, type HttpServerConfig } from "./config";

/**
 * Optional, user-supplied OAuth client config (from the server entry). When a
 * `clientId` is present we hand it to the SDK as already-registered client
 * information, so it skips dynamic client registration entirely — the escape
 * hatch for servers that gate or forbid anonymous registration (e.g. Figma).
 */
export interface OAuthClientOptions {
    clientId?: string;
    clientSecret?: string;
    scopes?: string[];
}

/** Pull the OAuth client options out of a server config, resolving secrets. */
export function oauthClientOptions(cfg: HttpServerConfig): OAuthClientOptions {
    return {
        clientId: cfg.clientId,
        clientSecret: cfg.clientSecret ? resolveSecrets(cfg.clientSecret) : undefined,
        scopes: cfg.scopes,
    };
}

const mcpAuthStore = new Configstore("loop-agent-mcp-auth", {}, { configPath: join(getLoopDir(), "mcp-auth.json") });

/** Everything we persist for one server's OAuth session. */
interface StoredAuth {
    clientInformation?: OAuthClientInformation;
    tokens?: OAuthTokens;
    codeVerifier?: string;
    state?: string;
}

function read(server: string): StoredAuth {
    return (mcpAuthStore.get(server) as StoredAuth | undefined) ?? {};
}

function write(server: string, patch: Partial<StoredAuth>): void {
    mcpAuthStore.set(server, { ...read(server), ...patch });
}

/** True once a server has completed login (used to decide auto-connect). */
export function hasStoredTokens(server: string): boolean {
    return read(server).tokens?.access_token != null;
}

/** Forget a server's OAuth session entirely (used on /mcp delete or re-auth). */
export function clearMcpAuth(server: string): void {
    mcpAuthStore.delete(server);
}

/**
 * `onRedirect` is called with the provider authorization URL. During a normal
 * background connect it's left undefined, so a server that still needs login
 * surfaces as needs-auth instead of silently popping a browser. The /mcp
 * authorize flow passes a real opener.
 */
export class LoopOAuthProvider implements OAuthClientProvider {
    constructor(
        private readonly server: string,
        private readonly redirectUri: string,
        private readonly onRedirect?: (url: URL) => void,
        private readonly opts: OAuthClientOptions = {},
    ) {}

    get redirectUrl(): string {
        return this.redirectUri;
    }

    get clientMetadata(): OAuthClientMetadata {
        return {
            client_name: "loop",
            redirect_uris: [this.redirectUri],
            grant_types: ["authorization_code", "refresh_token"],
            response_types: ["code"],
            // A configured secret means a confidential client; otherwise we're a
            // public client and authenticate the token request with PKCE alone.
            token_endpoint_auth_method: this.opts.clientSecret ? "client_secret_post" : "none",
            ...(this.opts.scopes?.length ? { scope: this.opts.scopes.join(" ") } : {}),
        };
    }

    clientInformation(): OAuthClientInformation | undefined {
        // A user-supplied client_id short-circuits dynamic registration: hand it
        // straight to the SDK (with the secret for confidential clients). Falls
        // back to whatever a prior registration stored.
        if (this.opts.clientId) {
            return { client_id: this.opts.clientId, client_secret: this.opts.clientSecret };
        }
        return read(this.server).clientInformation;
    }

    saveClientInformation(info: OAuthClientInformation): void {
        write(this.server, { clientInformation: info });
    }

    tokens(): OAuthTokens | undefined {
        return read(this.server).tokens;
    }

    saveTokens(tokens: OAuthTokens): void {
        // RFC 6749 §6: a token-refresh response MAY omit refresh_token, which
        // means "keep using the existing one". Saving the response verbatim
        // would drop the stored refresh_token, so the *next* restart has nothing
        // to refresh with — the SDK then wipes the session (invalidateCredentials),
        // which is why MCP auth "expires" on every relaunch. Carry the previous
        // refresh_token forward whenever the new payload doesn't include one.
        const previous = read(this.server).tokens;
        const merged: OAuthTokens = {
            ...tokens,
            refresh_token: tokens.refresh_token ?? previous?.refresh_token,
        };
        write(this.server, { tokens: merged });
    }

    saveCodeVerifier(codeVerifier: string): void {
        write(this.server, { codeVerifier });
    }

    codeVerifier(): string {
        const verifier = read(this.server).codeVerifier;
        if (!verifier) throw new Error("no PKCE code verifier stored — restart the authorization flow");
        return verifier;
    }

    saveState(state: string): void {
        write(this.server, { state });
    }

    state(): string {
        return read(this.server).state ?? "";
    }

    storedState(): string | undefined {
        return read(this.server).state;
    }

    redirectToAuthorization(authorizationUrl: URL): void {
        if (!this.onRedirect) {
            // Background connect with no opener: signal "login required" rather
            // than open a browser the user didn't ask for.
            throw new McpAuthRequiredError(this.server);
        }
        this.onRedirect(authorizationUrl);
    }

    invalidateCredentials(scope: "all" | "client" | "tokens" | "verifier"): void {
        if (scope === "all") {
            clearMcpAuth(this.server);
            return;
        }
        const patch: Partial<StoredAuth> = {};
        if (scope === "tokens") patch.tokens = undefined;
        else if (scope === "verifier") patch.codeVerifier = undefined;
        else patch.clientInformation = undefined;
        write(this.server, patch);
    }
}

/** Thrown when a server needs interactive login before it can connect. */
export class McpAuthRequiredError extends Error {
    constructor(public readonly server: string) {
        super(`MCP server "${server}" requires authorization — run /mcp and choose Authorize`);
        this.name = "McpAuthRequiredError";
    }
}
