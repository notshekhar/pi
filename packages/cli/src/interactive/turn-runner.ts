import { EventEmitter } from "node:events";
import { asTurnEmitter, type CommandContext, parseModelId, runTurn } from "@notshekhar/loop-core";
import { wrapSessionHookContext } from "@notshekhar/loop-core";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";
import { formatError } from "./format-error";
import { createSubagentStream } from "./subagent-stream";
import { wireTurnEmitter } from "./turn-emitter";
import { traceEvent } from "./debug-log";

/**
 * Whether the leading /token of an input maps to a registered slash command.
 * Mirrors how CommandRegistry.run parses the name so the two stay in sync.
 */
function commandExists(commands: { has(name: string): boolean }, input: string): boolean {
    const space = input.indexOf(" ");
    const name = (space < 0 ? input.slice(1) : input.slice(1, space)).trim();
    return commands.has(name);
}

export function createTurnRunner(state: AppState, deps: AppDeps, ctx: CommandContext) {
    const {
        tui,
        history,
        editor,
        commands,
        queuedMessages,
        refreshStatusLine,
        renderPending,
        showWorking,
        hideWorking,
        ensureSession,
        tracker,
    } = deps;

    // Pull the next queued input and resubmit it, whatever its type (chat or
    // command). Called after every item finishes so the FIFO queue keeps
    // draining — chat turns drain from their finally, commands/guards from
    // their return paths.
    const drainNext = (): void => {
        const next = queuedMessages.shift();
        if (next === undefined) return;
        traceEvent("drain", `"${next}" aborted=${state.abort.signal.aborted} remaining=${queuedMessages.length}`);
        renderPending();
        if (editor.onSubmit) void editor.onSubmit(next);
    };

    const onSubmit = async (raw: string) => {
        const text = raw.trim();
        if (!text) {
            drainNext();
            return;
        }

        // Agent busy → queue every input for after the current turn, FIFO.
        // Everything queues uniformly: chat messages AND slash commands
        // (including /new and /clear). They mutate session/model state and would
        // race the running turn, so they run in order when their turn comes up
        // rather than preempting. The queue drains after each item via
        // drainNext(), whatever its type.
        if (state.busy) {
            queuedMessages.push(text);
            traceEvent("queue", `"${text}" (depth=${queuedMessages.length})`);
            renderPending();
            tui.requestRender();
            return;
        }

        // Slash commands run inline. Handler errors land in chat — otherwise
        // they die as unhandled rejections nobody sees. An unrecognized /name
        // isn't an error: the user may just be talking about a path or option,
        // so we fall through and send it to the model as a normal message.
        if (text.startsWith("/") && commandExists(commands, text)) {
            history.addCommand(text);
            try {
                await commands.run(text, ctx);
            } catch (err) {
                history.addError(formatError(err));
            }
            tui.requestRender();
            drainNext();
            return;
        }

        // No model picked yet — a chat turn can't run. Guide instead of crashing
        // on parseModelId(""); /provider and /login stay reachable above.
        if (!state.modelId) {
            history.addSystem("No model selected. Run /provider to pick one, or /login to add a provider.");
            tui.requestRender();
            drainNext();
            return;
        }

        // First turn may race SessionStart hooks — wait so their injected
        // context (pendingInjection) isn't silently dropped.
        if (state.startupHooksDone) {
            try {
                await state.startupHooksDone;
            } catch (err) {
                history.addError(`startup hooks: ${formatError(err)}`);
            }
            state.startupHooksDone = null;
        }

        // SessionStart hook context must persist in the transcript (the model
        // needs it in history on every later turn), but the tag lets the TUI
        // collapse it instead of rendering it as if the user typed it.
        const finalInput = state.pendingInjection ? wrapSessionHookContext(state.pendingInjection, text) : text;
        state.pendingInjection = null;

        // One-shot agent (/<agent> <message>) applies to exactly this turn.
        const turnAgent = state.oneShotAgent ?? state.agent;
        state.oneShotAgent = null;

        const activeSession = await ensureSession();
        state.busy = true;
        history.addUser(finalInput);
        showWorking("Generating");
        tui.requestRender();

        const { provider: turnProvider } = parseModelId(state.modelId);
        history.ensureAssistant(turnProvider, state.modelId);
        const emitter = asTurnEmitter(new EventEmitter());
        const subagentStream = createSubagentStream(history, tui);
        wireTurnEmitter(emitter, {
            history,
            tui,
            state,
            turnProvider,
            subagentStream,
            showWorking,
            refreshStatusLine,
        });

        const turnSignal = state.abort.signal;
        traceEvent("turn", `start "${text}" abortedAtStart=${turnSignal.aborted} agent=${turnAgent}`);
        try {
            await runTurn({
                session: activeSession,
                modelId: state.modelId,
                userInput: finalInput,
                cwd: state.cwd,
                abortSignal: turnSignal,
                tracker,
                emitter,
                thinkingLevel: state.thinkingLevel,
                agent: turnAgent,
            });
        } catch (err) {
            history.addError(formatError(err));
        } finally {
            traceEvent("turn", `end   "${text}" abortedAtEnd=${turnSignal.aborted}`);
            state.busy = false;
            history.finishAssistant();
            hideWorking();
            tui.requestRender();
            // Drain the next queued input (FIFO), whatever its type. Each fresh
            // turn/command re-reads state.
            drainNext();
        }
    };

    return onSubmit;
}
