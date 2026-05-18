import type {
  Container,
  Editor,
  SelectItem,
  SelectList,
  TUI,
} from "@earendil-works/pi-tui";
import type { CommandRegistry, CostTracker, Session, SessionManager, UsageBlock } from "@notshekhar/pi-core";
import type { ChatHistory } from "./components/chat-history";
import type { CostFooter } from "./components/cost-footer";

/**
 * Stable references for handlers. Functions and objects here don't change
 * across the app's lifetime — only the AppState fields mutate.
 */
export interface AppDeps {
  tui: TUI;
  history: ChatHistory;
  footer: CostFooter;
  tracker: CostTracker;
  editor: Editor;
  commands: CommandRegistry;
  manager: SessionManager;
  queuedMessages: string[];
  refreshFooter: (usage?: UsageBlock) => void;
  refreshFooterCtx: (usage?: UsageBlock) => void;
  renderPending: () => void;
  showWorking: (msg?: string) => void;
  hideWorking: () => void;
  showSelector: (component: Container, focusable: Container | SelectList) => () => void;
  selectOnce: (items: SelectItem[], title?: string) => Promise<SelectItem | null>;
  promptOnce: (label?: string) => Promise<string>;
  resolveModelId: (input: string) => Promise<string | null>;
  ensureSession: () => Promise<Session>;
  cleanExit: (code?: number) => void;
}
