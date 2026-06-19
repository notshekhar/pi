/**
 * Tree layout algorithms for the /tree selector — pure functions, no UI
 * state (flattenTree / recalculateVisualStructure).
 *
 * Indentation rules:
 * - At indent 0: stay at 0 unless parent has >1 children (then +1)
 * - At indent 1: children always go to indent 2 (visual grouping of subtree)
 * - At indent 2+: stay flat for single-child chains, +1 only if parent branches
 */
import type { SessionTreeNode } from "@notshekhar/loop-core";

/** Gutter info: position (displayIndent where connector was) and whether to show │ */
export interface GutterInfo {
    position: number;
    show: boolean;
}

/** Flattened tree node for navigation */
export interface FlatNode {
    node: SessionTreeNode;
    /** Indentation level (each level = 3 chars) */
    indent: number;
    /** Whether to show connector (├─ or └─) - true if parent has multiple children */
    showConnector: boolean;
    /** If showConnector, true = last sibling (└─), false = not last (├─) */
    isLast: boolean;
    /** Gutter info for each ancestor branch point */
    gutters: GutterInfo[];
    /** True if this node is a root under a virtual branching root (multiple roots) */
    isVirtualRootChild: boolean;
}

/** Stack frame shared by both DFS passes. */
type StackItem<T> = [T, number, boolean, boolean, boolean, GutterInfo[], boolean];

function childLayout(
    indent: number,
    justBranched: boolean,
    multipleChildren: boolean,
    showConnector: boolean,
    isVirtualRootChild: boolean,
    isLast: boolean,
    gutters: GutterInfo[],
    multipleRoots: boolean,
): { childIndent: number; childGutters: GutterInfo[] } {
    let childIndent: number;
    if (multipleChildren) {
        // Parent branches: children get +1
        childIndent = indent + 1;
    } else if (justBranched && indent > 0) {
        // First generation after a branch: +1 for visual grouping
        childIndent = indent + 1;
    } else {
        // Single-child chain: stay flat
        childIndent = indent;
    }

    // If this node showed a connector, descendants get a gutter at its position.
    const connectorDisplayed = showConnector && !isVirtualRootChild;
    const currentDisplayIndent = multipleRoots ? Math.max(0, indent - 1) : indent;
    const connectorPosition = Math.max(0, currentDisplayIndent - 1);
    const childGutters: GutterInfo[] = connectorDisplayed
        ? [...gutters, { position: connectorPosition, show: !isLast }]
        : gutters;

    return { childIndent, childGutters };
}

/** Mark every subtree that contains the active leaf (active branch sorts first). */
function buildContainsActive(roots: SessionTreeNode[], leafId: string | null): Map<SessionTreeNode, boolean> {
    const containsActive = new Map<SessionTreeNode, boolean>();
    // Build list in pre-order, then process in reverse for post-order effect
    // (children before parents) without recursion.
    const allNodes: SessionTreeNode[] = [];
    const preOrderStack: SessionTreeNode[] = [...roots];
    while (preOrderStack.length > 0) {
        const node = preOrderStack.pop()!;
        allNodes.push(node);
        for (let i = node.children.length - 1; i >= 0; i--) {
            preOrderStack.push(node.children[i]);
        }
    }
    for (let i = allNodes.length - 1; i >= 0; i--) {
        const node = allNodes[i];
        let has = leafId !== null && node.entry.id === leafId;
        for (const child of node.children) {
            if (containsActive.get(child)) has = true;
        }
        containsActive.set(node, has);
    }
    return containsActive;
}

/** Flatten the tree depth-first, active branch first. */
export function flattenTree(
    roots: SessionTreeNode[],
    currentLeafId: string | null,
): { flatNodes: FlatNode[]; multipleRoots: boolean } {
    const result: FlatNode[] = [];
    const multipleRoots = roots.length > 1;
    const containsActive = buildContainsActive(roots, currentLeafId);

    // Multiple roots are treated as children of a virtual branching root.
    const stack: StackItem<SessionTreeNode>[] = [];
    const orderedRoots = [...roots].sort((a, b) => Number(containsActive.get(b)) - Number(containsActive.get(a)));
    for (let i = orderedRoots.length - 1; i >= 0; i--) {
        const isLast = i === orderedRoots.length - 1;
        stack.push([orderedRoots[i], multipleRoots ? 1 : 0, multipleRoots, multipleRoots, isLast, [], multipleRoots]);
    }

    while (stack.length > 0) {
        const [node, indent, justBranched, showConnector, isLast, gutters, isVirtualRootChild] = stack.pop()!;

        result.push({ node, indent, showConnector, isLast, gutters, isVirtualRootChild });

        const children = node.children;
        const multipleChildren = children.length > 1;

        // Branch containing the active leaf comes first.
        const prioritized: SessionTreeNode[] = [];
        const rest: SessionTreeNode[] = [];
        for (const child of children) {
            if (containsActive.get(child)) prioritized.push(child);
            else rest.push(child);
        }
        const orderedChildren = [...prioritized, ...rest];

        const { childIndent, childGutters } = childLayout(
            indent,
            justBranched,
            multipleChildren,
            showConnector,
            isVirtualRootChild,
            isLast,
            gutters,
            multipleRoots,
        );

        for (let i = orderedChildren.length - 1; i >= 0; i--) {
            const childIsLast = i === orderedChildren.length - 1;
            stack.push([
                orderedChildren[i],
                childIndent,
                multipleChildren,
                multipleChildren,
                childIsLast,
                childGutters,
                false,
            ]);
        }
    }

    return { flatNodes: result, multipleRoots };
}

export interface VisibleStructure {
    visibleParent: Map<string, string | null>;
    visibleChildren: Map<string | null, string[]>;
    multipleRoots: boolean;
}

/**
 * Recompute indentation/connectors for a filtered view; mutates the visual
 * fields of the filtered FlatNodes in place.
 *
 * Filtering can hide intermediate entries; descendants attach to the nearest
 * visible ancestor. Indentation semantics stay aligned with flattenTree() so
 * single-child chains don't drift right.
 */
export function recalculateVisualStructure(flatNodes: FlatNode[], filteredNodes: FlatNode[]): VisibleStructure {
    const visibleParent = new Map<string, string | null>();
    const visibleChildren = new Map<string | null, string[]>();
    visibleChildren.set(null, []);
    if (filteredNodes.length === 0) {
        return { visibleParent, visibleChildren, multipleRoots: false };
    }

    const visibleIds = new Set(filteredNodes.map((n) => n.node.entry.id!));

    const entryMap = new Map<string, FlatNode>();
    for (const flatNode of flatNodes) {
        entryMap.set(flatNode.node.entry.id!, flatNode);
    }

    const findVisibleAncestor = (nodeId: string): string | null => {
        let currentId = entryMap.get(nodeId)?.node.entry.parentId ?? null;
        while (currentId !== null) {
            if (visibleIds.has(currentId)) return currentId;
            currentId = entryMap.get(currentId)?.node.entry.parentId ?? null;
        }
        return null;
    };

    for (const flatNode of filteredNodes) {
        const nodeId = flatNode.node.entry.id!;
        const ancestorId = findVisibleAncestor(nodeId);
        visibleParent.set(nodeId, ancestorId);
        if (!visibleChildren.has(ancestorId)) visibleChildren.set(ancestorId, []);
        visibleChildren.get(ancestorId)!.push(nodeId);
    }

    const visibleRootIds = visibleChildren.get(null)!;
    const multipleRoots = visibleRootIds.length > 1;

    const filteredNodeMap = new Map<string, FlatNode>();
    for (const flatNode of filteredNodes) {
        filteredNodeMap.set(flatNode.node.entry.id!, flatNode);
    }

    const stack: StackItem<string>[] = [];
    for (let i = visibleRootIds.length - 1; i >= 0; i--) {
        const isLast = i === visibleRootIds.length - 1;
        stack.push([visibleRootIds[i], multipleRoots ? 1 : 0, multipleRoots, multipleRoots, isLast, [], multipleRoots]);
    }

    while (stack.length > 0) {
        const [nodeId, indent, justBranched, showConnector, isLast, gutters, isVirtualRootChild] = stack.pop()!;

        const flatNode = filteredNodeMap.get(nodeId);
        if (!flatNode) continue;

        flatNode.indent = indent;
        flatNode.showConnector = showConnector;
        flatNode.isLast = isLast;
        flatNode.gutters = gutters;
        flatNode.isVirtualRootChild = isVirtualRootChild;

        const children = visibleChildren.get(nodeId) || [];
        const multipleChildren = children.length > 1;

        const { childIndent, childGutters } = childLayout(
            indent,
            justBranched,
            multipleChildren,
            showConnector,
            isVirtualRootChild,
            isLast,
            gutters,
            multipleRoots,
        );

        for (let i = children.length - 1; i >= 0; i--) {
            const childIsLast = i === children.length - 1;
            stack.push([
                children[i],
                childIndent,
                multipleChildren,
                multipleChildren,
                childIsLast,
                childGutters,
                false,
            ]);
        }
    }

    return { visibleParent, visibleChildren, multipleRoots };
}
