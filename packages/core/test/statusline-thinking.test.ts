import { describe, expect, test } from "bun:test";
import { LAYOUTS } from "../src/extensions/builtin/statusline-themes/layouts";
import type { StatusLineContext } from "../src/extensions/api";

const strip = (s: string) => s.replace(/\x1b\[[0-9;]*m/g, "");

const BASE: StatusLineContext = {
    agent: "default",
    modelId: "anthropic/claude-opus-4-8",
    provider: "anthropic",
    model: "claude-opus-4-8",
    sessionId: null,
    cwd: "/x",
    cost: { usd: 0.004, inputTokens: 3200, outputTokens: 98, cachedInputTokens: 21600 },
    context: { used: 11600, max: 200000 },
    thinking: "off",
    reasoning: false,
    width: 220,
};

const SYS = { cpu: 0.5, memUsed: 8e9 } as never;
const text = (ctx: StatusLineContext, render: NonNullable<(typeof LAYOUTS)[number]["render"]>) =>
    strip((render(ctx, SYS) ?? []).join("  "));

// Every layout that renders rows must obey the same rule: the thinking level
// shows only when the model reasons AND a non-off level is selected.
for (const layout of LAYOUTS) {
    if (!layout.render) continue; // native keeps the built-in render
    const render = layout.render;
    describe(`layout "${layout.id}" thinking gating`, () => {
        test("shows the level when the model reasons and thinking is on", () => {
            expect(text({ ...BASE, reasoning: true, thinking: "high" }, render)).toContain("high");
        });
        test("places thinking right after the model, ahead of the metrics", () => {
            const out = text({ ...BASE, reasoning: true, thinking: "high" }, render);
            // The model label carries no "%"; the first "%" belongs to a metric
            // (context/cache). Thinking must sit before it — i.e. next to the model.
            expect(out.indexOf("high")).toBeLessThan(out.indexOf("%"));
        });
        test("hides it when thinking is off", () => {
            expect(text({ ...BASE, reasoning: true, thinking: "off" }, render)).not.toContain("high");
        });
        test("hides it when the model does not support thinking (stale level)", () => {
            expect(text({ ...BASE, reasoning: false, thinking: "high" }, render)).not.toContain("high");
        });
    });
}
