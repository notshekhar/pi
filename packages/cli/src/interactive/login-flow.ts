import { type SelectItem, type TUI } from "@notshekhar/loop-tui";
import chalk from "chalk";
import {
    bustCatalogCache,
    deleteCustomProvider,
    fetchCustomProviderModels,
    getActiveProvider,
    listAuthorizedProviders,
    listCustomProviders,
    loginApiKey,
    loginOAuth,
    loginXaiOAuth,
    logout,
    listOllamaModels,
    ollamaBaseURL,
    PROVIDER_IDS,
    saveCustomProvider,
    setActiveProvider,
    type CustomProviderConfig,
    type ProviderId,
} from "@notshekhar/loop-core";
import type { ChatHistory } from "./components/chat-history";
import { providerLabel } from "./provider-labels";
import { openBrowser } from "../open-browser";

export interface LoginDeps {
    tui: TUI;
    history: ChatHistory;
    selectOnce: (items: SelectItem[], title?: string) => Promise<SelectItem | null>;
    searchOnce: (items: SelectItem[], title?: string, opts?: { initialIndex?: number }) => Promise<SelectItem | null>;
    promptOnce: (label?: string) => Promise<string>;
}

type StepResult = "done" | "back" | "cancel";

function presentAuth(deps: LoginDeps, label: string, url: string, instructions?: string): void {
    const { tui, history } = deps;
    if (instructions) history.addSystem(instructions);
    history.addSystem(chalk.cyan(url));
    const opened = openBrowser(url);
    history.addSystem(chalk.dim(opened ? `(opened in browser — ${label})` : `(open this URL in a browser — ${label})`));
    tui.requestRender();
}

async function pickProvider(deps: LoginDeps): Promise<ProviderId | null> {
    const items: SelectItem[] = [
        ...PROVIDER_IDS.map((id) => ({
            value: id as string,
            label: id as string,
            description: providerLabel(id),
        })),
        {
            value: "custom",
            label: "custom",
            description:
                "Any compatible endpoint/gateway — bifrost, litellm, proxies (OpenAI/Anthropic/Google compatible)",
        },
    ];
    const pick = await deps.searchOnce(items, "Sign in to provider (type to filter)");
    return pick ? (pick.value as ProviderId) : null;
}

/**
 * Custom provider wizard: name → compat sdk → baseURL → key → headers, then
 * model discovery (sdk-appropriate /models call). When the gateway can't list
 * models, falls back to asking the user for model ids — that manual path is
 * always available, discovery is just the happy path.
 */
async function loginCustom(deps: LoginDeps): Promise<StepResult> {
    const { tui, history, promptOnce, selectOnce } = deps;

    history.addSystem("Custom provider — name (lowercase, e.g. bifrost). Esc to go back.");
    tui.requestRender();
    const rawName = await promptOnce("name: ");
    if (!rawName) return "back";
    const name = rawName.trim().toLowerCase();
    if (!/^[a-z0-9-]+$/.test(name)) {
        history.addError("name must be lowercase letters, digits, hyphens");
        tui.requestRender();
        return "back";
    }

    const sdkPick = await selectOnce(
        [
            { value: "anthropic", label: "Anthropic-compatible", description: "Claude API shape (/v1/messages)" },
            {
                value: "openai",
                label: "OpenAI-compatible",
                description: "Chat completions shape (/v1/chat/completions)",
            },
            { value: "google", label: "Google-compatible", description: "Gemini API shape (/v1beta)" },
        ],
        `custom:${name} — which API is the endpoint compatible with?`,
    );
    if (!sdkPick) return "back";
    const sdk = sdkPick.value as CustomProviderConfig["sdk"];

    history.addSystem("Base URL of the endpoint (e.g. http://bifrost.internal/anthropic).");
    tui.requestRender();
    const baseURL = (await promptOnce("baseURL: ")).trim();
    if (!baseURL) return "back";

    history.addSystem("API key (Enter to skip if the gateway only uses headers).");
    tui.requestRender();
    const apiKey = (await promptOnce("apiKey: ")).trim();

    history.addSystem('Extra headers, optional. Format: "X-Header: value; Other-Header: value". Enter to skip.');
    tui.requestRender();
    const headersRaw = (await promptOnce("headers: ")).trim();
    const headers: Record<string, string> = {};
    for (const pair of headersRaw.split(";")) {
        const idx = pair.indexOf(":");
        if (idx > 0) headers[pair.slice(0, idx).trim()] = pair.slice(idx + 1).trim();
    }

    const cfg: CustomProviderConfig = {
        name,
        sdk,
        baseURL,
        apiKey,
        ...(Object.keys(headers).length ? { headers } : {}),
    };

    history.addSystem("Discovering models from the endpoint…");
    tui.requestRender();
    const discovered = await fetchCustomProviderModels(cfg);

    if (discovered?.length) {
        cfg.models = discovered.map((m) => ({
            id: m.id,
            ...(m.name ? { name: m.name } : {}),
            ...(m.contextWindow ? { contextWindow: m.contextWindow } : {}),
            ...(m.maxOutput ? { maxOutput: m.maxOutput } : {}),
        }));
        history.addSystem(chalk.green(`✓ found ${discovered.length} models:`));
        for (const m of discovered.slice(0, 12)) history.addSystem(chalk.dim(`  • ${m.id}`));
        if (discovered.length > 12) history.addSystem(chalk.dim(`  … +${discovered.length - 12} more`));
    } else {
        history.addSystem(
            chalk.yellow("Endpoint doesn't expose a model list — enter model ids manually (comma-separated)."),
        );
        tui.requestRender();
        const ids = (await promptOnce("model ids: "))
            .split(",")
            .map((x) => x.trim())
            .filter(Boolean);
        if (ids.length === 0) {
            history.addError("no models given — custom provider not saved");
            tui.requestRender();
            return "back";
        }
        cfg.models = ids.map((id) => ({ id }));
    }

    saveCustomProvider(cfg);
    setActiveProvider(`custom:${name}`);
    bustCatalogCache();
    history.addSystem(chalk.green(`✓ custom provider "${name}" saved (${cfg.models!.length} models) and set active.`));
    tui.requestRender();
    return "done";
}

async function loginXai(deps: LoginDeps): Promise<StepResult> {
    const { tui, history, promptOnce } = deps;
    const mode = await deps.selectOnce(
        [
            { value: "oauth", label: "Sign in with xAI (SuperGrok subscription)", description: "OAuth, opens browser" },
            { value: "apikey", label: "Use API key", description: "Paste your XAI_API_KEY" },
        ],
        "xAI — authentication method (Esc to go back)",
    );
    if (!mode) return "back";
    if (mode.value === "oauth") {
        history.addSystem("xAI login: launching OAuth in your browser…");
        tui.requestRender();
        try {
            await loginXaiOAuth(({ url, instructions }) => presentAuth(deps, "xAI", url, instructions));
            setActiveProvider("xai");
            history.addSystem(chalk.green("✓ xAI subscription connected."));
        } catch (err) {
            history.addError(`xAI login failed: ${(err as Error).message}`);
        }
        tui.requestRender();
        return "done";
    }
    return apiKeyLogin(deps, "xai", promptOnce);
}

async function loginCopilot(deps: LoginDeps): Promise<StepResult> {
    const { tui, history, promptOnce } = deps;
    history.addSystem("GitHub Copilot: starting device flow…");
    tui.requestRender();
    try {
        await loginOAuth("github-copilot", {
            onAuth: ({ url, instructions }) => presentAuth(deps, "GitHub Copilot", url, instructions),
            onPrompt: async ({ message }) => {
                history.addSystem(message);
                tui.requestRender();
                return promptOnce("");
            },
            onProgress: (msg) => {
                history.addSystem(msg);
                tui.requestRender();
            },
        });
        setActiveProvider("github-copilot");
        bustCatalogCache();
        history.addSystem(chalk.green("✓ GitHub Copilot connected."));
    } catch (err) {
        history.addError(`Copilot login failed: ${(err as Error).message}`);
    }
    tui.requestRender();
    return "done";
}

async function loginOpenAI(deps: LoginDeps): Promise<StepResult> {
    const { promptOnce } = deps;
    const mode = await deps.selectOnce(
        [
            {
                value: "oauth",
                label: "Sign in with ChatGPT (subscription · Codex)",
                description: "OAuth, opens browser — billed to your ChatGPT plan",
            },
            { value: "apikey", label: "Use API key", description: "Paste your OPENAI_API_KEY (pay-as-you-go)" },
        ],
        "OpenAI — authentication method (Esc to go back)",
    );
    if (!mode) return "back";
    if (mode.value === "oauth") return loginChatgpt(deps);
    return apiKeyLogin(deps, "openai", promptOnce);
}

async function loginChatgpt(deps: LoginDeps): Promise<StepResult> {
    const { tui, history, promptOnce } = deps;
    history.addSystem("ChatGPT (Codex): opening browser for sign-in…");
    tui.requestRender();
    try {
        await loginOAuth("openai-chatgpt", {
            onAuth: ({ url, instructions }) => presentAuth(deps, "ChatGPT", url, instructions),
            onPrompt: async ({ message }) => {
                history.addSystem(message);
                tui.requestRender();
                return promptOnce("");
            },
            onProgress: (msg) => {
                history.addSystem(msg);
                tui.requestRender();
            },
        });
        setActiveProvider("openai-chatgpt");
        bustCatalogCache();
        history.addSystem(chalk.green("✓ ChatGPT (Codex) connected."));
        history.addSystem(chalk.dim("Personal/local use only — usage is billed to your ChatGPT subscription."));
    } catch (err) {
        history.addError(`ChatGPT login failed: ${(err as Error).message}`);
    }
    tui.requestRender();
    return "done";
}

async function loginOllama(deps: LoginDeps): Promise<StepResult> {
    const { tui, history } = deps;
    history.addSystem("Ollama: checking local daemon…");
    tui.requestRender();
    const models = await listOllamaModels();
    if (models === null) {
        history.addError(
            `Ollama not reachable at ${ollamaBaseURL()}. Start it (\`ollama serve\`) or set LOOP_OLLAMA_BASE_URL.`,
        );
        tui.requestRender();
        return "done";
    }
    if (models.length === 0) {
        history.addSystem(
            chalk.yellow(
                "Ollama is running but no models are installed. Pull one, e.g. `ollama pull llama3.2`, then retry.",
            ),
        );
        tui.requestRender();
        return "done";
    }
    // Local provider has no key; store a placeholder so it counts as authorized.
    loginApiKey("ollama", "local");
    setActiveProvider("ollama");
    bustCatalogCache();
    history.addSystem(
        chalk.green(`✓ Ollama connected — ${models.length} model${models.length === 1 ? "" : "s"} installed.`),
    );
    tui.requestRender();
    return "done";
}

async function apiKeyLogin(
    deps: LoginDeps,
    p: ProviderId,
    promptOnce: (label?: string) => Promise<string>,
): Promise<StepResult> {
    const { tui, history } = deps;
    history.addSystem(`${p}: paste API key, then press Enter. Esc to go back.`);
    tui.requestRender();
    const key = await promptOnce(`${p.toUpperCase().replace(/-/g, "_")}_API_KEY: `);
    if (!key) return "back";
    loginApiKey(p, key);
    setActiveProvider(p);
    bustCatalogCache();
    history.addSystem(chalk.green(`✓ ${p} key saved.`));
    tui.requestRender();
    return "done";
}

async function loginForProvider(deps: LoginDeps, p: ProviderId): Promise<StepResult> {
    switch (p) {
        case "custom":
            return loginCustom(deps);
        case "xai":
            return loginXai(deps);
        case "openai":
            return loginOpenAI(deps);
        case "github-copilot":
            return loginCopilot(deps);
        case "ollama":
            return loginOllama(deps);
        default:
            return apiKeyLogin(deps, p, deps.promptOnce);
    }
}

export async function startLogin(deps: LoginDeps, target?: string): Promise<void> {
    const { tui, history } = deps;

    // Validate explicit `target` once; if invalid, surface and exit.
    if (target) {
        if (!(PROVIDER_IDS as readonly string[]).includes(target) && target !== "custom") {
            history.addSystem(`unknown provider: ${target}. options: ${[...PROVIDER_IDS, "custom"].join(", ")}`);
            tui.requestRender();
            return;
        }
        const r = await loginForProvider(deps, target as ProviderId);
        if (r !== "back") return;
        // fall through to interactive picker if user pressed Esc
    }

    // Loop: provider picker → provider-specific flow → may return "back" to here.
    while (true) {
        const p = await pickProvider(deps);
        if (!p) return; // Esc at outermost picker exits the login wizard
        const r = await loginForProvider(deps, p);
        if (r !== "back") return;
        // r === "back": loop and re-open provider picker
    }
}

export async function startLogout(deps: LoginDeps, target?: string): Promise<void> {
    const { tui, history, searchOnce } = deps;
    let pick = target;
    if (!pick) {
        const authed: string[] = [
            ...listAuthorizedProviders(),
            ...listCustomProviders().map((c) => `custom:${c.name}`),
        ];
        if (authed.length === 0) {
            history.addSystem("no providers to sign out from");
            tui.requestRender();
            return;
        }
        const items: SelectItem[] = [
            ...authed.map((id) => ({
                value: id,
                label: id,
                description: id === getActiveProvider() ? "(active)" : "",
            })),
            { value: "__all__", label: chalk.red("all providers"), description: "Sign out from every provider" },
        ];
        const sel = await searchOnce(items, "Sign out of provider (type to filter)");
        if (!sel) return;
        pick = sel.value;
    }
    if (pick === "__all__") {
        logout();
        for (const c of listCustomProviders()) deleteCustomProvider(c.name);
        history.addSystem("signed out of all providers");
    } else if (pick.startsWith("custom:")) {
        deleteCustomProvider(pick.slice("custom:".length));
        bustCatalogCache();
        history.addSystem(`removed custom provider ${pick}`);
    } else {
        logout(pick as ProviderId);
        history.addSystem(`signed out of ${pick}`);
    }
    tui.requestRender();
}
