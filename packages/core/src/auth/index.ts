import { authStore } from "./storage";
import { login as xaiLogin, refresh as xaiRefresh } from "./xai-oauth";
import { XaiErrorCode, XaiOAuthError } from "./errors";
import { getOAuthProvider } from "./oauth/registry";
import type { OAuthLoginCallbacks } from "./oauth/types";
import type {
    AuthEntry,
    CustomProviderConfig,
    GenericOAuthCredentials,
    ProviderId,
    XaiOAuthCredentials,
} from "../types";

export { XaiOAuthError, XaiErrorCode } from "./errors";
export { authStore, settingsStore, costStore, getPiDir, getProjectModel, setProjectModel } from "./storage";

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
    if (entry?.mode === "oauth" && "xai" in entry) return entry.xai;
    return undefined;
}

export async function getAccessToken(provider: "xai", opts: { forceRefresh?: boolean } = {}): Promise<string> {
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

// ─── Generic OAuth (anthropic, github-copilot) ────────────

export async function loginOAuth(provider: ProviderId, cb: OAuthLoginCallbacks): Promise<void> {
    const impl = getOAuthProvider(provider);
    if (!impl) throw new Error(`No OAuth provider registered for ${provider}`);
    const creds = await impl.login(cb);
    const providers = readProviders();
    providers[provider] = { mode: "oauth", provider, creds };
    writeProviders(providers);
    if (!getActiveProvider()) setActiveProvider(provider);
}

function getGenericOAuthCreds(provider: ProviderId): GenericOAuthCredentials | undefined {
    const entry = readProviders()[provider];
    if (entry?.mode === "oauth" && "creds" in entry) return entry.creds;
    return undefined;
}

/**
 * Returns bearer token for a provider. Resolves stored API key, OAuth creds
 * (auto-refresh + persist), or env var fallback. Pi-mono pattern.
 */
export async function resolveAuthToken(provider: ProviderId): Promise<string | null> {
    const providers = readProviders();
    const entry = providers[provider];

    if (entry?.mode === "apikey") return entry.apiKey;

    if (entry?.mode === "oauth" && "creds" in entry) {
        const impl = getOAuthProvider(provider);
        if (!impl) return null;
        let creds = entry.creds;
        if (Date.now() >= creds.expires) {
            try {
                creds = await impl.refreshToken(creds);
                providers[provider] = { mode: "oauth", provider, creds };
                writeProviders(providers);
            } catch {
                return null;
            }
        }
        return impl.getApiKey(creds);
    }

    // env var fallback (PROVIDER_API_KEY uppercased, with - → _)
    const envKey = `${provider.replace(/-/g, "_").toUpperCase()}_API_KEY`;
    return process.env[envKey] ?? null;
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

// ─── Custom providers ─────────────────────────────────────────────────────────

function readCustom(): Record<string, CustomProviderConfig> {
    return (authStore.get("customProviders") as Record<string, CustomProviderConfig>) ?? {};
}

function writeCustom(p: Record<string, CustomProviderConfig>): void {
    authStore.set("customProviders", p);
}

export function listCustomProviders(): CustomProviderConfig[] {
    return Object.values(readCustom());
}

export function getCustomProvider(name: string): CustomProviderConfig | undefined {
    return readCustom()[name];
}

export function saveCustomProvider(config: CustomProviderConfig): void {
    const all = readCustom();
    all[config.name] = config;
    writeCustom(all);
    if (!getActiveProvider()) setActiveProvider(`custom:${config.name}`);
}

export function deleteCustomProvider(name: string): void {
    const all = readCustom();
    delete all[name];
    writeCustom(all);
    if (getActiveProvider() === `custom:${name}`) {
        authStore.set("active", null);
    }
}

export function isCustomProvider(id: string): boolean {
    return id.startsWith("custom:");
}

export function parseCustomProviderId(id: string): string | null {
    return isCustomProvider(id) ? id.slice("custom:".length) : null;
}

/**
 * The vendor API shape a provider actually speaks: custom providers (gateways
 * like bifrost) map to their configured sdk, so e.g. an anthropic-compatible
 * gateway gets anthropic-specific behavior (prompt caching, thinking budget).
 */
export function effectiveSdkProvider(provider: string): string {
    if (!isCustomProvider(provider)) return provider;
    const sdk = getCustomProvider(parseCustomProviderId(provider)!)?.sdk;
    if (!sdk) return provider;
    return sdk === "openai-compatible" ? "openai" : sdk;
}
