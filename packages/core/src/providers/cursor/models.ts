// Static fallback catalog. Real list comes from `Cursor.models.list({ apiKey })`
// at runtime — refresh command lands in v2. IDs taken from a snapshot of
// cursor's catalog (mirrors fitchmultz/pi-cursor-sdk fallback snapshot).
//
// Context windows are best-effort defaults; cursor doesn't publish per-model
// numbers via the SDK list endpoint. Cost set to 0 because cursor bills via
// subscription / unified pricing, not per-token.

import type { ModelInfo } from "../../types";

type CursorModelEntry = Pick<ModelInfo, "contextWindow" | "maxOutput" | "reasoning">;

export const CURSOR_MODELS: Record<string, CursorModelEntry> = {
  // Cursor's in-house composer models
  "composer-2.5":      { contextWindow: 200_000, maxOutput: 16_000, reasoning: true  },
  "composer-2":        { contextWindow: 200_000, maxOutput: 16_000, reasoning: true  },

  // Anthropic via cursor
  "claude-opus-4-7":   { contextWindow: 200_000, maxOutput: 32_000, reasoning: true  },
  "claude-opus-4-6":   { contextWindow: 200_000, maxOutput: 32_000, reasoning: true  },
  "claude-opus-4-5":   { contextWindow: 200_000, maxOutput: 32_000, reasoning: true  },
  "claude-sonnet-4-6": { contextWindow: 200_000, maxOutput: 32_000, reasoning: true  },
  "claude-sonnet-4-5": { contextWindow: 200_000, maxOutput: 32_000, reasoning: true  },
  "claude-sonnet-4":   { contextWindow: 200_000, maxOutput: 32_000, reasoning: true  },
  "claude-haiku-4-5":  { contextWindow: 200_000, maxOutput: 16_000, reasoning: true  },

  // OpenAI via cursor
  "gpt-5.5":           { contextWindow: 400_000, maxOutput: 32_000, reasoning: true  },
  "gpt-5.4":           { contextWindow: 400_000, maxOutput: 32_000, reasoning: true  },
  "gpt-5.4-mini":      { contextWindow: 400_000, maxOutput: 16_000, reasoning: false },
  "gpt-5.4-nano":      { contextWindow: 200_000, maxOutput:  8_000, reasoning: false },
  "gpt-5.3-codex":     { contextWindow: 400_000, maxOutput: 32_000, reasoning: true  },
  "gpt-5.2":           { contextWindow: 400_000, maxOutput: 32_000, reasoning: true  },
  "gpt-5.2-codex":     { contextWindow: 400_000, maxOutput: 32_000, reasoning: true  },
  "gpt-5.1":           { contextWindow: 400_000, maxOutput: 32_000, reasoning: true  },
  "gpt-5.1-codex-max": { contextWindow: 400_000, maxOutput: 32_000, reasoning: true  },
  "gpt-5.1-codex-mini":{ contextWindow: 400_000, maxOutput: 16_000, reasoning: true  },
  "gpt-5-mini":        { contextWindow: 200_000, maxOutput: 16_000, reasoning: false },

  // Google via cursor
  "gemini-3.1-pro":    { contextWindow: 1_000_000, maxOutput: 64_000, reasoning: true  },
  "gemini-3.5-flash":  { contextWindow: 1_000_000, maxOutput: 32_000, reasoning: false },
  "gemini-3-flash":    { contextWindow: 1_000_000, maxOutput: 32_000, reasoning: false },
  "gemini-2.5-flash":  { contextWindow: 1_000_000, maxOutput: 32_000, reasoning: false },

  // xAI via cursor
  "grok-4.3":          { contextWindow: 1_000_000, maxOutput: 16_000, reasoning: true  },

  // Moonshot via cursor
  "kimi-k2.5":         { contextWindow: 200_000, maxOutput: 16_000, reasoning: false },
};

export const CURSOR_DEFAULT_MODEL = "composer-2.5";

export function buildCursorCatalog(): Record<string, ModelInfo> {
  const out: Record<string, ModelInfo> = {};
  for (const [id, entry] of Object.entries(CURSOR_MODELS)) {
    const fullId = `cursor/${id}`;
    out[fullId] = {
      id: fullId,
      provider: "cursor",
      name: id,
      contextWindow: entry.contextWindow,
      maxOutput: entry.maxOutput,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      reasoning: entry.reasoning,
      modalities: ["text"],
      available: true,
    };
  }
  return out;
}
