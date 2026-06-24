/**
 * Extensions panel: /extensions opens an interactive list of built-in (bundled)
 * and installed extensions, each enable/disable/reload-able; installed ones can
 * also be uninstalled. `/install <spec>` (and `/extensions install <spec>`) is
 * the non-interactive shortcut. Mirrors the /mcp panel.
 *
 * Tools, providers, and turn middleware take effect immediately; newly added
 * slash commands need a /reload (surfaced in the confirmations).
 */
import type { SelectItem } from "@notshekhar/loop-tui";
import {
    bustCatalogCache,
    getExtensionHost,
    installExtension,
    removeExtension,
    setBuiltinEnabled,
    setRecordEnabled,
    type CommandContext,
} from "@notshekhar/loop-core";
import type { AppDeps } from "../deps";
import type { AppState } from "../state";

type ExtensionHandlers = Pick<CommandContext, "manageExtensions">;
type Entry = ReturnType<ReturnType<typeof getExtensionHost>["listAll"]>[number];

const INSTALL_ROW = "\0install";

export function createExtensionHandlers(_state: AppState, deps: AppDeps): ExtensionHandlers {
    const { tui, history, searchOnce, selectOnce, promptOnce } = deps;

    async function installFlow(spec: string): Promise<void> {
        history.addSystem(`installing ${spec}…`);
        tui.requestRender();
        try {
            const r = await installExtension(spec);
            await getExtensionHost().reload(r.name);
            bustCatalogCache();
            history.addSystem(
                `✓ installed ${r.name}${r.version ? `@${r.version}` : ""} — /reload to pick up its slash commands`,
            );
        } catch (err) {
            history.addError(`install failed: ${err instanceof Error ? err.message : String(err)}`);
        }
        tui.requestRender();
    }

    async function setEnabled(entry: Entry, enabled: boolean): Promise<void> {
        if (entry.builtin) setBuiltinEnabled(entry.name, enabled);
        else setRecordEnabled(entry.name, enabled);
        const host = getExtensionHost();
        if (enabled) await host.reload(entry.name);
        else await host.unload(entry.name);
        bustCatalogCache();
    }

    async function extActions(entry: Entry): Promise<void> {
        const toggle = entry.enabled ? "disable" : "enable";
        const actions: SelectItem[] = [
            { value: toggle, label: toggle, description: entry.enabled ? "stop loading it" : "load it now" },
            { value: "reload", label: "reload", description: "re-run the extension (pick up edits)" },
            ...(entry.builtin
                ? []
                : [{ value: "uninstall", label: "uninstall", description: "remove the extension and its files" }]),
            { value: "info", label: "info", description: "show details" },
        ];
        const pick = await selectOnce(actions, `${entry.displayName}${entry.builtin ? " (built-in)" : ""}`);
        if (!pick) return;
        const host = getExtensionHost();
        switch (pick.value) {
            case "enable":
                await setEnabled(entry, true);
                history.addSystem(`✓ enabled ${entry.name} — /reload to add its slash commands`);
                break;
            case "disable":
                await setEnabled(entry, false);
                history.addSystem(`✓ disabled ${entry.name}`);
                break;
            case "reload":
                await host.reload(entry.name);
                bustCatalogCache();
                history.addSystem(`✓ reloaded ${entry.name}`);
                break;
            case "uninstall":
                await host.unload(entry.name);
                removeExtension(entry.name);
                bustCatalogCache();
                history.addSystem(`✓ uninstalled ${entry.name}`);
                break;
            case "info":
                history.addSystem(
                    entry.builtin
                        ? `${entry.name} (built-in)\n  ${entry.description ?? ""}\n  enabled: ${entry.enabled}`
                        : `${entry.name}@${entry.version ?? "?"}\n  source: ${entry.source}\n  enabled: ${entry.enabled}${entry.linkPath ? `\n  linked: ${entry.linkPath}` : ""}`,
                );
                break;
        }
        tui.requestRender();
    }

    return {
        async manageExtensions(args: string) {
            const trimmed = args.trim();
            if (trimmed.startsWith("install")) {
                const spec = trimmed.slice("install".length).trim();
                if (!spec) {
                    history.addSystem("usage: /install <npm|github:owner/repo|path>");
                    tui.requestRender();
                    return;
                }
                await installFlow(spec);
                return;
            }

            let lastIndex = 0;
            while (true) {
                const all = getExtensionHost().listAll();
                const items: SelectItem[] = [
                    {
                        value: INSTALL_ROW,
                        label: "+ install",
                        description: "install from npm, github:owner/repo, or a local path",
                    },
                    ...all.map((e) => ({
                        value: e.name,
                        label: `${e.enabled ? "●" : "○"} ${e.displayName}${e.builtin ? "  ·  built-in" : e.version ? `@${e.version}` : ""}`,
                        description: e.description ?? `${e.linkPath ? "linked · " : ""}${e.source ?? ""}`,
                    })),
                ];
                const pick = await searchOnce(items, "Extensions (type to filter, Esc to close)", {
                    initialIndex: lastIndex,
                });
                if (!pick) return;
                lastIndex = Math.max(
                    0,
                    items.findIndex((i) => i.value === pick.value),
                );
                if (pick.value === INSTALL_ROW) {
                    const spec = (await promptOnce("install (npm name, github:owner/repo, or path)")).trim();
                    if (spec) await installFlow(spec);
                    continue;
                }
                const entry = getExtensionHost()
                    .listAll()
                    .find((e) => e.name === pick.value);
                if (entry) await extActions(entry);
            }
        },
    };
}
