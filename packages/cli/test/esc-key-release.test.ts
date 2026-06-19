import { describe, expect, test } from "bun:test";
import { setKittyProtocolActive } from "@notshekhar/loop-tui";
import { createInputHandler } from "../src/interactive/input-handler";

// Under the Kitty keyboard protocol one physical Esc press emits a press event
// AND a release event, and both match isEsc(). The input handler must act only
// on the press — otherwise a single Esc fires the interrupt twice, and the
// second firing lands on the next (drained) queued turn and kills it.
const ESC_PRESS = "\x1b[27u";
const ESC_RELEASE = "\x1b[27;1:3u";

function makeHarness() {
    const state: any = {
        cwd: "/tmp",
        modelId: "xai/grok-build-0.1",
        busy: true,
        abort: new AbortController(),
        lastCtrlCAt: 0,
        agent: "default",
    };
    const editor: any = { focused: true, isShowingAutocomplete: () => false };
    const deps: any = {
        tui: { requestRender: () => {} },
        history: { addSystem: () => {} },
        queuedMessages: [],
        renderPending: () => {},
        hideWorking: () => {},
        cleanExit: () => {},
        editor,
        footer: { setAgent: () => {} },
    };
    const handler = createInputHandler(state, deps, {} as any);
    return { handler, state };
}

describe("Esc interrupt fires on press, not on key-release", () => {
    test("press aborts the turn; release is ignored", () => {
        setKittyProtocolActive(true);
        const { handler, state } = makeHarness();
        const firstSignal = state.abort.signal;

        // Press: interrupts the running turn.
        const pressResult = handler(ESC_PRESS);
        expect(pressResult).toEqual({ consume: true });
        expect(firstSignal.aborted).toBe(true);
        expect(state.busy).toBe(false);

        // The handler swapped in a fresh controller for the next turn.
        const secondSignal = state.abort.signal;
        expect(secondSignal.aborted).toBe(false);

        // Release of the SAME physical Esc must NOT abort again — this is the
        // event that used to kill the drained turn.
        const releaseResult = handler(ESC_RELEASE);
        expect(releaseResult).toBeUndefined();
        expect(secondSignal.aborted).toBe(false);
    });

    test("a release event is ignored even while busy (no phantom interrupt)", () => {
        setKittyProtocolActive(true);
        const { handler, state } = makeHarness();
        const signal = state.abort.signal;
        // Simulate the drained turn now running: busy again on the fresh signal.
        state.busy = true;
        const res = handler(ESC_RELEASE);
        expect(res).toBeUndefined();
        expect(signal.aborted).toBe(false);
        expect(state.busy).toBe(true);
    });
});
