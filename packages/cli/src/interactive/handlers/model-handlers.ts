/**
 * Model & provider selection: /model, /provider, /thinking.
 */
import type { SelectItem } from "@notshekhar/loop-tui";
import chalk from "chalk";
import {
    addCustomModel,
    getActiveProvider,
    getCatalog,
    getModelSync,
    getProjectProviderModel,
    listCustomModelIds,
    removeCustomModel,
    setActiveProvider,
    setProjectModel,
    settingsStore,
    THINKING_LEVEL_DESCRIPTIONS,
    THINKING_LEVELS,
    type CommandContext,
    type ProviderId,
    type ThinkingLevel,
} from "@notshekhar/loop-core";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";
import { listUsableProviders } from "../provider-availability";

type ModelHandlers = Pick<CommandContext, "setModel" | "setProvider" | "openModelPicker" | "setThinking">;

export function createModelHandlers(state: AppState, deps: AppDeps): ModelHandlers {
    const { tui, history, footer, refreshFooter, selectOnce, searchOnce, promptOnce, resolveModelId } = deps;

    const applyModel = (id: string) => {
        state.modelId = id;
        settingsStore.set("defaultModel", id);
        setProjectModel(state.cwd, id);
        footer.setModel(id);
        history.addSystem(`model → ${id}`);
        tui.requestRender();
    };

    return {
        async setModel(id) {
            const resolved = await resolveModelId(id);
            if (!resolved) {
                history.addSystem(chalk.red(`unknown model: ${id} — try /model to pick from a list`));
                tui.requestRender();
                return;
            }
            applyModel(resolved);
            refreshFooter();
        },
        async setProvider(p) {
            // logged-in providers + zero-login ollama + saved custom gateways
            const usable = await listUsableProviders();
            if (usable.length === 0) {
                history.addSystem(chalk.yellow("no providers available. /login first."));
                tui.requestRender();
                return;
            }
            let target = p;
            if (!target) {
                const items: SelectItem[] = usable.map((id) => ({
                    value: id,
                    label: id,
                    description: id === getActiveProvider() ? "(active)" : "",
                }));
                const pick = await selectOnce(items);
                if (!pick) return;
                target = pick.value;
            }
            if (!usable.includes(target as ProviderId)) {
                history.addSystem(chalk.red(`not authorized: ${target}. /login ${target} first.`));
                tui.requestRender();
                return;
            }
            setActiveProvider(target as ProviderId);
            const cat = await getCatalog();
            // This folder's last model for the provider wins; first available is the fallback.
            const rememberedId = getProjectProviderModel(state.cwd, target);
            const remembered = rememberedId ? cat[rememberedId] : undefined;
            const pick = remembered?.available
                ? remembered
                : Object.values(cat).find((m) => m.provider === target && m.available);
            if (pick) {
                state.modelId = pick.id;
                settingsStore.set("defaultModel", state.modelId);
                setProjectModel(state.cwd, state.modelId);
                footer.setModel(state.modelId);
            }
            history.addSystem(`provider → ${target}${pick ? `, model → ${pick.id}` : ""}`);
            tui.requestRender();
        },
        async openModelPicker() {
            const active = (getActiveProvider() ?? state.provider) as ProviderId;

            const ADD = "\x00add";
            let lastIndex = 0;
            while (true) {
                const cat = await getCatalog();
                const custom = new Set(listCustomModelIds());
                const modelItems: SelectItem[] = Object.values(cat)
                    .filter((m) => m.provider === active && m.available)
                    .sort((a, b) => a.id.localeCompare(b.id))
                    .map((m) => ({
                        value: m.id,
                        label: m.id.slice(active.length + 1) + (custom.has(m.id) ? "  (custom)" : ""),
                        description: `${m.name}  ·  ctx ${m.contextWindow.toLocaleString()}  ·  $${m.cost.input}/$${m.cost.output}`,
                    }));
                const items: SelectItem[] = [
                    { value: ADD, label: "+ add model…", description: `register a model id under ${active}` },
                    ...modelItems,
                ];
                const pick = await searchOnce(items, `Model · ${active} (type to filter)`, {
                    initialIndex: lastIndex,
                });
                if (!pick) return;
                lastIndex = Math.max(0, items.findIndex((i) => i.value === pick.value));

                if (pick.value === ADD) {
                    const modelId = (await promptOnce(`new model id under ${active}/ (e.g. some-model-v2)`)).trim();
                    if (!modelId) continue;
                    const full = addCustomModel({ provider: active, modelId });
                    history.addSystem(
                        `added ${full} (custom). It'll error at chat time if ${active} doesn't serve it.`,
                    );
                    applyModel(full);
                    return;
                }
                // A custom model offers remove via a follow-up action menu.
                if (custom.has(pick.value)) {
                    const action = await selectOnce(
                        [
                            { value: "use", label: "use", description: pick.value },
                            {
                                value: "remove",
                                label: "remove custom model",
                                description: "delete from ~/.loop/models.json",
                            },
                        ],
                        pick.value,
                    );
                    if (!action) continue;
                    if (action.value === "remove") {
                        removeCustomModel(pick.value);
                        history.addSystem(`removed custom model ${pick.value}`);
                        tui.requestRender();
                        continue;
                    }
                }
                applyModel(pick.value);
                return;
            }
        },
        async setThinking(level) {
            // Models that don't reason (e.g. composer-2.5, grok-3) have no
            // thinking levels — the reference shows "does not support thinking" and
            // skips the selector entirely.
            if (!getModelSync(state.modelId)?.reasoning) {
                history.addSystem("current model does not support thinking");
                tui.requestRender();
                return;
            }
            let target = level as ThinkingLevel | undefined;
            if (!target) {
                const items: SelectItem[] = THINKING_LEVELS.map((lv) => ({
                    value: lv,
                    label: lv,
                    description: THINKING_LEVEL_DESCRIPTIONS[lv] + (lv === state.thinkingLevel ? "  (current)" : ""),
                }));
                const pick = await selectOnce(items, "Thinking level");
                if (!pick) return;
                target = pick.value as ThinkingLevel;
            }
            if (!(THINKING_LEVELS as readonly string[]).includes(target)) {
                history.addSystem(
                    chalk.red(`unknown thinking level: ${target}. options: ${THINKING_LEVELS.join(", ")}`),
                );
                tui.requestRender();
                return;
            }
            state.thinkingLevel = target;
            settingsStore.set("thinkingLevel", target);
            footer.setThinking(target);
            history.addSystem(`thinking → ${target}`);
            tui.requestRender();
        },
    };
}
