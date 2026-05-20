// Map cursor SDK internal tool names to pi's tool taxonomy so the tool
// renderer (pi-coding-agent ToolExecutionComponent) draws them correctly.
// Inspired by pi-cursor-sdk's "native tool display".
//
// Tools are tagged with providerExecuted: true downstream — the agent loop
// must NOT re-execute them (cursor already did the work).

export type CursorToolName =
  | "read"
  | "edit"
  | "write"
  | "shell"
  | "ls"
  | "grep"
  | "glob"
  | "delete"
  | "sem_search"
  | "task"
  | "mcp"
  | "create_plan"
  | "update_todos"
  | "read_lints"
  | string;

export function mapCursorTool(cursorTool: CursorToolName): string {
  switch (cursorTool) {
    case "read": return "read";
    case "edit": return "edit";
    case "write": return "write";
    case "shell": return "bash";
    case "ls": return "ls";
    case "grep": return "grep";
    case "glob": return "glob";
    case "delete": return "delete";
    case "sem_search": return "sem_search";
    case "task": return "task";
    case "mcp": return "mcp";
    case "create_plan": return "plan";
    case "update_todos": return "todos";
    case "read_lints": return "lints";
    default: return cursorTool;
  }
}
