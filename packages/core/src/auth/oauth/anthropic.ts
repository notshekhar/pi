import { createServer, type Server } from "node:http";
import type { GenericOAuthCredentials } from "../../types";
import { generatePKCE } from "./pkce";
import type { OAuthLoginCallbacks, OAuthProviderInterface } from "./types";

const CLIENT_ID = atob("OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl");
const AUTHORIZE_URL = "https://claude.ai/oauth/authorize";
const TOKEN_URL = "https://platform.claude.com/v1/oauth/token";
const CALLBACK_HOST = process.env.PI_OAUTH_CALLBACK_HOST || "127.0.0.1";
const CALLBACK_PORT = 53692;
const CALLBACK_PATH = "/callback";
const REDIRECT_URI = `http://localhost:${CALLBACK_PORT}${CALLBACK_PATH}`;
const SCOPES =
  "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload";

function parseAuthInput(input: string): { code?: string; state?: string } {
  const v = input.trim();
  if (!v) return {};
  try {
    const u = new URL(v);
    return { code: u.searchParams.get("code") ?? undefined, state: u.searchParams.get("state") ?? undefined };
  } catch {}
  if (v.includes("#")) {
    const [code, state] = v.split("#", 2);
    return { code, state };
  }
  if (v.includes("code=")) {
    const p = new URLSearchParams(v);
    return { code: p.get("code") ?? undefined, state: p.get("state") ?? undefined };
  }
  return { code: v };
}

async function postJson(url: string, body: Record<string, string>): Promise<Record<string, unknown>> {
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(30_000),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`HTTP ${res.status}: ${text}`);
  return JSON.parse(text) as Record<string, unknown>;
}

interface CallbackServerInfo {
  server: Server;
  waitForCode: () => Promise<{ code: string; state: string } | null>;
}

function startCallbackServer(expectedState: string): Promise<CallbackServerInfo> {
  return new Promise((resolve, reject) => {
    let settle: ((v: { code: string; state: string } | null) => void) | undefined;
    const wait = new Promise<{ code: string; state: string } | null>((r) => {
      let done = false;
      settle = (v) => {
        if (done) return;
        done = true;
        r(v);
      };
    });
    const server = createServer((req, res) => {
      const url = new URL(req.url || "", "http://localhost");
      if (url.pathname !== CALLBACK_PATH) {
        res.writeHead(404).end();
        return;
      }
      const code = url.searchParams.get("code");
      const state = url.searchParams.get("state");
      if (!code || state !== expectedState) {
        res.writeHead(400, { "Content-Type": "text/html; charset=utf-8" }).end("<p>Login failed.</p>");
        return;
      }
      res
        .writeHead(200, { "Content-Type": "text/html; charset=utf-8" })
        .end("<p>Anthropic login complete. You can close this window.</p>");
      settle?.({ code, state });
    });
    server.on("error", reject);
    server.listen(CALLBACK_PORT, CALLBACK_HOST, () => resolve({ server, waitForCode: () => wait }));
  });
}

async function exchangeCode(code: string, state: string, verifier: string): Promise<GenericOAuthCredentials> {
  const data = await postJson(TOKEN_URL, {
    grant_type: "authorization_code",
    client_id: CLIENT_ID,
    code,
    state,
    redirect_uri: REDIRECT_URI,
    code_verifier: verifier,
  });
  return {
    refresh: data.refresh_token as string,
    access: data.access_token as string,
    expires: Date.now() + (data.expires_in as number) * 1000 - 5 * 60 * 1000,
  };
}

async function refreshAnthropic(refreshToken: string): Promise<GenericOAuthCredentials> {
  const data = await postJson(TOKEN_URL, {
    grant_type: "refresh_token",
    client_id: CLIENT_ID,
    refresh_token: refreshToken,
  });
  return {
    refresh: data.refresh_token as string,
    access: data.access_token as string,
    expires: Date.now() + (data.expires_in as number) * 1000 - 5 * 60 * 1000,
  };
}

export const anthropicOAuthProvider: OAuthProviderInterface = {
  id: "anthropic",
  name: "Anthropic (Claude Pro/Max)",

  async login(cb: OAuthLoginCallbacks): Promise<GenericOAuthCredentials> {
    const { verifier, challenge } = await generatePKCE();
    const server = await startCallbackServer(verifier);
    try {
      const params = new URLSearchParams({
        code: "true",
        client_id: CLIENT_ID,
        response_type: "code",
        redirect_uri: REDIRECT_URI,
        scope: SCOPES,
        code_challenge: challenge,
        code_challenge_method: "S256",
        state: verifier,
      });
      cb.onAuth({
        url: `${AUTHORIZE_URL}?${params}`,
        instructions: "Complete login in browser. Or paste the redirect URL/code here.",
      });

      let code: string | undefined;
      let state: string | undefined;
      const result = await Promise.race([
        server.waitForCode(),
        cb
          .onPrompt({
            message: "Paste authorization code or redirect URL (or wait for browser):",
            allowEmpty: true,
          })
          .then((input) => {
            const p = parseAuthInput(input);
            return p.code ? { code: p.code, state: p.state ?? verifier } : null;
          }),
      ]);
      if (result) {
        code = result.code;
        state = result.state;
      }
      if (!code || !state) throw new Error("Missing authorization code");
      if (state !== verifier) throw new Error("OAuth state mismatch");
      return exchangeCode(code, state, verifier);
    } finally {
      server.server.close();
    }
  },

  async refreshToken(creds: GenericOAuthCredentials): Promise<GenericOAuthCredentials> {
    return refreshAnthropic(creds.refresh);
  },

  getApiKey(creds: GenericOAuthCredentials): string {
    return creds.access;
  },
};
