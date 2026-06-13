/**
 * Which providers the user can actually use right now: logged-in providers,
 * a detected local ollama daemon, and saved custom gateways. Shared by the
 * /provider picker and the startup "no model selected" guidance so both agree
 * on what "you have a provider" means.
 */
import { getCatalog, listAuthorizedProviders, listCustomProviders, type ProviderId } from "@notshekhar/pi-core";

export async function listUsableProviders(): Promise<ProviderId[]> {
    const providers = [...listAuthorizedProviders()];

    // ollama needs no login — it's usable whenever the daemon is detected.
    const catalog = await getCatalog();
    for (const model of Object.values(catalog)) {
        if (model.available && model.provider === "ollama" && !providers.includes(model.provider)) {
            providers.push(model.provider);
        }
    }

    for (const custom of listCustomProviders()) {
        const id = `custom:${custom.name}` as ProviderId;
        if (!providers.includes(id)) providers.push(id);
    }

    return providers;
}
