import type { LanguageModel } from "ai";
import { createCursorLanguageModel } from "./language-model";

export { CURSOR_DEFAULT_MODEL, CURSOR_MODELS, buildCursorCatalog } from "./models";
export { mapCursorTool } from "./tool-mapping";

export function createCursor(opts: { apiKey: string }): (modelId: string) => LanguageModel {
  return (modelId) => createCursorLanguageModel({ apiKey: opts.apiKey, modelId }) as LanguageModel;
}
