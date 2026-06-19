/** App-level keybindings layered over the tui defaults. */
import { KeybindingsManager, setKeybindings, TUI_KEYBINDINGS } from "@notshekhar/loop-tui";

const APP_KEYBINDINGS = {
    "app.tools.expand": { defaultKeys: "ctrl+e", description: "Toggle tool output" },
    "app.interrupt": { defaultKeys: "escape", description: "Interrupt agent" },
    "app.clear": { defaultKeys: "ctrl+c", description: "Clear / exit" },
    // /tree selector bindings
    "app.tree.foldOrUp": { defaultKeys: ["ctrl+left", "alt+left"], description: "Fold tree branch or move up" },
    "app.tree.unfoldOrDown": {
        defaultKeys: ["ctrl+right", "alt+right"],
        description: "Unfold tree branch or move down",
    },
    "app.tree.editLabel": { defaultKeys: "shift+l", description: "Edit tree label" },
    "app.tree.toggleLabelTimestamp": { defaultKeys: "shift+t", description: "Toggle tree label timestamps" },
    "app.tree.filter.default": { defaultKeys: "ctrl+d", description: "Tree filter: default view" },
    "app.tree.filter.noTools": { defaultKeys: "ctrl+t", description: "Tree filter: hide tool results" },
    "app.tree.filter.userOnly": { defaultKeys: "ctrl+u", description: "Tree filter: user messages only" },
    "app.tree.filter.labeledOnly": { defaultKeys: "ctrl+l", description: "Tree filter: labeled entries only" },
    "app.tree.filter.all": { defaultKeys: "ctrl+a", description: "Tree filter: show all entries" },
    "app.tree.filter.cycleForward": { defaultKeys: "ctrl+o", description: "Tree filter: cycle forward" },
    "app.tree.filter.cycleBackward": { defaultKeys: "shift+ctrl+o", description: "Tree filter: cycle backward" },
} as const;

export function registerAppKeybindings(): void {
    setKeybindings(new KeybindingsManager({ ...TUI_KEYBINDINGS, ...APP_KEYBINDINGS } as never));
}
