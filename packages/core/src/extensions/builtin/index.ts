/**
 * Built-in extensions — bundled with the loop binary (statically imported, so
 * `bun --compile` includes them). They're "pre-installed": they appear in
 * `/extensions` and `loop list` without `loop install`, and the user just
 * enables the ones they want. Enable state lives in the extensions store; all
 * ship disabled by default (opt-in), so a clean install behaves exactly as
 * before until a user turns one on.
 */
import type { ExtensionModule } from "../api";
import lsp from "./lsp/index";
import ponytail from "./ponytail/index";
import caveman from "./caveman/index";
import rtk from "./rtk/index";

export interface BuiltinExtension {
    name: string;
    displayName: string;
    description: string;
    module: ExtensionModule;
    /** Whether it's on unless the user disables it. All false today (opt-in). */
    defaultEnabled: boolean;
}

export const BUILTIN_EXTENSIONS: BuiltinExtension[] = [
    {
        name: "lsp",
        displayName: "LSP Diagnostics",
        description: "Appends type/lint errors after write/edit via language servers.",
        module: lsp,
        defaultEnabled: false,
    },
    {
        name: "ponytail",
        displayName: "Ponytail",
        description: "Lazy senior dev — write the minimal solution (lite/full/ultra). /ponytail",
        module: ponytail,
        defaultEnabled: false,
    },
    {
        name: "caveman",
        displayName: "Caveman",
        description: "Ultra-terse replies — fewer tokens, full substance (lite/full/ultra). /caveman",
        module: caveman,
        defaultEnabled: false,
    },
    {
        name: "rtk",
        displayName: "RTK Token Optimizer",
        description: "Rewrites bash commands to compress output 60-90% (needs the rtk binary). /rtk",
        module: rtk,
        defaultEnabled: false,
    },
];

export function getBuiltin(name: string): BuiltinExtension | undefined {
    return BUILTIN_EXTENSIONS.find((b) => b.name === name);
}

export function isBuiltin(name: string): boolean {
    return BUILTIN_EXTENSIONS.some((b) => b.name === name);
}
