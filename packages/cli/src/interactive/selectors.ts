import {
  Container,
  Editor,
  type EditorTheme,
  SelectList,
  type SelectItem,
  Text,
  TUI,
} from "@earendil-works/pi-tui";
import { DynamicBorder, getSelectListTheme } from "@earendil-works/pi-coding-agent";
import chalk from "chalk";

export function buildSelectorWrapper(
  items: SelectItem[],
  title: string | undefined,
  list: SelectList,
): Container {
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

export function selectOnce(
  host: SelectorHost,
  items: SelectItem[],
  title?: string,
): Promise<SelectItem | null> {
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

export function promptOnce(
  host: SelectorHost,
  editorTheme: EditorTheme,
  label?: string,
): Promise<string> {
  return new Promise((resolve) => {
    const tempEditor = new Editor(host.tui, editorTheme, { paddingX: 1 });
    const wrapper = new Container();
    if (label) wrapper.addChild(new Text(chalk.cyan(` ${label}`), 0, 0));
    wrapper.addChild(new DynamicBorder());
    wrapper.addChild(tempEditor);
    wrapper.addChild(new DynamicBorder());
    wrapper.addChild(new Text(chalk.dim(" Enter to submit · Esc to cancel"), 0, 0));
    const close = host.showSelector(wrapper, tempEditor as never);
    tempEditor.onSubmit = (text) => {
      close();
      resolve(text.trim());
    };
  });
}
