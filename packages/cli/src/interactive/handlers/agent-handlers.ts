/**
 * Agent management: /agents and one-shot /<agent> <message>.
 */
import type { SelectItem } from "@notshekhar/pi-tui";
import chalk from "chalk";
import {
    AGENT_TOOL_NAMES,
    DEFAULT_AGENT_NAME,
    DEFAULT_BASE_PROMPT,
    agentExists,
    deleteAgent,
    getAgentPrompt,
    getAgentTools,
    hasBuiltinOverride,
    isHiddenAgent,
    isValidAgentName,
    listAgents,
    registerAgentCommand,
    saveAgent,
    settingsStore,
    type CommandContext,
} from "@notshekhar/pi-core";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";

type AgentHandlers = Pick<CommandContext, "useAgent" | "manageAgents">;

export function createAgentHandlers(state: AppState, deps: AppDeps): AgentHandlers {
    const { tui, history, footer, editor, commands, selectOnce, toggleOnce, promptOnce, refreshCommands } = deps;

    return {
        useAgent(name, message) {
            if (!agentExists(name)) {
                history.addSystem(chalk.red(`unknown agent: ${name} — /agents to create one`));
                tui.requestRender();
                return;
            }
            // /<agent> <message> = one-shot: that message runs under this agent's
            // prompt; the session's selected agent is untouched. Switching the
            // session agent happens only via /agents → use.
            if (message?.trim()) {
                state.oneShotAgent = name;
                history.addSystem(chalk.dim(`agent for this message: ${name}`));
                tui.requestRender();
                if (editor.onSubmit) void editor.onSubmit(message);
                return;
            }
            history.addSystem(`usage: /${name} <message> — one message with this agent. Session switch: /agents`);
            tui.requestRender();
        },
        async manageAgents() {
            const toolsLabel = (tools: string[] | undefined) => (tools?.length ? tools.join(", ") : "all tools");
            // Toggle multi-select (cursor stays put, Enter/Space toggles).
            // Returns undefined = all tools, null = cancelled.
            const pickFrom = async (
                all: string[],
                initial: string[] | undefined,
                title: string,
            ): Promise<string[] | undefined | null> => {
                const picked = await toggleOnce(all, new Set(initial?.length ? initial : all), title);
                if (picked === null) return null;
                return picked.length === all.length ? undefined : picked;
            };
            // Agent tools include "task" (subagents). No separate subagent
            // config: a subagent forks the spawning turn's agent and is always
            // capped to its tools, so delegation can never widen access.
            const pickTools = (initial: string[] | undefined) =>
                pickFrom([...AGENT_TOOL_NAMES], initial, "Agent tools (task = can spawn subagents)");

            // Loop so Esc in submenus returns to the agent list, like /settings.
            while (true) {
                const agents = listAgents();
                const items: SelectItem[] = [
                    {
                        value: "+new",
                        label: "+ new agent",
                        description: "create an agent with its own tools and system prompt",
                    },
                    ...agents.map((a) => ({
                        value: a.name,
                        label:
                            a.name + (a.name === state.agent ? "  (active)" : "") + (a.builtin ? "  [built-in]" : ""),
                        description: `[${toolsLabel(a.tools)}] ${a.prompt.split("\n")[0].slice(0, 60)}`,
                    })),
                ];
                const pick = await selectOnce(items, "Agents (Esc to close)");
                if (!pick) return;

                if (pick.value === "+new") {
                    const name = (await promptOnce("agent name (e.g. reviewer)")).trim();
                    if (!name) continue;
                    if (!isValidAgentName(name)) {
                        history.addSystem(chalk.red(`invalid name: ${name} (alphanumeric, dashes, ≤32 chars)`));
                        tui.requestRender();
                        continue;
                    }
                    if (agentExists(name) || commands.has(name)) {
                        history.addSystem(chalk.red(`"${name}" already exists (agent or command)`));
                        tui.requestRender();
                        continue;
                    }
                    // Tools first, then the prompt — the prompt can reference what's allowed.
                    const tools = await pickTools(undefined);
                    if (tools === null) continue;
                    const prompt = await promptOnce(
                        `system prompt for "${name}" [${toolsLabel(tools)}]`,
                        DEFAULT_BASE_PROMPT,
                    );
                    if (!prompt.trim()) continue;
                    saveAgent(name, prompt, tools);
                    registerAgentCommand(commands, name);
                    refreshCommands();
                    history.addSystem(
                        `agent "${name}" created [${toolsLabel(tools)}] — /${name} <message> for one message, /agents → use for the session`,
                    );
                    tui.requestRender();
                    continue;
                }

                const name = pick.value;
                const info = agents.find((a) => a.name === name);
                const isBuiltin = info?.builtin ?? false;
                const currentTools = getAgentTools(name);
                const actions: SelectItem[] = [
                    { value: "use", label: "use", description: `switch active agent to "${name}"` },
                    { value: "edit", label: "edit prompt", description: "edit this agent's system prompt" },
                ];
                if (isBuiltin) {
                    // Built-in tool sets are fixed — preview only, no edit.
                    actions.push({
                        value: "tools-view",
                        label: "tools (fixed)",
                        description: toolsLabel(currentTools),
                    });
                    if (hasBuiltinOverride(name)) {
                        actions.push({
                            value: "delete",
                            label: "reset to built-in",
                            description: "remove the prompt override",
                        });
                    }
                } else {
                    actions.push({
                        value: "tools",
                        label: "edit tools",
                        description: `current: ${toolsLabel(currentTools)}`,
                    });
                    actions.push({ value: "delete", label: "delete", description: "remove agent and its /command" });
                }
                const action = await selectOnce(actions, `Agent: ${name} [${toolsLabel(currentTools)}]`);
                if (!action) continue;

                if (action.value === "use") {
                    state.agent = name;
                    settingsStore.set("agent", name);
                    footer.setAgent(name);
                    // Custom agents — and hidden built-ins like data-analyst —
                    // join the Tab cycle once explicitly selected.
                    if (!isBuiltin || isHiddenAgent(name)) state.cycleCustomAgent = name;
                    history.addSystem(`agent → ${name}`);
                    tui.requestRender();
                    return;
                }
                if (action.value === "tools-view") {
                    history.addSystem(`agent "${name}" tools (fixed): ${toolsLabel(currentTools)}`);
                    tui.requestRender();
                    continue;
                }
                if (action.value === "tools") {
                    const tools = await pickTools(currentTools);
                    if (tools === null) continue;
                    saveAgent(name, getAgentPrompt(name) ?? DEFAULT_BASE_PROMPT, tools);
                    history.addSystem(`agent "${name}" tools → ${toolsLabel(tools)}`);
                    tui.requestRender();
                    continue;
                }
                if (action.value === "edit") {
                    const current = getAgentPrompt(name) ?? DEFAULT_BASE_PROMPT;
                    // Built-ins: tools are fixed but still previewed before editing.
                    if (isBuiltin) {
                        history.addSystem(chalk.dim(`tools (fixed): ${toolsLabel(currentTools)}`));
                        tui.requestRender();
                    }
                    const edited = await promptOnce(
                        `system prompt for "${name}" [${toolsLabel(currentTools)}]`,
                        current,
                    );
                    if (!edited.trim() || edited.trim() === current.trim()) continue;
                    saveAgent(name, edited, currentTools);
                    history.addSystem(`agent "${name}" prompt updated`);
                    tui.requestRender();
                    continue;
                }
                if (action.value === "delete") {
                    deleteAgent(name);
                    // Resetting a hidden built-in's prompt override leaves the
                    // agent itself intact, so keep it revealed in the cycle.
                    if (state.cycleCustomAgent === name && !isHiddenAgent(name)) state.cycleCustomAgent = null;
                    if (state.agent === name && !isBuiltin) {
                        state.agent = DEFAULT_AGENT_NAME;
                        settingsStore.set("agent", DEFAULT_AGENT_NAME);
                        footer.setAgent(DEFAULT_AGENT_NAME);
                    }
                    if (!isBuiltin) commands.unregister(name);
                    refreshCommands();
                    history.addSystem(isBuiltin ? `"${name}" prompt reset to built-in` : `agent "${name}" deleted`);
                    tui.requestRender();
                    continue;
                }
            }
        },
    };
}
