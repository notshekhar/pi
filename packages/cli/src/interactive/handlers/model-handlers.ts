/**
 * Model & provider selection: /model, /provider, /thinking.
 */
import type { SelectItem } from "@notshekhar/pi-tui";
import chalk from "chalk";
import {
    addCustomModel,
    getActiveProvider,
    getCatalog,
    listAuthorizedProviders,
    listCustomModelIds,
    listCustomProviders,
    removeCustomModel,
    setActiveProvider,
    setProjectModel,
    settingsStore,
    THINKING_LEVEL_DESCRIPTIONS,
    THINKING_LEVELS,
    type CommandContext,
    type ProviderId,
    type ThinkingLevel,
} from "@notshekhar/pi-core";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";

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
            // auth'd providers + zero-login ollama (daemon detected via the catalog)
            // + saved custom providers (gateways like bifrost)
            const authed = listAuthorizedProviders();
            const detectedCat = await getCatalog();
            for (const m of Object.values(detectedCat)) {
                if (m.available && m.provider === "ollama" && !authed.includes(m.provider)) {
                    authed.push(m.provider);
                }
            }
            for (const c of listCustomProviders()) {
                const id = `custom:${c.name}` as ProviderId;
                if (!authed.includes(id)) authed.push(id);
            }
            if (authed.length === 0) {
                history.addSystem(chalk.yellow("no providers authenticated. /login first."));
                tui.requestRender();
                return;
            }
            let target = p;
            if (!target) {
                const items: SelectItem[] = authed.map((id) => ({
                    value: id,
                    label: id,
                    description: id === getActiveProvider() ? "(active)" : "",
                }));
                const pick = await selectOnce(items);
                if (!pick) return;
                target = pick.value;
            }
            if (!authed.includes(target as ProviderId)) {
                history.addSystem(chalk.red(`not authorized: ${target}. /login ${target} first.`));
                tui.requestRender();
                return;
            }
            setActiveProvider(target as ProviderId);
            const cat = await getCatalog();
            const first = Object.values(cat).find((m) => m.provider === target && m.available);
            if (first) {
                state.modelId = first.id;
                settingsStore.set("defaultModel", state.modelId);
                setProjectModel(state.cwd, state.modelId);
                footer.setModel(state.modelId);
            }
            history.addSystem(`provider → ${target}${first ? `, model → ${first.id}` : ""}`);
            tui.requestRender();
        },
        async openModelPicker() {
            const active = (getActiveProvider() ?? state.provider) as ProviderId;

            const ADD = "\x00add";
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
                const pick = await searchOnce(items, `Model · ${active} (type to filter)`);
                if (!pick) return;

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
                                description: "delete from ~/.pi/models.json",
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
