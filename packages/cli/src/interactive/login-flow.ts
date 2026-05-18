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

export interface LoginDeps {
  tui: TUI;
  history: ChatHistory;
  selectOnce: (items: SelectItem[], title?: string) => Promise<SelectItem | null>;
  promptOnce: (label?: string) => Promise<string>;
}

export async function startLogin(deps: LoginDeps, target?: string): Promise<void> {
  const { tui, history, selectOnce, promptOnce } = deps;
  let p: ProviderId | null = null;
  if (target) {
    if (!(PROVIDER_IDS as readonly string[]).includes(target)) {
      history.addSystem(`unknown provider: ${target}. options: ${PROVIDER_IDS.join(", ")}`);
      tui.requestRender();
      return;
    }
    p = target as ProviderId;
  } else {
    const items: SelectItem[] = PROVIDER_IDS.map((id) => ({
      value: id,
      label: id,
      description: providerLabel(id),
    }));
    const pick = await selectOnce(items, "Sign in to provider");
    if (!pick) return;
    p = pick.value as ProviderId;
  }

  if (p === "xai") {
    const mode = await selectOnce([
      { value: "oauth", label: "Sign in with xAI (SuperGrok subscription)", description: "OAuth, opens browser" },
      { value: "apikey", label: "Use API key", description: "Paste your XAI_API_KEY" },
    ]);
    if (!mode) return;
    if (mode.value === "oauth") {
      history.addSystem("xAI login: launching OAuth in your browser…");
      tui.requestRender();
      try {
        await loginXaiOAuth(({ url, instructions }) => {
          history.addSystem(instructions);
          history.addSystem(chalk.cyan(url));
          tui.requestRender();
        });
        setActiveProvider("xai");
        history.addSystem(chalk.green("✓ xAI subscription connected."));
      } catch (err) {
        history.addError(`xAI login failed: ${(err as Error).message}`);
      }
      tui.requestRender();
      return;
    }
  }

  if (p === "github-copilot") {
    history.addSystem("GitHub Copilot: starting device flow…");
    tui.requestRender();
    try {
      await loginOAuth("github-copilot", {
        onAuth: ({ url, instructions }) => {
          if (instructions) history.addSystem(instructions);
          history.addSystem(chalk.cyan(url));
          tui.requestRender();
        },
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
    return;
  }

  if (p === "claude-agent") {
    const mode = await selectOnce([
      { value: "apikey", label: "API key", description: "Paste your ANTHROPIC_API_KEY" },
      { value: "oauth", label: "Claude Pro/Max OAuth", description: "Subscription bearer (no API spend)" },
    ]);
    if (!mode) return;
    if (mode.value === "oauth") {
      history.addSystem("Anthropic OAuth: opening browser…");
      tui.requestRender();
      try {
        await loginOAuth("claude-agent", {
          onAuth: ({ url, instructions }) => {
            if (instructions) history.addSystem(instructions);
            history.addSystem(chalk.cyan(url));
            tui.requestRender();
          },
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
      return;
    }
  }

  history.addSystem(`${p}: paste API key, then press Enter.`);
  tui.requestRender();
  const key = await promptOnce(`${p.toUpperCase().replace(/-/g, "_")}_API_KEY: `);
  if (key) {
    loginApiKey(p, key);
    setActiveProvider(p);
    bustCatalogCache();
    history.addSystem(chalk.green(`✓ ${p} key saved.`));
    tui.requestRender();
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
