import type { CommandContext } from "@pi/core";
import type { AppDeps } from "./deps";
import type { AppState } from "./state";
import { isCtrlC, isCtrlD, isCtrlI, isCtrlL, isCtrlO, isCtrlV, isEsc } from "./keys";
import { pickImageFile, readClipboardImageToFile } from "./clipboard-image";

export type InputListener = (data: string) => { consume: boolean } | undefined;

export function createInputHandler(state: AppState, deps: AppDeps, ctx: CommandContext): InputListener {
  const { tui, history, queuedMessages, renderPending, hideWorking, cleanExit } = deps;

  return (data) => {
    if (isCtrlL(data)) {
      ctx.clearScreen();
      return { consume: true };
    }
    if (isCtrlO(data)) {
      const now = history.toggleToolsExpanded();
      history.addSystem(`tools ${now ? "expanded" : "collapsed"}`);
      tui.requestRender();
      return { consume: true };
    }
    if (isCtrlV(data)) {
      const path = readClipboardImageToFile();
      if (path) {
        void ctx.attachImage(path);
        return { consume: true };
      }
    }
    if (isCtrlI(data)) {
      const path = pickImageFile();
      if (path) void ctx.attachImage(path);
      return { consume: true };
    }
    if (isCtrlD(data) && !state.busy) {
      cleanExit(0);
      return { consume: true };
    }
    if (isCtrlC(data)) {
      if (state.busy) {
        state.abort.abort();
        state.abort = new AbortController();
        state.busy = false;
        hideWorking();
        queuedMessages.length = 0;
        renderPending();
        tui.requestRender();
        return { consume: true };
      }
      const now = Date.now();
      if (now - state.lastCtrlCAt < 1000) {
        cleanExit(130);
        return { consume: true };
      }
      state.lastCtrlCAt = now;
      history.addSystem("Press Ctrl+C again to quit.");
      tui.requestRender();
      return { consume: true };
    }
    if (isEsc(data) && state.busy) {
      state.abort.abort();
      state.abort = new AbortController();
      state.busy = false;
      hideWorking();
      tui.requestRender();
      return { consume: true };
    }
    return undefined;
  };
}
