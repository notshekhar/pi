/**
 * "Sign in with ChatGPT" (OpenAI Codex) OAuth — lets a ChatGPT Plus/Pro/Team
 * subscription drive model calls via OpenAI's Codex backend instead of a
 * pay-as-you-go API key. We reuse the exact public Codex CLI OAuth client and
 * the chatgpt.com Codex Responses endpoint.
 *
 * IMPORTANT (ToS / stability): this is for personal, local, single-user use —
 * the same use-case as the Codex CLI. Do not pool/share tokens or run it as a
 * hosted service. The endpoint is unofficial and can change without notice.
 *
 * Token + account id are stored as GenericOAuthCredentials; the account id is
 * derived from the id_token and sent as the `chatgpt-account-id` header by the
 * provider fetch (see providers/index.ts).
 */
import { createServer } from "node:http";
import type { GenericOAuthCredentials } from "../../types";
import { generatePKCE } from "./pkce";
import type { OAuthLoginCallbacks, OAuthProviderInterface } from "./types";

// Public Codex CLI OAuth client (same values the official CLI uses).
const CLIENT_ID = process.env.LOOP_OPENAI_OAUTH_CLIENT_ID || "app_EMoamEEZ73f0CkXaXp7hrann";
const AUTHORIZE_URL = "https://auth.openai.com/oauth/authorize";
const TOKEN_URL = "https://auth.openai.com/oauth/token";
const SCOPE = "openid profile email offline_access";
// The Codex client is registered with this exact redirect — it must match.
const CALLBACK_HOST = "localhost";
const CALLBACK_PORT = Number.parseInt(process.env.LOOP_OPENAI_OAUTH_CALLBACK_PORT || "1455", 10);
const CALLBACK_PATH = "/auth/callback";
const REDIRECT_URI = `http://${CALLBACK_HOST}:${CALLBACK_PORT}${CALLBACK_PATH}`;
const REFRESH_SKEW_MS = 5 * 60 * 1000;
const LOGIN_TIMEOUT_MS = 180_000;

/** Codex Responses backend — billed to the signed-in ChatGPT subscription. */
export const CODEX_BASE_URL = process.env.LOOP_OPENAI_CODEX_BASE_URL || "https://chatgpt.com/backend-api/codex";

/** Static headers every Codex request needs (besides auth + account id). */
export const OPENAI_CHATGPT_HEADERS: Record<string, string> = {
    "OpenAI-Beta": "responses=experimental",
    originator: "codex_cli_rs",
};

// The account id lives in a nested claim on the id_token.
const JWT_CLAIM_PATH = "https://api.openai.com/auth";

function decodeJwtPayload(token: string): Record<string, unknown> | null {
    const parts = token.split(".");
    if (parts.length < 2) return null;
    try {
        const b64 = parts[1].replace(/-/g, "+").replace(/_/g, "/");
        return JSON.parse(atob(b64)) as Record<string, unknown>;
    } catch {
        return null;
    }
}

/** Pull the ChatGPT account id out of the id_token (needed for the API header). */
export function accountIdFromIdToken(idToken: string | undefined): string | undefined {
    if (!idToken) return undefined;
    const claims = decodeJwtPayload(idToken);
    const auth = claims?.[JWT_CLAIM_PATH] as Record<string, unknown> | undefined;
    const id = auth?.chatgpt_account_id;
    return typeof id === "string" && id ? id : undefined;
}

function parseAuthInput(input: string): { code?: string; state?: string } {
    const v = input.trim();
    if (v.includes("://")) {
        try {
            const u = new URL(v);
            return { code: u.searchParams.get("code") ?? undefined, state: u.searchParams.get("state") ?? undefined };
        } catch {
            // fall through
        }
    }
    if (v.includes("code=")) {
        const p = new URLSearchParams(v.startsWith("?") ? v.slice(1) : v);
        return { code: p.get("code") ?? undefined, state: p.get("state") ?? undefined };
    }
    if (v.includes("#")) {
        const [code, state] = v.split("#", 2);
        return { code, state };
    }
    return { code: v || undefined };
}

interface CallbackResult {
    code?: string;
    state?: string;
    error?: string;
}

/** Loopback listener on the fixed Codex redirect (localhost:1455/auth/callback). */
function startCallbackServer(expectedState: string) {
    let settle: ((v: CallbackResult) => void) | undefined;
    const wait = new Promise<CallbackResult>((r) => {
        settle = r;
    });
    const server = createServer((req, res) => {
        const url = new URL(req.url || "/", `http://${CALLBACK_HOST}`);
        if (url.pathname !== CALLBACK_PATH) {
            res.statusCode = 404;
            res.end("Not found");
            return;
        }
        const error = url.searchParams.get("error") ?? undefined;
        const code = url.searchParams.get("code") ?? undefined;
        const state = url.searchParams.get("state") ?? undefined;
        const ok = !error && code && state === expectedState;
        res.statusCode = ok ? 200 : 400;
        res.setHeader("Content-Type", "text/html; charset=utf-8");
        res.end(
            ok
                ? "<html><body><h1>ChatGPT authorization received.</h1>You can close this tab and return to loop.</body></html>"
                : "<html><body><h1>ChatGPT authorization failed.</h1>You can close this tab.</body></html>",
        );
        settle?.({ code, state, error });
    });
    const ready = new Promise<void>((resolve, reject) => {
        server.once("error", reject);
        server.listen(CALLBACK_PORT, CALLBACK_HOST, () => {
            server.removeAllListeners("error");
            resolve();
        });
    });
    return { server, ready, waitForCallback: () => wait };
}

function credsFromTokenResponse(
    payload: Record<string, unknown>,
    prev?: GenericOAuthCredentials,
): GenericOAuthCredentials {
    const access = String(payload.access_token ?? "");
    if (!access) throw new Error("OpenAI token response missing access_token");
    // A refresh response may omit refresh_token — keep the existing one (RFC 6749 §6).
    const refresh = String(payload.refresh_token ?? prev?.refresh ?? "");
    const idToken = String(payload.id_token ?? prev?.idToken ?? "");
    const expiresIn = typeof payload.expires_in === "number" ? payload.expires_in : Number(payload.expires_in ?? 3600);
    const accountId = accountIdFromIdToken(idToken) ?? (prev?.accountId as string | undefined);
    return {
        access,
        refresh,
        expires: Date.now() + expiresIn * 1000 - REFRESH_SKEW_MS,
        idToken,
        accountId,
    };
}

async function exchangeCode(code: string, verifier: string): Promise<GenericOAuthCredentials> {
    const res = await fetch(TOKEN_URL, {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
        body: new URLSearchParams({
            grant_type: "authorization_code",
            client_id: CLIENT_ID,
            code,
            redirect_uri: REDIRECT_URI,
            code_verifier: verifier,
        }),
    });
    if (!res.ok) throw new Error(`OpenAI token exchange failed: ${res.status} ${await res.text()}`);
    const creds = credsFromTokenResponse((await res.json()) as Record<string, unknown>);
    if (!creds.refresh) throw new Error("OpenAI token exchange did not return a refresh_token");
    return creds;
}

export const openaiChatgptOAuthProvider: OAuthProviderInterface = {
    id: "openai-chatgpt",
    name: "ChatGPT (Codex)",

    async login(cb: OAuthLoginCallbacks): Promise<GenericOAuthCredentials> {
        const { verifier, challenge } = await generatePKCE();
        const state = crypto.randomUUID();

        const authUrl = new URL(AUTHORIZE_URL);
        authUrl.searchParams.set("response_type", "code");
        authUrl.searchParams.set("client_id", CLIENT_ID);
        authUrl.searchParams.set("redirect_uri", REDIRECT_URI);
        authUrl.searchParams.set("scope", SCOPE);
        authUrl.searchParams.set("code_challenge", challenge);
        authUrl.searchParams.set("code_challenge_method", "S256");
        authUrl.searchParams.set("state", state);
        authUrl.searchParams.set("id_token_add_organizations", "true");
        authUrl.searchParams.set("codex_cli_simplified_flow", "true");
        authUrl.searchParams.set("originator", "codex_cli_rs");

        // Prefer the loopback listener; fall back to manual paste if the fixed
        // port (1455) is taken or the browser can't reach this machine.
        let callback: ReturnType<typeof startCallbackServer> | null = startCallbackServer(state);
        try {
            await callback.ready;
        } catch {
            callback.server.close();
            callback = null;
        }

        cb.onAuth({
            url: authUrl.toString(),
            instructions: callback
                ? "Sign in with ChatGPT in the browser, then return to loop. Or paste the redirect URL/code here."
                : "Sign in with ChatGPT, then paste the full redirect URL (or code) here.",
        });

        try {
            let code: string | undefined;
            let returnedState: string | undefined;

            if (callback) {
                const race = await Promise.race([
                    callback.waitForCallback(),
                    cb
                        .onPrompt({ message: "Paste redirect URL or code (or wait for the browser)", allowEmpty: true })
                        .then((v): CallbackResult => ({ ...parseAuthInput(v) }))
                        .catch((): CallbackResult => ({})),
                    new Promise<CallbackResult>((resolve) =>
                        setTimeout(() => resolve({ error: "timeout" }), LOGIN_TIMEOUT_MS),
                    ),
                ]);
                if (race.error === "timeout") throw new Error("ChatGPT login timed out");
                code = race.code;
                returnedState = race.state;
            } else {
                const pasted = await cb.onPrompt({ message: "Paste redirect URL or code" });
                const parsed = parseAuthInput(pasted);
                code = parsed.code;
                returnedState = parsed.state;
            }

            if (cb.signal?.aborted) throw new Error("Login cancelled");
            if (!code) throw new Error("ChatGPT login did not return an authorization code");
            // Loopback results carry state; a manually pasted bare code may not.
            if (returnedState && returnedState !== state)
                throw new Error("ChatGPT OAuth state mismatch — possible CSRF");

            return await exchangeCode(code, verifier);
        } finally {
            callback?.server.close();
        }
    },

    async refreshToken(creds: GenericOAuthCredentials): Promise<GenericOAuthCredentials> {
        if (!creds.refresh) throw new Error("Missing refresh_token. Re-login required: /login openai-chatgpt");
        const res = await fetch(TOKEN_URL, {
            method: "POST",
            headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
            body: new URLSearchParams({
                grant_type: "refresh_token",
                client_id: CLIENT_ID,
                refresh_token: creds.refresh,
            }),
        });
        if (!res.ok) throw new Error(`OpenAI token refresh failed: ${res.status} ${await res.text()}`);
        return credsFromTokenResponse((await res.json()) as Record<string, unknown>, creds);
    },

    getApiKey(creds: GenericOAuthCredentials): string {
        return creds.access;
    },
};
