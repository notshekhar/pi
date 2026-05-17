import { costStore } from "../auth/storage";
import { getModelSync } from "../catalog";
import type { CostBreakdown, ProviderId, UsageBlock } from "../types";
import { parseModelId } from "../providers";

export class CostTracker {
  private session: CostBreakdown = { inputTokens: 0, outputTokens: 0, cachedInputTokens: 0, usd: 0 };

  add(modelId: string, usage: UsageBlock): CostBreakdown {
    const { provider } = parseModelId(modelId);
    const inTok = usage.inputTokens ?? 0;
    const outTok = usage.outputTokens ?? 0;
    const cacheTok = usage.cachedInputTokens ?? 0;

    let usd: number;
    if (typeof usage.cost === "number" && provider === "openrouter") {
      usd = usage.cost;
    } else {
      const model = getModelSync(modelId);
      if (!model) {
        usd = 0;
      } else {
        const billedIn = Math.max(0, inTok - cacheTok);
        usd =
          (billedIn / 1_000_000) * model.cost.input +
          (outTok / 1_000_000) * model.cost.output +
          (cacheTok / 1_000_000) * model.cost.cacheRead;
      }
    }

    this.session.inputTokens += inTok;
    this.session.outputTokens += outTok;
    this.session.cachedInputTokens += cacheTok;
    this.session.usd += usd;

    const lifetime = costStore.get("lifetime") as { usd: number; byProvider: Record<string, number> };
    lifetime.usd = (lifetime.usd ?? 0) + usd;
    lifetime.byProvider[provider] = (lifetime.byProvider[provider] ?? 0) + usd;
    costStore.set("lifetime", lifetime);

    return { ...this.session };
  }

  sessionBreakdown(): CostBreakdown {
    return { ...this.session };
  }

  lifetimeBreakdown(): { usd: number; byProvider: Record<ProviderId, number> } {
    return costStore.get("lifetime") as { usd: number; byProvider: Record<ProviderId, number> };
  }

  format(): string {
    const s = this.session;
    return `$${s.usd.toFixed(4)} · in:${s.inputTokens} out:${s.outputTokens} cache:${s.cachedInputTokens}`;
  }
}
