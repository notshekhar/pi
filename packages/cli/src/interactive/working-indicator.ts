import chalk from "chalk";
import { Loader, type Component, type Container, type TUI } from "@notshekhar/loop-tui";

export interface WorkingIndicator {
    /** Show/update the spinner in the status slot (Esc-to-interrupt hint added). */
    showWorking(message?: string): void;
    /** Stop the spinner and restore the idle spacer so the editor never shifts. */
    hideWorking(): void;
}

/**
 * Drives the fixed-height status slot above the editor: a Loader while a turn
 * (or hook) is working, the idle spacer otherwise. The slot keeps a constant
 * height so the editor/status line block never jumps a row.
 */
export function createWorkingIndicator(
    tui: TUI,
    statusContainer: Container,
    statusIdleSpacer: Component,
): WorkingIndicator {
    let workingLoader: Loader | null = null;

    function showWorking(message = "Generating…"): void {
        const fullMsg = `${message} ${chalk.dim("(Esc to interrupt)")}`;
        if (workingLoader) {
            workingLoader.setMessage(fullMsg);
            return;
        }
        workingLoader = new Loader(
            tui,
            (s) => chalk.cyan(s),
            (s) => chalk.dim(s),
            fullMsg,
        );
        statusContainer.clear();
        statusContainer.addChild(workingLoader);
        workingLoader.start();
        tui.requestRender();
    }

    function hideWorking(): void {
        if (!workingLoader) return;
        workingLoader.stop();
        statusContainer.clear();
        statusContainer.addChild(statusIdleSpacer);
        workingLoader = null;
        tui.requestRender();
    }

    return { showWorking, hideWorking };
}
