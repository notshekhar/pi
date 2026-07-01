/**
 * Scrollable, filterable list over the flattened session tree: cursor,
 * filter modes, type-to-search, folding, and label editing hooks. Layout
 * math lives in layout.ts, row presentation in entry-display.ts.
 */
import { type Component, getKeybindings, type Keybinding, truncateToWidth } from "@notshekhar/loop-tui";
import type { SessionTreeNode } from "@notshekhar/loop-core";
import { theme } from "../theme";
import { type FlatNode, flattenTree, recalculateVisualStructure } from "./layout";
import {
    buildToolCallMap,
    formatLabelTimestamp,
    getEntryDisplayText,
    getSearchableText,
    hasTextContent,
    type TreeRenderContext,
} from "./entry-display";

/** App-level keybindings are registered at startup; the tui Keybinding union doesn't know them. */
export const kbMatches = (keyData: string, name: string): boolean =>
    getKeybindings().matches(keyData, name as Keybinding);
export const keyText = (name: string): string => getKeybindings().getKeys(name as Keybinding)[0] ?? "";

/** Filter mode for tree display */
export type FilterMode = "default" | "no-tools" | "user-only" | "labeled-only" | "all";

const FILTER_MODES: FilterMode[] = ["default", "no-tools", "user-only", "labeled-only", "all"];

const FILTER_STATUS_LABEL: Partial<Record<FilterMode, string>> = {
    "no-tools": " [no-tools]",
    "user-only": " [user]",
    "labeled-only": " [labeled]",
    all: " [all]",
};

export class TreeList implements Component {
    private flatNodes: FlatNode[] = [];
    private filteredNodes: FlatNode[] = [];
    private selectedIndex = 0;
    private currentLeafId: string | null;
    private maxVisibleLines: number;
    private filterMode: FilterMode = "default";
    private searchQuery = "";
    private multipleRoots = false;
    private showLabelTimestamps = false;
    private activePathIds: Set<string> = new Set();
    private visibleParentMap: Map<string, string | null> = new Map();
    private visibleChildrenMap: Map<string | null, string[]> = new Map();
    private lastSelectedId: string | null = null;
    private foldedNodes: Set<string> = new Set();
    private renderCtx: TreeRenderContext;

    public onSelect?: (entryId: string) => void;
    public onCancel?: () => void;
    public onLabelEdit?: (entryId: string, currentLabel: string | undefined) => void;

    constructor(
        tree: SessionTreeNode[],
        currentLeafId: string | null,
        maxVisibleLines: number,
        cwd: string,
        initialSelectedId?: string,
        initialFilterMode?: FilterMode,
    ) {
        this.currentLeafId = currentLeafId;
        this.maxVisibleLines = maxVisibleLines;
        // Resolve tool-result rows to their call's name + args (built once).
        this.renderCtx = { toolCalls: buildToolCallMap(tree), cwd };
        this.filterMode = initialFilterMode ?? "default";
        const { flatNodes, multipleRoots } = flattenTree(tree, currentLeafId);
        this.flatNodes = flatNodes;
        this.multipleRoots = multipleRoots;
        this.buildActivePath();
        this.applyFilter();

        const targetId = initialSelectedId ?? currentLeafId;
        this.selectedIndex = this.findNearestVisibleIndex(targetId);
        this.lastSelectedId = this.filteredNodes[this.selectedIndex]?.node.entry.id ?? null;
    }

    invalidate(): void {}

    getSearchQuery(): string {
        return this.searchQuery;
    }

    getSelectedNode(): SessionTreeNode | undefined {
        return this.filteredNodes[this.selectedIndex]?.node;
    }

    updateNodeLabel(entryId: string, label: string | undefined): void {
        for (const flatNode of this.flatNodes) {
            if (flatNode.node.entry.id === entryId) {
                flatNode.node.label = label;
                flatNode.node.labelTimestamp = label ? Date.now() : undefined;
                break;
            }
        }
    }

    /**
     * Find the index of the nearest visible entry, walking up the parent
     * chain if needed. Falls back to the last visible entry.
     */
    private findNearestVisibleIndex(entryId: string | null): number {
        if (this.filteredNodes.length === 0) return 0;

        const entryMap = new Map<string, FlatNode>();
        for (const flatNode of this.flatNodes) {
            entryMap.set(flatNode.node.entry.id!, flatNode);
        }
        const visibleIdToIndex = new Map<string, number>(this.filteredNodes.map((node, i) => [node.node.entry.id!, i]));

        let currentId = entryId;
        while (currentId !== null) {
            const index = visibleIdToIndex.get(currentId);
            if (index !== undefined) return index;
            const node = entryMap.get(currentId);
            if (!node) break;
            currentId = node.node.entry.parentId ?? null;
        }

        return this.filteredNodes.length - 1;
    }

    /** Build the set of entry IDs on the path from root to current leaf */
    private buildActivePath(): void {
        this.activePathIds.clear();
        if (!this.currentLeafId) return;

        const entryMap = new Map<string, FlatNode>();
        for (const flatNode of this.flatNodes) {
            entryMap.set(flatNode.node.entry.id!, flatNode);
        }

        let currentId: string | null = this.currentLeafId;
        while (currentId) {
            this.activePathIds.add(currentId);
            const node = entryMap.get(currentId);
            if (!node) break;
            currentId = node.node.entry.parentId ?? null;
        }
    }

    private passesFilterMode(flatNode: FlatNode): boolean {
        const entry = flatNode.node.entry;
        // Entry types hidden in default view (settings/bookkeeping)
        const isSettingsEntry =
            entry.type === "label" ||
            entry.type === "custom" ||
            entry.type === "model-change" ||
            entry.type === "session-info";

        switch (this.filterMode) {
            case "user-only":
                return entry.type === "message" && entry.role === "user";
            case "no-tools":
                return !isSettingsEntry && !(entry.type === "message" && entry.role === "tool");
            case "labeled-only":
                return flatNode.node.label !== undefined;
            case "all":
                return true;
            default:
                return !isSettingsEntry;
        }
    }

    private applyFilter(): void {
        // Preserve the selection across re-filters (skip when the previous
        // filter produced an empty list).
        if (this.filteredNodes.length > 0) {
            this.lastSelectedId = this.filteredNodes[this.selectedIndex]?.node.entry.id ?? this.lastSelectedId;
        }

        const searchTokens = this.searchQuery.toLowerCase().split(/\s+/).filter(Boolean);

        this.filteredNodes = this.flatNodes.filter((flatNode) => {
            const entry = flatNode.node.entry;
            const isCurrentLeaf = entry.id === this.currentLeafId;

            // Skip empty assistant messages unless they're the current leaf
            if (entry.type === "message" && entry.role === "assistant" && !isCurrentLeaf) {
                if (!hasTextContent(entry.content)) return false;
            }

            if (!this.passesFilterMode(flatNode)) return false;

            if (searchTokens.length > 0) {
                const nodeText = getSearchableText(flatNode.node, this.renderCtx).toLowerCase();
                return searchTokens.every((token) => nodeText.includes(token));
            }

            return true;
        });

        // Filter out descendants of folded nodes.
        if (this.foldedNodes.size > 0) {
            const skipSet = new Set<string>();
            for (const flatNode of this.flatNodes) {
                const { id, parentId } = flatNode.node.entry;
                if (parentId != null && (this.foldedNodes.has(parentId) || skipSet.has(parentId))) {
                    skipSet.add(id!);
                }
            }
            this.filteredNodes = this.filteredNodes.filter((flatNode) => !skipSet.has(flatNode.node.entry.id!));
        }

        const structure = recalculateVisualStructure(this.flatNodes, this.filteredNodes);
        this.visibleParentMap = structure.visibleParent;
        this.visibleChildrenMap = structure.visibleChildren;
        if (this.filteredNodes.length > 0) this.multipleRoots = structure.multipleRoots;

        // Keep the cursor on the same node, or its nearest visible ancestor.
        if (this.lastSelectedId) {
            this.selectedIndex = this.findNearestVisibleIndex(this.lastSelectedId);
        } else if (this.selectedIndex >= this.filteredNodes.length) {
            this.selectedIndex = Math.max(0, this.filteredNodes.length - 1);
        }
        if (this.filteredNodes.length > 0) {
            this.lastSelectedId = this.filteredNodes[this.selectedIndex]?.node.entry.id ?? this.lastSelectedId;
        }
    }

    private getStatusLabels(): string {
        let labels = FILTER_STATUS_LABEL[this.filterMode] ?? "";
        if (this.showLabelTimestamps) labels += " [+label time]";
        return labels;
    }

    render(width: number): string[] {
        const lines: string[] = [];

        if (this.filteredNodes.length === 0) {
            lines.push(truncateToWidth(theme.fg("muted", "  No entries found"), width));
            lines.push(truncateToWidth(theme.fg("muted", `  (0/0)${this.getStatusLabels()}`), width));
            return lines;
        }

        const startIndex = Math.max(
            0,
            Math.min(
                this.selectedIndex - Math.floor(this.maxVisibleLines / 2),
                this.filteredNodes.length - this.maxVisibleLines,
            ),
        );
        const endIndex = Math.min(startIndex + this.maxVisibleLines, this.filteredNodes.length);

        for (let i = startIndex; i < endIndex; i++) {
            lines.push(truncateToWidth(this.renderRow(this.filteredNodes[i], i === this.selectedIndex), width));
        }

        lines.push(
            truncateToWidth(
                theme.fg(
                    "muted",
                    `  (${this.selectedIndex + 1}/${this.filteredNodes.length})${this.getStatusLabels()}`,
                ),
                width,
            ),
        );

        return lines;
    }

    /** Cursor + branch art + fold/path markers + label + entry text. */
    private renderRow(flatNode: FlatNode, isSelected: boolean): string {
        const entry = flatNode.node.entry;
        const cursor = isSelected ? theme.fg("accent", "› ") : "  ";
        const displayIndent = this.multipleRoots ? Math.max(0, flatNode.indent - 1) : flatNode.indent;

        const connector =
            flatNode.showConnector && !flatNode.isVirtualRootChild ? (flatNode.isLast ? "└─ " : "├─ ") : "";
        const connectorPosition = connector ? displayIndent - 1 : -1;

        const totalChars = displayIndent * 3;
        const prefixChars: string[] = [];
        const isFolded = this.foldedNodes.has(entry.id!);
        for (let c = 0; c < totalChars; c++) {
            const level = Math.floor(c / 3);
            const posInLevel = c % 3;

            const gutter = flatNode.gutters.find((g) => g.position === level);
            if (gutter) {
                prefixChars.push(posInLevel === 0 ? (gutter.show ? "│" : " ") : " ");
            } else if (connector && level === connectorPosition) {
                if (posInLevel === 0) {
                    prefixChars.push(flatNode.isLast ? "└" : "├");
                } else if (posInLevel === 1) {
                    const foldable = this.isFoldable(entry.id!);
                    prefixChars.push(isFolded ? "⊞" : foldable ? "⊟" : "─");
                } else {
                    prefixChars.push(" ");
                }
            } else {
                prefixChars.push(" ");
            }
        }
        const prefix = prefixChars.join("");

        const showsFoldInConnector = flatNode.showConnector && !flatNode.isVirtualRootChild;
        const foldMarker = isFolded && !showsFoldInConnector ? theme.fg("accent", "⊞ ") : "";
        const pathMarker = this.activePathIds.has(entry.id!) ? theme.fg("accent", "• ") : "";

        const label = flatNode.node.label ? theme.fg("warning", `[${flatNode.node.label}] `) : "";
        const labelTimestamp =
            this.showLabelTimestamps && flatNode.node.label && flatNode.node.labelTimestamp
                ? theme.fg("muted", `${formatLabelTimestamp(flatNode.node.labelTimestamp)} `)
                : "";
        const content = getEntryDisplayText(flatNode.node, isSelected, this.renderCtx);

        const line = cursor + theme.fg("dim", prefix) + foldMarker + pathMarker + label + labelTimestamp + content;
        return isSelected ? theme.bg("selectedBg", line) : line;
    }

    private setFilterMode(mode: FilterMode): void {
        this.filterMode = mode;
        this.foldedNodes.clear();
        this.applyFilter();
    }

    /** Toggle into a filter mode; selecting the active one returns to default. */
    private toggleFilterMode(mode: FilterMode): void {
        this.setFilterMode(this.filterMode === mode ? "default" : mode);
    }

    private cycleFilterMode(step: 1 | -1): void {
        const currentIndex = FILTER_MODES.indexOf(this.filterMode);
        this.setFilterMode(FILTER_MODES[(currentIndex + step + FILTER_MODES.length) % FILTER_MODES.length]);
    }

    handleInput(keyData: string): void {
        const kb = getKeybindings();
        if (kb.matches(keyData, "tui.select.up")) {
            this.selectedIndex = this.selectedIndex === 0 ? this.filteredNodes.length - 1 : this.selectedIndex - 1;
        } else if (kb.matches(keyData, "tui.select.down")) {
            this.selectedIndex = this.selectedIndex === this.filteredNodes.length - 1 ? 0 : this.selectedIndex + 1;
        } else if (kbMatches(keyData, "app.tree.foldOrUp")) {
            const currentId = this.filteredNodes[this.selectedIndex]?.node.entry.id;
            if (currentId && this.isFoldable(currentId) && !this.foldedNodes.has(currentId)) {
                this.foldedNodes.add(currentId);
                this.applyFilter();
            } else {
                this.selectedIndex = this.findBranchSegmentStart("up");
            }
        } else if (kbMatches(keyData, "app.tree.unfoldOrDown")) {
            const currentId = this.filteredNodes[this.selectedIndex]?.node.entry.id;
            if (currentId && this.foldedNodes.has(currentId)) {
                this.foldedNodes.delete(currentId);
                this.applyFilter();
            } else {
                this.selectedIndex = this.findBranchSegmentStart("down");
            }
        } else if (kb.matches(keyData, "tui.editor.cursorLeft") || kb.matches(keyData, "tui.select.pageUp")) {
            this.selectedIndex = Math.max(0, this.selectedIndex - this.maxVisibleLines);
        } else if (kb.matches(keyData, "tui.editor.cursorRight") || kb.matches(keyData, "tui.select.pageDown")) {
            this.selectedIndex = Math.min(this.filteredNodes.length - 1, this.selectedIndex + this.maxVisibleLines);
        } else if (kb.matches(keyData, "tui.select.confirm")) {
            const selected = this.filteredNodes[this.selectedIndex];
            if (selected && this.onSelect) this.onSelect(selected.node.entry.id!);
        } else if (kb.matches(keyData, "tui.select.cancel")) {
            if (this.searchQuery) {
                this.searchQuery = "";
                this.foldedNodes.clear();
                this.applyFilter();
            } else {
                this.onCancel?.();
            }
        } else if (kbMatches(keyData, "app.tree.filter.default")) {
            this.setFilterMode("default");
        } else if (kbMatches(keyData, "app.tree.filter.noTools")) {
            this.toggleFilterMode("no-tools");
        } else if (kbMatches(keyData, "app.tree.filter.userOnly")) {
            this.toggleFilterMode("user-only");
        } else if (kbMatches(keyData, "app.tree.filter.labeledOnly")) {
            this.toggleFilterMode("labeled-only");
        } else if (kbMatches(keyData, "app.tree.filter.all")) {
            this.toggleFilterMode("all");
        } else if (kbMatches(keyData, "app.tree.filter.cycleBackward")) {
            this.cycleFilterMode(-1);
        } else if (kbMatches(keyData, "app.tree.filter.cycleForward")) {
            this.cycleFilterMode(1);
        } else if (kb.matches(keyData, "tui.editor.deleteCharBackward")) {
            if (this.searchQuery.length > 0) {
                this.searchQuery = this.searchQuery.slice(0, -1);
                this.foldedNodes.clear();
                this.applyFilter();
            }
        } else if (kbMatches(keyData, "app.tree.editLabel")) {
            const selected = this.filteredNodes[this.selectedIndex];
            if (selected && this.onLabelEdit) {
                this.onLabelEdit(selected.node.entry.id!, selected.node.label);
            }
        } else if (kbMatches(keyData, "app.tree.toggleLabelTimestamp")) {
            this.showLabelTimestamps = !this.showLabelTimestamps;
        } else {
            const hasControlChars = [...keyData].some((ch) => {
                const code = ch.charCodeAt(0);
                return code < 32 || code === 0x7f || (code >= 0x80 && code <= 0x9f);
            });
            if (!hasControlChars && keyData.length > 0) {
                this.searchQuery += keyData;
                this.foldedNodes.clear();
                this.applyFilter();
            }
        }
    }

    /**
     * Whether a node can be folded. A node is foldable if it has visible children
     * and is either a root (no visible parent) or a segment start (visible parent
     * has multiple visible children).
     */
    private isFoldable(entryId: string): boolean {
        const children = this.visibleChildrenMap.get(entryId);
        if (!children || children.length === 0) return false;
        const parentId = this.visibleParentMap.get(entryId);
        if (parentId === null || parentId === undefined) return true;
        const siblings = this.visibleChildrenMap.get(parentId);
        return siblings !== undefined && siblings.length > 1;
    }

    /**
     * Find the index of the next branch segment start in the given direction.
     * A segment start is the first child of a branch point.
     */
    private findBranchSegmentStart(direction: "up" | "down"): number {
        const selectedId = this.filteredNodes[this.selectedIndex]?.node.entry.id;
        if (!selectedId) return this.selectedIndex;

        const indexByEntryId = new Map(this.filteredNodes.map((node, i) => [node.node.entry.id!, i]));
        let currentId: string = selectedId;
        if (direction === "down") {
            while (true) {
                const children: string[] = this.visibleChildrenMap.get(currentId) ?? [];
                if (children.length === 0) return indexByEntryId.get(currentId)!;
                if (children.length > 1) return indexByEntryId.get(children[0])!;
                currentId = children[0];
            }
        }

        // direction === "up"
        while (true) {
            const parentId: string | null = this.visibleParentMap.get(currentId) ?? null;
            if (parentId === null) return indexByEntryId.get(currentId)!;
            const children = this.visibleChildrenMap.get(parentId) ?? [];
            if (children.length > 1) {
                const segmentStart = indexByEntryId.get(currentId)!;
                if (segmentStart < this.selectedIndex) {
                    return segmentStart;
                }
            }
            currentId = parentId;
        }
    }
}
