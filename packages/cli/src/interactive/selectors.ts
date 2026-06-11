import { Container, Editor, type EditorTheme, SelectList, type SelectItem, Text, TUI } from "@notshekhar/pi-tui";
import { DynamicBorder } from "./ui/messages";
import { getSelectListTheme } from "./ui/theme";
import chalk from "chalk";
import { isEsc } from "./keys";

export function buildSelectorWrapper(items: SelectItem[], title: string | undefined, list: SelectList): Container {
    const wrapper = new Container();
    if (title) wrapper.addChild(new Text(chalk.bold.cyan(` ${title}`), 0, 0));
    wrapper.addChild(new DynamicBorder());
    wrapper.addChild(list);
    wrapper.addChild(new DynamicBorder());
    wrapper.addChild(new Text(chalk.dim(" ↑↓ navigate · Enter select · Esc cancel"), 0, 0));
    return wrapper;
}

export interface SelectorHost {
    tui: TUI;
    showSelector: (component: Container, focusable: Container | SelectList) => () => void;
}

export function selectOnce(host: SelectorHost, items: SelectItem[], title?: string): Promise<SelectItem | null> {
    return new Promise((resolve) => {
        if (!items.length) {
            resolve(null);
            return;
        }
        const visible = Math.min(items.length, 10);
        const list = new SelectList(items, visible, getSelectListTheme());
        const wrapper = buildSelectorWrapper(items, title, list);
        const close = host.showSelector(wrapper, list);
        let done = false;
        const finish = (v: SelectItem | null) => {
            if (done) return;
            done = true;
            close();
            resolve(v);
        };
        list.onSelect = (item) => finish(item);
        list.onCancel = () => finish(null);
    });
}

/**
 * Multi-select with toggle semantics: Enter or Space flips the highlighted
 * entry in place (cursor stays put), "done" confirms, Esc cancels (null).
 * Items are mutated in place so the SelectList never resets its cursor.
 */
export function toggleSelectOnce(
    host: SelectorHost,
    values: string[],
    initial: Set<string>,
    title?: string,
): Promise<string[] | null> {
    return new Promise((resolve) => {
        if (!values.length) {
            resolve(null);
            return;
        }
        const selected = new Set(initial);
        const DONE = "__done__";
        const doneItem = (): SelectItem => ({
            value: DONE,
            label: `done (${selected.size}/${values.length})`,
            description:
                selected.size === values.length ? "all" : [...selected].join(", ") || "none — pick at least one",
        });
        const items: SelectItem[] = [
            doneItem(),
            ...values.map((v) => ({ value: v, label: `${selected.has(v) ? "[x]" : "[ ]"} ${v}`, description: "" })),
        ];
        const list = new SelectList(items, Math.min(items.length, 10), getSelectListTheme());

        const wrapper = new Container();
        if (title) wrapper.addChild(new Text(chalk.bold.cyan(` ${title}`), 0, 0));
        wrapper.addChild(new DynamicBorder());
        wrapper.addChild(list);
        wrapper.addChild(new DynamicBorder());
        wrapper.addChild(new Text(chalk.dim(" ↑↓ navigate · Enter/Space toggle · done confirms · Esc cancel"), 0, 0));
        const close = host.showSelector(wrapper, list);

        let current: SelectItem = items[0];
        list.onSelectionChange = (item) => (current = item);

        let done = false;
        const finish = (v: string[] | null) => {
            if (done) return;
            done = true;
            try {
                removeSpaceListener?.();
            } catch {}
            close();
            resolve(v);
        };

        const toggle = (item: SelectItem) => {
            if (item.value === DONE) return;
            if (selected.has(item.value)) selected.delete(item.value);
            else selected.add(item.value);
            // Mutate labels in place — the list keeps its cursor position.
            item.label = `${selected.has(item.value) ? "[x]" : "[ ]"} ${item.value}`;
            Object.assign(items[0], doneItem());
            host.tui.requestRender();
        };

        list.onSelect = (item) => {
            if (item.value === DONE) {
                if (selected.size === 0) return; // need at least one — keep open
                finish(values.filter((v) => selected.has(v)));
                return;
            }
            toggle(item);
        };
        list.onCancel = () => finish(null);

        // Space toggles too (Enter is handled by the list itself).
        const spaceListener = (data: string) => {
            if (data === " ") {
                toggle(current);
                return { consume: true };
            }
            return undefined;
        };
        let removeSpaceListener: (() => void) | undefined;
        const addInput = (
            host.tui as unknown as {
                addInputListener?: (cb: (d: string) => { consume: boolean } | undefined) => () => void;
            }
        ).addInputListener;
        if (typeof addInput === "function") {
            removeSpaceListener = addInput.call(host.tui, spaceListener);
        }
    });
}

export function promptOnce(
    host: SelectorHost,
    editorTheme: EditorTheme,
    label?: string,
    initial?: string,
): Promise<string> {
    return new Promise((resolve) => {
        const tempEditor = new Editor(host.tui, editorTheme, { paddingX: 1 });
        if (initial) tempEditor.setText(initial);
        const wrapper = new Container();
        if (label) wrapper.addChild(new Text(chalk.cyan(` ${label}`), 0, 0));
        wrapper.addChild(new DynamicBorder());
        wrapper.addChild(tempEditor);
        wrapper.addChild(new DynamicBorder());
        wrapper.addChild(new Text(chalk.dim(" Enter to submit · Shift+Enter newline · Esc to cancel"), 0, 0));
        const close = host.showSelector(wrapper, tempEditor as never);

        let done = false;
        const finish = (v: string) => {
            if (done) return;
            done = true;
            try {
                removeEscListener?.();
            } catch {}
            close();
            resolve(v);
        };

        // Editor doesn't expose its own onCancel; intercept Esc at the TUI level
        // while this prompt is showing. Listeners are LIFO so this fires before
        // the editor sees the key.
        const escListener = (data: string) => {
            if (isEsc(data)) {
                finish("");
                return { consume: true };
            }
            return undefined;
        };
        let removeEscListener: (() => void) | undefined;
        const addInput = (
            host.tui as unknown as {
                addInputListener?: (cb: (d: string) => { consume: boolean } | undefined) => () => void;
            }
        ).addInputListener;
        if (typeof addInput === "function") {
            removeEscListener = addInput.call(host.tui, escListener);
        }

        tempEditor.onSubmit = (text) => finish(text.trim());
    });
}
