import { matchesKey } from "@notshekhar/loop-tui";

export const CTRL_C = "\x03";
export const CTRL_D = "\x04";
export const ESC = "\x1b";

export const isCtrlC = (d: string) => d === CTRL_C || matchesKey(d, "ctrl+c");
export const isCtrlD = (d: string) => d === CTRL_D || matchesKey(d, "ctrl+d");
export const isCtrlL = (d: string) => d === "\x0c" || matchesKey(d, "ctrl+l");
export const isCtrlE = (d: string) => d === "\x05" || matchesKey(d, "ctrl+e");
export const isCtrlV = (d: string) => d === "\x16" || matchesKey(d, "ctrl+v");
export const isCtrlG = (d: string) => d === "\x07" || matchesKey(d, "ctrl+g");
// NOTE: \x09 is TAB which legacy terminals share with Ctrl+I (i & 0x1f === 9),
// and matchesKey's legacy ctrl+letter branch treats them as the same byte.
// Exclude raw "\t" so only the Kitty-protocol Ctrl+I (a distinct CSI-u
// sequence) matches — otherwise plain Tab would fire the image picker instead
// of falling through to autocomplete.
export const isCtrlI = (d: string) => d !== "\t" && matchesKey(d, "ctrl+i");
export const isEsc = (d: string) => d === ESC || matchesKey(d, "escape");
export const isTab = (d: string) => d === "\t" || matchesKey(d, "tab");
export const isShiftTab = (d: string) => d === "\x1b[Z" || matchesKey(d, "shift+tab");
