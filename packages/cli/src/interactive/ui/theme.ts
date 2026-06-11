/**
 * Own theme engine — replaces @earendil-works/pi-coding-agent's theme module.
 * Ported from pi-mono theme.ts, simplified: built-in palettes are embedded TS
 * data (no JSON assets shipped next to the binary, no typebox validation, no
 * file watcher). Custom themes still load from ~/.pi/agent/themes/<name>.json
 * using pi-mono's theme JSON shape (vars + colors).
 */
import * as fs from "node:fs";
import * as path from "node:path";
import type { MarkdownTheme, SelectListTheme } from "@notshekhar/pi-tui";
import chalk from "chalk";
import { DARK_THEME, LIGHT_THEME, type ThemeColors, type ThemeJson } from "./themes";

export type ThemeColor = keyof ThemeColors & string;
export type ThemeBg =
  | "selectedBg"
  | "userMessageBg"
  | "customMessageBg"
  | "toolPendingBg"
  | "toolSuccessBg"
  | "toolErrorBg";

type ColorMode = "truecolor" | "256color";
type ColorValue = string | number;

// ---------------------------------------------------------------------------
// Color conversion (ported from pi-mono)
// ---------------------------------------------------------------------------

function hexToRgb(hex: string): { r: number; g: number; b: number } {
  const cleaned = hex.replace("#", "");
  if (cleaned.length !== 6) throw new Error(`Invalid hex color: ${hex}`);
  const r = parseInt(cleaned.substring(0, 2), 16);
  const g = parseInt(cleaned.substring(2, 4), 16);
  const b = parseInt(cleaned.substring(4, 6), 16);
  if (Number.isNaN(r) || Number.isNaN(g) || Number.isNaN(b)) {
    throw new Error(`Invalid hex color: ${hex}`);
  }
  return { r, g, b };
}

const CUBE_VALUES = [0, 95, 135, 175, 215, 255];
const GRAY_VALUES = Array.from({ length: 24 }, (_, i) => 8 + i * 10);

function findClosest(values: number[], target: number): number {
  let minDist = Infinity;
  let minIdx = 0;
  for (let i = 0; i < values.length; i++) {
    const dist = Math.abs(target - values[i]);
    if (dist < minDist) {
      minDist = dist;
      minIdx = i;
    }
  }
  return minIdx;
}

function colorDistance(r1: number, g1: number, b1: number, r2: number, g2: number, b2: number): number {
  const dr = r1 - r2;
  const dg = g1 - g2;
  const db = b1 - b2;
  return dr * dr * 0.299 + dg * dg * 0.587 + db * db * 0.114;
}

function rgbTo256(r: number, g: number, b: number): number {
  const rIdx = findClosest(CUBE_VALUES, r);
  const gIdx = findClosest(CUBE_VALUES, g);
  const bIdx = findClosest(CUBE_VALUES, b);
  const cubeIndex = 16 + 36 * rIdx + 6 * gIdx + bIdx;
  const cubeDist = colorDistance(r, g, b, CUBE_VALUES[rIdx], CUBE_VALUES[gIdx], CUBE_VALUES[bIdx]);

  const gray = Math.round(0.299 * r + 0.587 * g + 0.114 * b);
  const grayIdx = findClosest(GRAY_VALUES, gray);
  const grayDist = colorDistance(r, g, b, GRAY_VALUES[grayIdx], GRAY_VALUES[grayIdx], GRAY_VALUES[grayIdx]);

  const spread = Math.max(r, g, b) - Math.min(r, g, b);
  if (spread < 10 && grayDist < cubeDist) return 232 + grayIdx;
  return cubeIndex;
}

function fgAnsi(color: ColorValue, mode: ColorMode): string {
  if (color === "") return "\x1b[39m";
  if (typeof color === "number") return `\x1b[38;5;${color}m`;
  const { r, g, b } = hexToRgb(color);
  if (mode === "truecolor") return `\x1b[38;2;${r};${g};${b}m`;
  return `\x1b[38;5;${rgbTo256(r, g, b)}m`;
}

function bgAnsi(color: ColorValue, mode: ColorMode): string {
  if (color === "") return "\x1b[49m";
  if (typeof color === "number") return `\x1b[48;5;${color}m`;
  const { r, g, b } = hexToRgb(color);
  if (mode === "truecolor") return `\x1b[48;2;${r};${g};${b}m`;
  return `\x1b[48;5;${rgbTo256(r, g, b)}m`;
}

function resolveVarRefs(value: ColorValue, vars: Record<string, ColorValue>, visited = new Set<string>()): ColorValue {
  if (typeof value === "number" || value === "" || value.startsWith("#")) return value;
  if (visited.has(value)) throw new Error(`Circular variable reference: ${value}`);
  if (!(value in vars)) throw new Error(`Variable reference not found: ${value}`);
  visited.add(value);
  return resolveVarRefs(vars[value], vars, visited);
}

function detectColorMode(): ColorMode {
  const ct = process.env.COLORTERM ?? "";
  return /truecolor|24bit/i.test(ct) ? "truecolor" : "256color";
}

// ---------------------------------------------------------------------------
// Theme class
// ---------------------------------------------------------------------------

const BG_KEYS: ReadonlySet<string> = new Set<ThemeBg>([
  "selectedBg",
  "userMessageBg",
  "customMessageBg",
  "toolPendingBg",
  "toolSuccessBg",
  "toolErrorBg",
]);

export class Theme {
  readonly name: string;
  private fgColors = new Map<string, string>();
  private bgColors = new Map<string, string>();

  constructor(json: ThemeJson, mode: ColorMode = detectColorMode()) {
    this.name = json.name;
    const vars = json.vars ?? {};
    for (const [key, raw] of Object.entries(json.colors)) {
      const value = resolveVarRefs(raw, vars);
      if (BG_KEYS.has(key)) this.bgColors.set(key, bgAnsi(value, mode));
      else this.fgColors.set(key, fgAnsi(value, mode));
    }
  }

  fg(color: ThemeColor, text: string): string {
    const ansi = this.fgColors.get(color);
    if (!ansi) throw new Error(`Unknown theme color: ${color}`);
    return `${ansi}${text}\x1b[39m`;
  }

  bg(color: ThemeBg, text: string): string {
    const ansi = this.bgColors.get(color);
    if (!ansi) throw new Error(`Unknown theme background color: ${color}`);
    return `${ansi}${text}\x1b[49m`;
  }

  bold(text: string): string {
    return chalk.bold(text);
  }
  italic(text: string): string {
    return chalk.italic(text);
  }
  underline(text: string): string {
    return chalk.underline(text);
  }
}

// ---------------------------------------------------------------------------
// Global theme instance
// ---------------------------------------------------------------------------

let activeTheme: Theme | null = null;

/** Proxy so `theme.fg(...)` always resolves against the active theme. */
export const theme: Theme = new Proxy({} as Theme, {
  get(_target, prop) {
    if (!activeTheme) throw new Error("Theme not initialized. Call initTheme() first.");
    return (activeTheme as unknown as Record<string | symbol, unknown>)[prop];
  },
});

function customThemesDir(): string {
  const home = process.env.HOME ?? process.env.USERPROFILE ?? "";
  return path.join(home, ".pi", "agent", "themes");
}

function loadThemeJson(name: string): ThemeJson {
  if (name === "dark") return DARK_THEME;
  if (name === "light") return LIGHT_THEME;
  const file = path.join(customThemesDir(), `${name}.json`);
  return JSON.parse(fs.readFileSync(file, "utf8")) as ThemeJson;
}

export function initTheme(themeName = "dark"): void {
  try {
    activeTheme = new Theme(loadThemeJson(themeName));
  } catch {
    activeTheme = new Theme(DARK_THEME);
  }
}

// ---------------------------------------------------------------------------
// Syntax highlighting (highlight.js/lib/common — ~35 languages instead of
// pi-mono's full set; covers everything in getLanguageFromPath that matters)
// ---------------------------------------------------------------------------

import hljs from "highlight.js/lib/common";

type HighlightTheme = Record<string, (s: string) => string>;

let cachedHighlightTheme: HighlightTheme | null = null;
let cachedHighlightFor: Theme | null = null;

function getHighlightTheme(): HighlightTheme {
  if (cachedHighlightFor !== activeTheme || !cachedHighlightTheme) {
    cachedHighlightFor = activeTheme;
    cachedHighlightTheme = {
      keyword: (s) => theme.fg("syntaxKeyword", s),
      built_in: (s) => theme.fg("syntaxType", s),
      literal: (s) => theme.fg("syntaxNumber", s),
      number: (s) => theme.fg("syntaxNumber", s),
      string: (s) => theme.fg("syntaxString", s),
      comment: (s) => theme.fg("syntaxComment", s),
      function: (s) => theme.fg("syntaxFunction", s),
      title: (s) => theme.fg("syntaxFunction", s),
      class: (s) => theme.fg("syntaxType", s),
      type: (s) => theme.fg("syntaxType", s),
      attr: (s) => theme.fg("syntaxVariable", s),
      variable: (s) => theme.fg("syntaxVariable", s),
      params: (s) => theme.fg("syntaxVariable", s),
      operator: (s) => theme.fg("syntaxOperator", s),
      punctuation: (s) => theme.fg("syntaxPunctuation", s),
    };
  }
  return cachedHighlightTheme;
}

const ENTITIES: Record<string, string> = { "&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&#x27;": "'" };

/** Convert hljs HTML span markup to ANSI using the active theme. */
function renderHighlightedHtml(html: string, hlTheme: HighlightTheme): string {
  let output = "";
  let textBuffer = "";
  const scopes: Array<string | undefined> = [];

  const formatterFor = (scope: string): ((s: string) => string) | undefined => {
    if (hlTheme[scope]) return hlTheme[scope];
    const dot = scope.indexOf(".");
    if (dot !== -1 && hlTheme[scope.slice(0, dot)]) return hlTheme[scope.slice(0, dot)];
    const dash = scope.indexOf("-");
    if (dash !== -1 && hlTheme[scope.slice(0, dash)]) return hlTheme[scope.slice(0, dash)];
    return undefined;
  };

  const flush = () => {
    if (!textBuffer) return;
    let formatter: ((s: string) => string) | undefined;
    for (let i = scopes.length - 1; i >= 0; i--) {
      const scope = scopes[i];
      if (scope) {
        formatter = formatterFor(scope);
        if (formatter) break;
      }
    }
    output += formatter ? formatter(textBuffer) : textBuffer;
    textBuffer = "";
  };

  let i = 0;
  while (i < html.length) {
    if (html.startsWith("<span", i)) {
      const end = html.indexOf(">", i + 5);
      if (end !== -1) {
        flush();
        const tag = html.slice(i, end + 1);
        const m = /class\s*=\s*"([^"]*)"/.exec(tag);
        const cls = m?.[1]?.split(/\s+/).find((c) => c.startsWith("hljs-"));
        scopes.push(cls ? cls.slice(5) : undefined);
        i = end + 1;
        continue;
      }
    }
    if (html.startsWith("</span>", i)) {
      flush();
      scopes.pop();
      i += 7;
      continue;
    }
    if (html[i] === "&") {
      const entity = Object.keys(ENTITIES).find((e) => html.startsWith(e, i));
      if (entity) {
        textBuffer += ENTITIES[entity];
        i += entity.length;
        continue;
      }
    }
    textBuffer += html[i];
    i++;
  }
  flush();
  return output;
}

export function highlightCode(code: string, lang?: string): string[] {
  // No valid language → plain mdCodeBlock color. Auto-detection is unreliable
  // (mirrors pi-mono's reasoning), so we never auto-detect.
  const validLang = lang && hljs.getLanguage(lang) ? lang : undefined;
  if (!validLang) return code.split("\n").map((line) => theme.fg("mdCodeBlock", line));
  try {
    const html = hljs.highlight(code, { language: validLang, ignoreIllegals: true }).value;
    return renderHighlightedHtml(html, getHighlightTheme()).split("\n");
  } catch {
    return code.split("\n").map((line) => theme.fg("mdCodeBlock", line));
  }
}

const EXT_TO_LANG: Record<string, string> = {
  ts: "typescript",
  tsx: "typescript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  py: "python",
  rb: "ruby",
  rs: "rust",
  go: "go",
  java: "java",
  kt: "kotlin",
  swift: "swift",
  c: "c",
  h: "c",
  cpp: "cpp",
  cc: "cpp",
  cxx: "cpp",
  hpp: "cpp",
  cs: "csharp",
  php: "php",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  sql: "sql",
  html: "html",
  htm: "html",
  css: "css",
  scss: "scss",
  less: "less",
  json: "json",
  yaml: "yaml",
  yml: "yaml",
  toml: "ini",
  xml: "xml",
  md: "markdown",
  markdown: "markdown",
  makefile: "makefile",
  lua: "lua",
  perl: "perl",
  r: "r",
  scala: "scala",
  graphql: "graphql",
};

export function getLanguageFromPath(filePath: string): string | undefined {
  const ext = filePath.split(".").pop()?.toLowerCase();
  return ext ? EXT_TO_LANG[ext] : undefined;
}

// ---------------------------------------------------------------------------
// pi-tui theme adapters
// ---------------------------------------------------------------------------

export function getMarkdownTheme(): MarkdownTheme {
  return {
    heading: (text) => theme.fg("mdHeading", text),
    link: (text) => theme.fg("mdLink", text),
    linkUrl: (text) => theme.fg("mdLinkUrl", text),
    code: (text) => theme.fg("mdCode", text),
    codeBlock: (text) => theme.fg("mdCodeBlock", text),
    codeBlockBorder: (text) => theme.fg("mdCodeBlockBorder", text),
    quote: (text) => theme.fg("mdQuote", text),
    quoteBorder: (text) => theme.fg("mdQuoteBorder", text),
    hr: (text) => theme.fg("mdHr", text),
    listBullet: (text) => theme.fg("mdListBullet", text),
    bold: (text) => theme.bold(text),
    italic: (text) => theme.italic(text),
    underline: (text) => theme.underline(text),
    strikethrough: (text) => chalk.strikethrough(text),
    highlightCode: (code, lang) => highlightCode(code, lang),
  };
}

export function getSelectListTheme(): SelectListTheme {
  return {
    selectedPrefix: (text) => theme.fg("accent", text),
    selectedText: (text) => theme.fg("accent", text),
    description: (text) => theme.fg("muted", text),
    scrollInfo: (text) => theme.fg("muted", text),
    noMatch: (text) => theme.fg("muted", text),
  };
}
