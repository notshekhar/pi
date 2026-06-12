/**
 * Fork-from-message selector — ported from pi-mono
 * modes/interactive/components/user-message-selector.ts.
 */
import { type Component, Container, getKeybindings, Spacer, Text, truncateToWidth } from "@notshekhar/pi-tui";
import { theme } from "./theme";
import { DynamicBorder } from "./messages";

export interface UserMessageItem {
    /** Entry ID in the session */
    id: string;
    /** The message text */
    text: string;
}

/**
 * Custom user message list component with selection
 */
class UserMessageList implements Component {
    private messages: UserMessageItem[] = [];
    private selectedIndex: number = 0;
    public onSelect?: (entryId: string) => void;
    public onCancel?: () => void;
    private maxVisible: number = 10;

    constructor(messages: UserMessageItem[], initialSelectedId?: string) {
        // Messages arrive in chronological order (oldest to newest)
        this.messages = messages;
        const initialIndex = initialSelectedId ? messages.findIndex((m) => m.id === initialSelectedId) : -1;
        this.selectedIndex = initialIndex >= 0 ? initialIndex : Math.max(0, messages.length - 1);
    }

    invalidate(): void {}

    render(width: number): string[] {
        const lines: string[] = [];

        if (this.messages.length === 0) {
            lines.push(theme.fg("muted", "  No user messages found"));
            return lines;
        }

        const startIndex = Math.max(
            0,
            Math.min(this.selectedIndex - Math.floor(this.maxVisible / 2), this.messages.length - this.maxVisible),
        );
        const endIndex = Math.min(startIndex + this.maxVisible, this.messages.length);

        for (let i = startIndex; i < endIndex; i++) {
            const message = this.messages[i];
            const isSelected = i === this.selectedIndex;

            const normalizedMessage = message.text.replace(/\n/g, " ").trim();
            const cursor = isSelected ? theme.fg("accent", "› ") : "  ";
            const truncatedMsg = truncateToWidth(normalizedMessage, width - 2);
            lines.push(cursor + (isSelected ? theme.bold(truncatedMsg) : truncatedMsg));
            lines.push(theme.fg("muted", `  Message ${i + 1} of ${this.messages.length}`));
            lines.push("");
        }

        if (startIndex > 0 || endIndex < this.messages.length) {
            lines.push(theme.fg("muted", `  (${this.selectedIndex + 1}/${this.messages.length})`));
        }

        return lines;
    }

    handleInput(keyData: string): void {
        const kb = getKeybindings();
        if (kb.matches(keyData, "tui.select.up")) {
            this.selectedIndex = this.selectedIndex === 0 ? this.messages.length - 1 : this.selectedIndex - 1;
        } else if (kb.matches(keyData, "tui.select.down")) {
            this.selectedIndex = this.selectedIndex === this.messages.length - 1 ? 0 : this.selectedIndex + 1;
        } else if (kb.matches(keyData, "tui.select.confirm")) {
            const selected = this.messages[this.selectedIndex];
            if (selected && this.onSelect) this.onSelect(selected.id);
        } else if (kb.matches(keyData, "tui.select.cancel")) {
            this.onCancel?.();
        }
    }
}

/**
 * Component that renders a user message selector for forking
 */
export class UserMessageSelectorComponent extends Container {
    private messageList: UserMessageList;

    constructor(
        messages: UserMessageItem[],
        onSelect: (entryId: string) => void,
        onCancel: () => void,
        initialSelectedId?: string,
    ) {
        super();

        this.addChild(new Spacer(1));
        this.addChild(new Text(theme.bold("Fork from Message"), 1, 0));
        this.addChild(
            new Text(
                theme.fg("muted", "Select a user message to copy the active path up to that point into a new session"),
                1,
                0,
            ),
        );
        this.addChild(new Spacer(1));
        this.addChild(new DynamicBorder());
        this.addChild(new Spacer(1));

        this.messageList = new UserMessageList(messages, initialSelectedId);
        this.messageList.onSelect = onSelect;
        this.messageList.onCancel = onCancel;

        this.addChild(this.messageList);

        this.addChild(new Spacer(1));
        this.addChild(new DynamicBorder());

        if (messages.length === 0) {
            setTimeout(() => onCancel(), 100);
        }
    }

    getMessageList(): UserMessageList {
        return this.messageList;
    }
}
