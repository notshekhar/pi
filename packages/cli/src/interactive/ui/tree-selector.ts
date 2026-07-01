/**
 * Session tree selector.
 * modes/interactive/components/tree-selector.ts. The component shell lives
 * here; list state/input is in tree/tree-list.ts, layout math in
 * tree/layout.ts, row presentation in tree/entry-display.ts.
 */
import {
    type Component,
    Container,
    type Focusable,
    getKeybindings,
    Input,
    Spacer,
    Text,
    TruncatedText,
    truncateToWidth,
} from "@notshekhar/loop-tui";
import type { SessionTreeNode } from "@notshekhar/loop-core";
import { theme } from "./theme";
import { DynamicBorder } from "./messages";
import { TreeList, keyText, type FilterMode } from "./tree/tree-list";

export { keyText, type FilterMode };

/** Component that displays the current search query */
class SearchLine implements Component {
    constructor(private treeList: TreeList) {}

    invalidate(): void {}

    render(width: number): string[] {
        const query = this.treeList.getSearchQuery();
        if (query) {
            return [truncateToWidth(`  ${theme.fg("muted", "Type to search:")} ${theme.fg("accent", query)}`, width)];
        }
        return [truncateToWidth(`  ${theme.fg("muted", "Type to search:")}`, width)];
    }

    handleInput(_keyData: string): void {}
}

/** Label input component shown when editing a label */
class LabelInput implements Component, Focusable {
    private input: Input;
    private entryId: string;
    public onSubmit?: (entryId: string, label: string | undefined) => void;
    public onCancel?: () => void;

    // Focusable implementation - propagate to input for IME cursor positioning
    private _focused = false;
    get focused(): boolean {
        return this._focused;
    }
    set focused(value: boolean) {
        this._focused = value;
        this.input.focused = value;
    }

    constructor(entryId: string, currentLabel: string | undefined) {
        this.entryId = entryId;
        this.input = new Input();
        if (currentLabel) {
            this.input.setValue(currentLabel);
        }
    }

    invalidate(): void {}

    render(width: number): string[] {
        const lines: string[] = [];
        const indent = "  ";
        const availableWidth = width - indent.length;
        lines.push(truncateToWidth(`${indent}${theme.fg("muted", "Label (empty to remove):")}`, width));
        lines.push(...this.input.render(availableWidth).map((line) => truncateToWidth(`${indent}${line}`, width)));
        lines.push(truncateToWidth(`${indent}${theme.fg("muted", "enter: save  esc: cancel")}`, width));
        return lines;
    }

    handleInput(keyData: string): void {
        const kb = getKeybindings();
        if (kb.matches(keyData, "tui.select.confirm")) {
            const value = this.input.getValue().trim();
            this.onSubmit?.(this.entryId, value || undefined);
        } else if (kb.matches(keyData, "tui.select.cancel")) {
            this.onCancel?.();
        } else {
            this.input.handleInput(keyData);
        }
    }
}

/**
 * Component that renders a session tree selector for navigation
 */
export class TreeSelectorComponent extends Container implements Focusable {
    private treeList: TreeList;
    private labelInput: LabelInput | null = null;
    private labelInputContainer: Container;
    private treeContainer: Container;
    private onLabelChangeCallback?: (entryId: string, label: string | undefined) => void;

    // Focusable implementation - propagate to labelInput when active for IME cursor positioning
    private _focused = false;
    get focused(): boolean {
        return this._focused;
    }
    set focused(value: boolean) {
        this._focused = value;
        if (this.labelInput) {
            this.labelInput.focused = value;
        }
    }

    constructor(
        tree: SessionTreeNode[],
        currentLeafId: string | null,
        terminalHeight: number,
        cwd: string,
        onSelect: (entryId: string) => void,
        onCancel: () => void,
        onLabelChange?: (entryId: string, label: string | undefined) => void,
        initialSelectedId?: string,
        initialFilterMode?: FilterMode,
    ) {
        super();

        this.onLabelChangeCallback = onLabelChange;
        const maxVisibleLines = Math.max(5, Math.floor(terminalHeight / 2));

        this.treeList = new TreeList(tree, currentLeafId, maxVisibleLines, cwd, initialSelectedId, initialFilterMode);
        this.treeList.onSelect = onSelect;
        this.treeList.onCancel = onCancel;
        this.treeList.onLabelEdit = (entryId, currentLabel) => this.showLabelInput(entryId, currentLabel);

        this.treeContainer = new Container();
        this.treeContainer.addChild(this.treeList);

        this.labelInputContainer = new Container();

        this.addChild(new Spacer(1));
        this.addChild(new DynamicBorder());
        this.addChild(new Text(theme.bold("  Session Tree"), 1, 0));
        this.addChild(new TruncatedText(theme.fg("muted", `  ${this.keyHintLine()}`), 0, 0));
        this.addChild(new SearchLine(this.treeList));
        this.addChild(new DynamicBorder());
        this.addChild(new Spacer(1));
        this.addChild(this.treeContainer);
        this.addChild(this.labelInputContainer);
        this.addChild(new Spacer(1));
        this.addChild(new DynamicBorder());

        if (tree.length === 0) {
            setTimeout(() => onCancel(), 100);
        }
    }

    private keyHintLine(): string {
        const filterKeys = [
            keyText("app.tree.filter.default"),
            keyText("app.tree.filter.noTools"),
            keyText("app.tree.filter.userOnly"),
            keyText("app.tree.filter.labeledOnly"),
            keyText("app.tree.filter.all"),
        ].join("/");
        const cycleKeys = `${keyText("app.tree.filter.cycleForward")}/${keyText("app.tree.filter.cycleBackward")}`;
        const branchKeys = `${keyText("app.tree.foldOrUp")}/${keyText("app.tree.unfoldOrDown")}`;
        return `↑/↓: move. ←/→: page. ${branchKeys}: fold/branch. ${keyText("app.tree.editLabel")}: label. ${filterKeys}: filters (${cycleKeys} cycle). ${keyText("app.tree.toggleLabelTimestamp")}: label time`;
    }

    private showLabelInput(entryId: string, currentLabel: string | undefined): void {
        this.labelInput = new LabelInput(entryId, currentLabel);
        this.labelInput.onSubmit = (id, label) => {
            this.treeList.updateNodeLabel(id, label);
            this.onLabelChangeCallback?.(id, label);
            this.hideLabelInput();
        };
        this.labelInput.onCancel = () => this.hideLabelInput();

        this.labelInput.focused = this._focused;

        this.treeContainer.clear();
        this.labelInputContainer.clear();
        this.labelInputContainer.addChild(this.labelInput);
    }

    private hideLabelInput(): void {
        this.labelInput = null;
        this.labelInputContainer.clear();
        this.treeContainer.clear();
        this.treeContainer.addChild(this.treeList);
    }

    handleInput(keyData: string): void {
        if (this.labelInput) {
            this.labelInput.handleInput(keyData);
        } else {
            this.treeList.handleInput(keyData);
        }
    }

    getTreeList(): TreeList {
        return this.treeList;
    }
}
