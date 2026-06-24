/**
 * Shell-side extension commands: `loop install <npm|github|path>`, plus the
 * scriptable management verbs. Interactive browse/enable/disable/uninstall live
 * in the in-session `/extensions` panel; these mirror them for scripts and
 * pre-launch setup. Dependency resolution happens inside install/link via the
 * embedded Bun runtime.
 */
import {
    BUILTIN_EXTENSIONS,
    getBuiltinEnabled,
    installExtension,
    isBuiltin,
    linkExtension,
    listRecords,
    removeExtension,
    setBuiltinEnabled,
    setRecordEnabled,
    syncExtensions,
} from "@notshekhar/loop-core";
import type { Args } from "./args";

export async function cmdInstall(args: Args): Promise<void> {
    const spec = args.positional[0];
    if (!spec) {
        // Bare `loop install` repairs/reinstalls deps for everything (bun-style).
        console.log("Syncing all installed extensions…");
        await syncExtensions();
        console.log("Done.");
        return;
    }
    try {
        const r = await installExtension(spec);
        console.log(`Installed ${r.name}${r.version ? `@${r.version}` : ""}`);
    } catch (err) {
        console.error(`Install failed: ${(err as Error).message}`);
        process.exitCode = 1;
    }
}

export async function cmdLink(args: Args): Promise<void> {
    const path = args.positional[0];
    if (!path) {
        console.error("usage: loop link <path>");
        process.exitCode = 1;
        return;
    }
    try {
        const { resolve } = await import("node:path");
        const r = await linkExtension(resolve(process.cwd(), path));
        console.log(`Linked ${r.name}${r.version ? `@${r.version}` : ""} (dev mode)`);
    } catch (err) {
        console.error(`Link failed: ${(err as Error).message}`);
        process.exitCode = 1;
    }
}

export function cmdListExtensions(): void {
    console.log("Built-in (toggle with: loop enable|disable <name>):");
    for (const b of BUILTIN_EXTENSIONS) {
        const state = getBuiltinEnabled(b.name, b.defaultEnabled) ? "on " : "off";
        console.log(`  [${state}] ${b.name}  — ${b.description}`);
    }
    const recs = listRecords();
    if (recs.length > 0) {
        console.log("\nInstalled:");
        for (const r of recs) {
            const state = r.enabled ? "on " : "off";
            const linked = r.linkPath ? " (linked)" : "";
            console.log(`  [${state}] ${r.name}${r.version ? `@${r.version}` : ""}${linked}  — ${r.source}`);
        }
    }
}

export async function cmdRemoveExtension(args: Args): Promise<void> {
    const name = args.positional[0];
    if (!name) {
        console.error("usage: loop remove <name>");
        process.exitCode = 1;
        return;
    }
    console.log(removeExtension(name) ? `Removed ${name}` : `Not installed: ${name}`);
}

export function cmdSetExtensionEnabled(args: Args, enabled: boolean): void {
    const name = args.positional[0];
    if (!name) {
        console.error(`usage: loop ${enabled ? "enable" : "disable"} <name>`);
        process.exitCode = 1;
        return;
    }
    if (isBuiltin(name)) {
        setBuiltinEnabled(name, enabled);
        console.log(`${enabled ? "Enabled" : "Disabled"} ${name} (built-in)`);
        return;
    }
    const ok = setRecordEnabled(name, enabled);
    console.log(ok ? `${enabled ? "Enabled" : "Disabled"} ${name}` : `Not installed: ${name}`);
}
