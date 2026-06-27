import { describe, expect, test, spyOn } from "bun:test";
import * as fs from "node:fs";
import { ProcessTerminal } from "../src/terminal";

// pop kitty keyboard protocol · disable modifyOtherKeys · disable bracketed
// paste · show cursor. Left enabled, these make the shell echo raw escapes
// (\x1b[27;5;13~) on the next keypress after an uncaught-signal exit.
const RESET = "\x1b[<u\x1b[>4;0m\x1b[?2004l\x1b[?25h";

function withTTY<T>(isTTY: boolean, fn: () => T): T {
    const original = process.stdout.isTTY;
    Object.defineProperty(process.stdout, "isTTY", { value: isTTY, configurable: true });
    try {
        return fn();
    } finally {
        Object.defineProperty(process.stdout, "isTTY", { value: original, configurable: true });
    }
}

describe("terminal exit safety net", () => {
    test("writes the reset sequence to fd 1 when stdout is a TTY", () => {
        const term = new ProcessTerminal() as unknown as { resetKeyboardModesSync(): void };
        const writeSync = spyOn(fs, "writeSync").mockImplementation(() => 0);
        try {
            withTTY(true, () => term.resetKeyboardModesSync());
            expect(writeSync).toHaveBeenCalledWith(1, RESET);
        } finally {
            writeSync.mockRestore();
        }
    });

    test("is a no-op when stdout is not a TTY (don't pollute a redirected stream)", () => {
        const term = new ProcessTerminal() as unknown as { resetKeyboardModesSync(): void };
        const writeSync = spyOn(fs, "writeSync").mockImplementation(() => 0);
        try {
            withTTY(false, () => term.resetKeyboardModesSync());
            expect(writeSync).not.toHaveBeenCalled();
        } finally {
            writeSync.mockRestore();
        }
    });

    test("a fatal signal resets the terminal and exits 128+signo (no re-raise)", () => {
        const term = new ProcessTerminal() as unknown as { onFatalSignal(signal: NodeJS.Signals): void };
        const writeSync = spyOn(fs, "writeSync").mockImplementation(() => 0);
        const exit = spyOn(process, "exit").mockImplementation((() => undefined) as never);
        try {
            withTTY(true, () => term.onFatalSignal("SIGINT"));
            expect(writeSync).toHaveBeenCalledWith(1, RESET);
            expect(exit).toHaveBeenCalledWith(130); // 128 + 2
        } finally {
            writeSync.mockRestore();
            exit.mockRestore();
        }
    });

    test("install registers signal + exit listeners; remove cleans them up", () => {
        const term = new ProcessTerminal() as unknown as {
            installExitSafetyNet(): void;
            removeExitSafetyNet(): void;
        };
        const base = process.listenerCount("SIGINT");
        term.installExitSafetyNet();
        expect(process.listenerCount("SIGINT")).toBe(base + 1);
        expect(process.listenerCount("SIGTERM")).toBeGreaterThan(0);
        expect(process.listenerCount("SIGHUP")).toBeGreaterThan(0);
        // idempotent — no duplicate registration
        term.installExitSafetyNet();
        expect(process.listenerCount("SIGINT")).toBe(base + 1);
        term.removeExitSafetyNet();
        expect(process.listenerCount("SIGINT")).toBe(base);
    });
});
