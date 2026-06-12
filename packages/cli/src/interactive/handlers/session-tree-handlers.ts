/**
 * /fork, /clone, /tree — session-tree navigation (pi-mono parity).
 */
import {
    BranchSummaryAbortedError,
    collectEntriesForBranchSummary,
    estimateContextTokens,
    runBranchSummary,
    settingsStore,
    stripSessionHookContext,
    type CommandContext,
} from "@notshekhar/pi-core";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";
import { UserMessageSelectorComponent } from "../ui/user-message-selector";
import { TreeSelectorComponent, type FilterMode } from "../ui/tree-selector";
import { extractText, rejectWhileBusy, replayCurrentBranch } from "./shared";

type SessionTreeHandlers = Pick<CommandContext, "forkFromMessage" | "cloneSession" | "showTree">;

export function createSessionTreeHandlers(state: AppState, deps: AppDeps): SessionTreeHandlers {
    const { tui, history, footer, tracker, editor, manager, refreshFooter, showWorking, hideWorking, showSelector } =
        deps;
    const { selectOnce, promptOnce } = deps;

    /**
     * pi-mono runtimeHost.fork: "before" forks at the parent of a user
     * message (its text returns to the editor); "at" clones up to and
     * including the entry (for /clone).
     */
    const applyFork = async (entryId: string, position: "before" | "at"): Promise<{ selectedText?: string }> => {
        const session = state.session!;
        const entry = session.getEntry(entryId);
        if (!entry) throw new Error("Invalid entry ID for forking");

        let targetLeafId: string | null;
        let selectedText: string | undefined;
        if (position === "at") {
            targetLeafId = entryId;
        } else {
            if (entry.type !== "message" || entry.role !== "user") throw new Error("Invalid entry ID for forking");
            targetLeafId = entry.parentId ?? null;
            // Hook context stays in the transcript, never in the editor.
            selectedText = stripSessionHookContext(extractText(entry.content));
        }

        const forked = targetLeafId
            ? manager.forkAtEntry(session, targetLeafId)
            : // Forking the very first message: brand-new empty session.
              await manager.create({ cwd: state.cwd, provider: state.provider, model: state.modelId });

        state.session = forked;
        footer.setSession(forked.id);
        state.latestContextTokens = tracker.seedFromSession(forked).ctxTokens;
        refreshFooter();
        return { selectedText };
    };

    const showForkSelector = (): void => {
        if (rejectWhileBusy(state, deps)) return;
        const session = state.session;
        const userMessages = session?.getUserMessagesForForking() ?? [];
        if (!session || userMessages.length === 0) {
            history.addSystem("No messages to fork from");
            tui.requestRender();
            return;
        }
        const initialSelectedId = userMessages[userMessages.length - 1]?.entryId;

        let done: () => void = () => {};
        const selector = new UserMessageSelectorComponent(
            userMessages.map((m) => ({ id: m.entryId, text: stripSessionHookContext(m.text) })),
            async (entryId) => {
                done();
                try {
                    const result = await applyFork(entryId, "before");
                    replayCurrentBranch(state, deps);
                    editor.setText(result.selectedText ?? "");
                    history.addSystem("Forked to new session");
                } catch (err) {
                    history.addError(err instanceof Error ? err.message : String(err));
                }
                tui.requestRender();
            },
            () => {
                done();
                tui.requestRender();
            },
            initialSelectedId,
        );
        done = showSelector(selector, selector.getMessageList() as never);
    };

    const cloneCurrentSession = async (): Promise<void> => {
        if (rejectWhileBusy(state, deps)) return;
        const session = state.session;
        const leafId = session?.getLeafId();
        if (!session || !leafId) {
            history.addSystem("Nothing to clone yet");
            tui.requestRender();
            return;
        }
        try {
            await applyFork(leafId, "at");
            replayCurrentBranch(state, deps);
            editor.setText("");
            history.addSystem("Cloned to new session");
        } catch (err) {
            history.addError(err instanceof Error ? err.message : String(err));
        }
        tui.requestRender();
    };

    /** Ask how to summarize the abandoned branch. null = user escaped back to the tree. */
    const promptSummaryChoice = async (): Promise<{ summarize: boolean; customInstructions?: string } | null> => {
        if (settingsStore.get("branchSummarySkipPrompt") as boolean) return { summarize: false };
        // Loop so Esc in the custom-prompt editor returns to this picker (pi-mono parity).
        while (true) {
            const choice = await selectOnce(
                [
                    { value: "none", label: "No summary", description: "" },
                    { value: "summarize", label: "Summarize", description: "" },
                    { value: "custom", label: "Summarize with custom prompt", description: "" },
                ],
                "Summarize branch?",
            );
            if (!choice) return null;
            if (choice.value === "custom") {
                const instr = await promptOnce("Custom summarization instructions");
                if (!instr.trim()) continue; // cancelled — back to the picker
                return { summarize: true, customInstructions: instr };
            }
            return { summarize: choice.value !== "none" };
        }
    };

    const showTreeSelector = (initialSelectedId?: string): void => {
        if (rejectWhileBusy(state, deps)) return;
        const session = state.session;
        const tree = session?.getTree() ?? [];
        if (!session || tree.length === 0) {
            history.addSystem("No entries in session");
            tui.requestRender();
            return;
        }
        const realLeafId = session.getLeafId();
        const initialFilterMode = (settingsStore.get("treeFilterMode") as FilterMode | undefined) ?? "default";

        const navigateTo = async (entryId: string): Promise<void> => {
            const choice = await promptSummaryChoice();
            if (!choice) {
                // Esc — re-show tree selector with same selection
                showTreeSelector(entryId);
                return;
            }

            const target = session.getEntry(entryId);
            if (!target) {
                history.addError(`Entry ${entryId} not found`);
                tui.requestRender();
                return;
            }
            // User message target: leaf = parent, text goes to editor
            let newLeafId: string | null;
            let editorText: string | undefined;
            if (target.type === "message" && target.role === "user") {
                newLeafId = target.parentId ?? null;
                editorText = stripSessionHookContext(extractText(target.content));
            } else {
                newLeafId = entryId;
            }

            try {
                let summarized = false;
                if (choice.summarize) {
                    const { entries } = collectEntriesForBranchSummary(session, realLeafId, entryId);
                    if (entries.length > 0) {
                        // busy + signal so the global Esc handler can abort
                        // the summarization like a normal turn.
                        state.busy = true;
                        showWorking("Summarizing branch");
                        const signal = state.abort.signal;
                        try {
                            const summary = await runBranchSummary({
                                entries,
                                modelId: state.modelId,
                                abortSignal: signal,
                                customInstructions: choice.customInstructions,
                            });
                            await session.branchWithSummary(newLeafId, summary);
                            summarized = true;
                        } finally {
                            state.busy = false;
                            hideWorking();
                        }
                    }
                }
                if (!summarized) {
                    if (newLeafId === null) session.resetLeaf();
                    else session.branch(newLeafId);
                }

                replayCurrentBranch(state, deps);
                if (editorText && !editor.getText().trim()) editor.setText(editorText);
                state.latestContextTokens = estimateContextTokens(session);
                refreshFooter();
                history.addSystem("Navigated to selected point");
            } catch (err) {
                if (err instanceof BranchSummaryAbortedError) {
                    history.addSystem("Branch summarization cancelled");
                    tui.requestRender();
                    showTreeSelector(entryId);
                    return;
                }
                history.addError(err instanceof Error ? err.message : String(err));
            }
            tui.requestRender();
        };

        let done: () => void = () => {};
        const selector = new TreeSelectorComponent(
            tree,
            realLeafId,
            process.stdout.rows ?? 24,
            async (entryId) => {
                // Selecting the current leaf is a no-op (already there)
                if (entryId === realLeafId) {
                    done();
                    history.addSystem("Already at this point");
                    tui.requestRender();
                    return;
                }
                done(); // Close selector first
                await navigateTo(entryId);
            },
            () => {
                done();
                tui.requestRender();
            },
            (entryId, label) => {
                void session.appendLabelChange(entryId, label).catch((err) => {
                    history.addError(err instanceof Error ? err.message : String(err));
                });
                tui.requestRender();
            },
            initialSelectedId,
            initialFilterMode,
        );
        done = showSelector(selector, selector as never);
    };

    return {
        forkFromMessage() {
            showForkSelector();
        },
        async cloneSession() {
            await cloneCurrentSession();
        },
        showTree() {
            showTreeSelector();
        },
    };
}
