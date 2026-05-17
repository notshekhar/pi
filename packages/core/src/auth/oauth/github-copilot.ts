import type { GenericOAuthCredentials } from "../../types";
import type { OAuthLoginCallbacks, OAuthProviderInterface } from "./types";

// Pi-mono compat: client id, headers (atob to avoid trivial scraping).
const CLIENT_ID = atob("SXYxLmI1MDdhMDhjODdlY2ZlOTg=");

const COPILOT_HEADERS = {
  "User-Agent": "GitHubCopilotChat/0.35.0",
  "Editor-Version": "vscode/1.107.0",
  "Editor-Plugin-Version": "copilot-chat/0.35.0",
  "Copilot-Integration-Id": "vscode-chat",
} as const;

export function normalizeDomain(input: string): string | null {
  const t = input.trim();
  if (!t) return null;
  try {
    const url = t.includes("://") ? new URL(t) : new URL(`https://${t}`);
    return url.hostname;
  } catch {
    return null;
  }
}

function urls(domain: string) {
  return {
    deviceCode: `https://${domain}/login/device/code`,
    accessToken: `https://${domain}/login/oauth/access_token`,
    copilotToken: `https://api.${domain}/copilot_internal/v2/token`,
  };
}

export function getCopilotBaseUrl(token?: string, enterpriseDomain?: string): string {
  if (token) {
    const m = token.match(/proxy-ep=([^;]+)/);
    if (m) return `https://${m[1].replace(/^proxy\./, "api.")}`;
  }
  if (enterpriseDomain) return `https://copilot-api.${enterpriseDomain}`;
  return "https://api.individual.githubcopilot.com";
}

async function fetchJson(url: string, init: RequestInit): Promise<Record<string, unknown>> {
  const res = await fetch(url, init);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}: ${await res.text()}`);
  return (await res.json()) as Record<string, unknown>;
}

function abortableSleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(new Error("Login cancelled"));
    const t = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => { clearTimeout(t); reject(new Error("Login cancelled")); }, { once: true });
  });
}

async function refreshCopilotToken(refreshToken: string, enterpriseDomain?: string): Promise<GenericOAuthCredentials> {
  const u = urls(enterpriseDomain || "github.com");
  const raw = await fetchJson(u.copilotToken, {
    headers: { Accept: "application/json", Authorization: `Bearer ${refreshToken}`, ...COPILOT_HEADERS },
  });
  const token = raw.token;
  const expiresAt = raw.expires_at;
  if (typeof token !== "string" || typeof expiresAt !== "number") throw new Error("Invalid Copilot token response");
  return {
    refresh: refreshToken,
    access: token,
    expires: expiresAt * 1000 - 5 * 60 * 1000,
    enterpriseUrl: enterpriseDomain,
  };
}

export const githubCopilotOAuthProvider: OAuthProviderInterface = {
  id: "github-copilot",
  name: "GitHub Copilot",

  async login(cb: OAuthLoginCallbacks): Promise<GenericOAuthCredentials> {
    const input = await cb.onPrompt({
      message: "GitHub Enterprise URL/domain (blank for github.com)",
      placeholder: "company.ghe.com",
      allowEmpty: true,
    });
    if (cb.signal?.aborted) throw new Error("Login cancelled");
    const enterpriseDomain = normalizeDomain(input);
    if (input.trim() && !enterpriseDomain) throw new Error("Invalid GitHub Enterprise URL/domain");
    const domain = enterpriseDomain || "github.com";
    const u = urls(domain);

    const dev = await fetchJson(u.deviceCode, {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/x-www-form-urlencoded",
        "User-Agent": COPILOT_HEADERS["User-Agent"],
      },
      body: new URLSearchParams({ client_id: CLIENT_ID, scope: "read:user" }),
    });
    const deviceCode = dev.device_code as string;
    const userCode = dev.user_code as string;
    const verificationUri = dev.verification_uri as string;
    const interval = dev.interval as number;
    const expiresIn = dev.expires_in as number;

    cb.onAuth({ url: verificationUri, instructions: `Enter code: ${userCode}` });

    const deadline = Date.now() + expiresIn * 1000;
    let intervalMs = Math.max(1000, interval * 1000);
    while (Date.now() < deadline) {
      if (cb.signal?.aborted) throw new Error("Login cancelled");
      await abortableSleep(intervalMs, cb.signal);
      const raw = await fetchJson(u.accessToken, {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/x-www-form-urlencoded",
          "User-Agent": COPILOT_HEADERS["User-Agent"],
        },
        body: new URLSearchParams({
          client_id: CLIENT_ID,
          device_code: deviceCode,
          grant_type: "urn:ietf:params:oauth:grant-type:device_code",
        }),
      });
      if (typeof raw.access_token === "string") {
        return refreshCopilotToken(raw.access_token, enterpriseDomain ?? undefined);
      }
      if (raw.error === "authorization_pending") continue;
      if (raw.error === "slow_down") {
        if (typeof raw.interval === "number") intervalMs = raw.interval * 1000;
        else intervalMs += 5000;
        continue;
      }
      throw new Error(`Device flow failed: ${raw.error}${raw.error_description ? `: ${raw.error_description}` : ""}`);
    }
    throw new Error("Device flow timed out");
  },

  async refreshToken(creds: GenericOAuthCredentials): Promise<GenericOAuthCredentials> {
    return refreshCopilotToken(creds.refresh, creds.enterpriseUrl);
  },

  getApiKey(creds: GenericOAuthCredentials): string {
    return creds.access;
  },
};

export { COPILOT_HEADERS };
