import { createBashTool } from "./bash";
import { createEditTool } from "./edit";
import { createFindTool } from "./find";
import { createGrepTool } from "./grep";
import { createLsTool } from "./ls";
import { createReadTool } from "./read";
import { createWriteTool } from "./write";

export interface ToolContext {
  cwd: string;
  abortSignal?: AbortSignal;
  shellPath?: string;
  commandPrefix?: string;
}

export function createTools(ctx: ToolContext) {
  return {
    read: createReadTool(ctx),
    write: createWriteTool(ctx),
    edit: createEditTool(ctx),
    bash: createBashTool(ctx),
    ls: createLsTool(ctx),
    grep: createGrepTool(ctx),
    find: createFindTool(ctx),
  };
}

export type ToolSet = ReturnType<typeof createTools>;
export {
  createBashTool,
  createEditTool,
  createFindTool,
  createGrepTool,
  createLsTool,
  createReadTool,
  createWriteTool,
};
