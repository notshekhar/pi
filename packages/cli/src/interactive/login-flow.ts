import { type SelectItem, type TUI } from "@earendil-works/pi-tui";
import chalk from "chalk";
import {
  bustCatalogCache,
  getActiveProvider,
  listAuthorizedProviders,
  loginApiKey,
  loginOAuth,
  loginXaiOAuth,
  logout,
  PROVIDER_IDS,
  setActiveProvider,
  type ProviderId,
} from "@pi/core";
import type { ChatHistory } from "./components/chat-history";
import { providerLabel } from "./provider-labels";
import { openBrowser } from "../open-browser";

export interface LoginDeps {
  tui: TUI;
  history: ChatHistory;
  selectOnce: (items: SelectItem[], title?: string) => Promise<SelectItem | null>;
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
  const items: SelectItem[] = PROVIDER_IDS.map((id) => ({
    value: id,
    label: id,
    description: providerLabel(id),
  }));
  const pick = await deps.selectOnce(items, "Sign in to provider");
  return pick ? (pick.value as ProviderId) : null;
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

async function loginClaudeAgent(deps: LoginDeps): Promise<StepResult> {
  const { tui, history, promptOnce } = deps;
  const mode = await deps.selectOnce(
    [
      { value: "apikey", label: "API key", description: "Paste your ANTHROPIC_API_KEY" },
      { value: "oauth", label: "Claude Pro/Max OAuth", description: "Subscription bearer (no API spend)" },
    ],
    "Claude Agent — authentication method (Esc to go back)",
  );
  if (!mode) return "back";
  if (mode.value === "oauth") {
    history.addSystem("Anthropic OAuth: opening browser…");
    tui.requestRender();
    try {
      await loginOAuth("claude-agent", {
        onAuth: ({ url, instructions }) => presentAuth(deps, "Anthropic", url, instructions),
        onPrompt: async ({ message }) => {
          history.addSystem(message);
          tui.requestRender();
          return promptOnce("");
        },
      });
      setActiveProvider("claude-agent");
      bustCatalogCache();
      history.addSystem(chalk.green("✓ Claude Pro/Max connected."));
    } catch (err) {
      history.addError(`Claude OAuth failed: ${(err as Error).message}`);
    }
    tui.requestRender();
    return "done";
  }
  return apiKeyLogin(deps, "claude-agent", promptOnce);
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
    case "xai":
      return loginXai(deps);
    case "github-copilot":
      return loginCopilot(deps);
    case "claude-agent":
      return loginClaudeAgent(deps);
    default:
      return apiKeyLogin(deps, p, deps.promptOnce);
  }
}

export async function startLogin(deps: LoginDeps, target?: string): Promise<void> {
  const { tui, history } = deps;

  // Validate explicit `target` once; if invalid, surface and exit.
  if (target) {
    if (!(PROVIDER_IDS as readonly string[]).includes(target)) {
      history.addSystem(`unknown provider: ${target}. options: ${PROVIDER_IDS.join(", ")}`);
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
  const { tui, history, selectOnce } = deps;
  let pick = target;
  if (!pick) {
    const authed = listAuthorizedProviders();
    if (authed.length === 0) {
      history.addSystem("no providers to sign out from");
      tui.requestRender();
      return;
    }
    const items: SelectItem[] = [
      ...authed.map((id) => ({ value: id, label: id, description: id === getActiveProvider() ? "(active)" : "" })),
      { value: "__all__", label: chalk.red("all providers"), description: "Sign out from every provider" },
    ];
    const sel = await selectOnce(items);
    if (!sel) return;
    pick = sel.value;
  }
  if (pick === "__all__") {
    logout();
    history.addSystem("signed out of all providers");
  } else {
    logout(pick as ProviderId);
    history.addSystem(`signed out of ${pick}`);
  }
  tui.requestRender();
}
