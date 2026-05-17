import { authStore } from "./storage";
import { login as xaiLogin, refresh as xaiRefresh } from "./xai-oauth";
import { XaiErrorCode, XaiOAuthError } from "./errors";
import type { AuthEntry, ProviderId, XaiOAuthCredentials } from "../types";

export { XaiOAuthError, XaiErrorCode } from "./errors";
export { authStore, settingsStore, costStore, getPiDir } from "./storage";

type ProviderMap = Record<string, AuthEntry>;

function readProviders(): ProviderMap {
  return (authStore.get("providers") as ProviderMap) ?? {};
}

function writeProviders(p: ProviderMap): void {
  authStore.set("providers", p);
}

export function listAuthorizedProviders(): ProviderId[] {
  return Object.keys(readProviders()) as ProviderId[];
}

export function getActiveProvider(): ProviderId | null {
  return (authStore.get("active") as ProviderId | null) ?? null;
}

export function setActiveProvider(p: ProviderId): void {
  authStore.set("active", p);
}

export function loginApiKey(provider: ProviderId, apiKey: string): void {
  const providers = readProviders();
  providers[provider] = { mode: "apikey", provider, apiKey };
  writeProviders(providers);
  if (!getActiveProvider()) setActiveProvider(provider);
}

export function getApiKey(provider: ProviderId): string | undefined {
  const entry = readProviders()[provider];
  if (entry?.mode === "apikey") return entry.apiKey;
  return process.env[`${provider.toUpperCase()}_API_KEY`];
}

export async function loginXaiOAuth(onAuth: (info: { url: string; instructions: string }) => void): Promise<void> {
  const creds = await xaiLogin({ onAuth });
  const providers = readProviders();
  providers.xai = { mode: "oauth", provider: "xai", xai: creds };
  writeProviders(providers);
  if (!getActiveProvider()) setActiveProvider("xai");
}

export function getXaiCreds(): XaiOAuthCredentials | undefined {
  const entry = readProviders().xai;
  if (entry?.mode === "oauth") return entry.xai;
  return undefined;
}

export async function getAccessToken(
  provider: "xai",
  opts: { forceRefresh?: boolean } = {},
): Promise<string> {
  const creds = getXaiCreds();
  if (!creds) throw new XaiOAuthError("No xAI OAuth credentials.", XaiErrorCode.AUTH_MISSING, true);
  const expired = Date.now() >= creds.expires;
  if (!opts.forceRefresh && !expired) return creds.access;
  const fresh = await xaiRefresh(creds);
  const providers = readProviders();
  providers.xai = { mode: "oauth", provider: "xai", xai: fresh };
  writeProviders(providers);
  return fresh.access;
}

export function logout(provider?: ProviderId): void {
  if (!provider) {
    writeProviders({});
    authStore.set("active", null);
    return;
  }
  const providers = readProviders();
  delete providers[provider];
  writeProviders(providers);
  if (getActiveProvider() === provider) {
    const remaining = Object.keys(providers) as ProviderId[];
    authStore.set("active", remaining[0] ?? null);
  }
}

export function getAuthMode(provider: ProviderId): "apikey" | "oauth" | "missing" {
  const entry = readProviders()[provider];
  if (!entry) return "missing";
  return entry.mode;
}
