/**
 * Composition root for the slash-command surface: each handler module owns
 * one concern (sessions, session tree, models, agents, hooks, settings,
 * misc) and contributes its slice of CommandContext.
 */
import type { CommandContext } from "@notshekhar/pi-core";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";
import { createAgentHandlers } from "./handlers/agent-handlers";
import { createHookHandlers } from "./handlers/hook-handlers";
import { createMiscHandlers } from "./handlers/misc-handlers";
import { createModelHandlers } from "./handlers/model-handlers";
import { createSessionHandlers } from "./handlers/session-handlers";
import { createSessionTreeHandlers } from "./handlers/session-tree-handlers";
import { createSettingsHandlers } from "./handlers/settings-handlers";
import { createTimerHandlers } from "./handlers/timer-handlers";

export function createCommandContext(state: AppState, deps: AppDeps): CommandContext {
    return {
        get cwd() {
            return state.cwd;
        },
        ...createMiscHandlers(state, deps),
        ...createModelHandlers(state, deps),
        ...createSessionHandlers(state, deps),
        ...createSessionTreeHandlers(state, deps),
        ...createAgentHandlers(state, deps),
        ...createHookHandlers(state, deps),
        ...createSettingsHandlers(state, deps),
        ...createTimerHandlers(state, deps),
    };
}
