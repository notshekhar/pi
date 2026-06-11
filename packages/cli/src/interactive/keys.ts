import { matchesKey } from "@notshekhar/pi-tui";

export const CTRL_C = "\x03";
export const CTRL_D = "\x04";
export const ESC = "\x1b";

export const isCtrlC = (d: string) => d === CTRL_C || matchesKey(d, "ctrl+c");
export const isCtrlD = (d: string) => d === CTRL_D || matchesKey(d, "ctrl+d");
export const isCtrlL = (d: string) => d === "\x0c" || matchesKey(d, "ctrl+l");
export const isCtrlE = (d: string) => d === "\x05" || matchesKey(d, "ctrl+e");
export const isCtrlV = (d: string) => d === "\x16" || matchesKey(d, "ctrl+v");
// NOTE: \x09 is TAB which legacy terminals share with Ctrl+I — only match the
// Kitty-protocol Ctrl+I so autocomplete (Tab) keeps working.
export const isCtrlI = (d: string) => matchesKey(d, "ctrl+i");
export const isEsc = (d: string) => d === ESC || matchesKey(d, "escape");
export const isTab = (d: string) => d === "\t" || matchesKey(d, "tab");
