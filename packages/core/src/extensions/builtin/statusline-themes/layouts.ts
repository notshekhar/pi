/**
 * Layout presets — the *structure* of the status line (what info appears and how
 * it's arranged), as opposed to themes which only recolor. A layout takes the
 * live status context plus cached system vitals and returns the fully rendered
 * rows (with their own colors). The "native" layout is special: it returns null
 * so the built-in two-row render is left untouched. Any active color theme then
 * recolors whatever the layout produced, so the two axes compose freely.
 */
import type { StatusLineContext } from "../../api";
import { ansiLen, bg, barCells, bold, COLORS, dim, fg, heat, type RGB } from "./ansi";
import type { Vitals } from "./system";

export type LayoutId = "native" | "compact" | "vitals" | "tokens" | "flex" | "powerline" | "minimal" | "bar";

export interface Layout {
    id: LayoutId;
    label: string;
    description: string;
    /** A short stripped sample for the picker menu. */
    sample: string;
    /** True if it reads CPU/mem — gates the background sampler. */
    needsVitals: boolean;
    /** Render the rows, or null to keep the native built-in render. */
    render: ((ctx: StatusLineContext, sys: Vitals) => string[] | null) | null;
}

// ── formatting ──────────────────────────────────────────────────────────────

function fmtTokens(n: number): string {
    if (n < 1000) return String(n);
    if (n < 10_000) return `${(n / 1000).toFixed(1)}k`;
    if (n < 1_000_000) return `${Math.round(n / 1000)}k`;
    if (n < 10_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
    return `${Math.round(n / 1_000_000)}M`;
}

function fmtBytes(n: number): string {
    const g = n / 1024 ** 3;
    if (g >= 1) return `${g.toFixed(1)}G`;
    return `${Math.round(n / 1024 ** 2)}M`;
}

function fmtClock(d = new Date()): string {
    const p = (x: number) => String(x).padStart(2, "0");
    return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/**
 * Turn a model id into a friendly label: "anthropic/claude-opus-4-5" → "Opus
 * 4.5", "fable-5" → "Fable 5". Falls back to the last path segment, de-dashed.
 */
export function prettyModel(ctx: StatusLineContext): string {
    const raw = (ctx.model || ctx.modelId || "no-model").split("/").pop() ?? "no-model";
    const claude = raw.match(/claude-(opus|sonnet|haiku)-(\d+)[-.](\d+)/i);
    if (claude) {
        const fam = claude[1][0].toUpperCase() + claude[1].slice(1).toLowerCase();
        return `${fam} ${claude[2]}.${claude[3]}`;
    }
    const fable = raw.match(/fable-(\d+)/i);
    if (fable) return `Fable ${fable[1]}`;
    // Generic: drop a leading vendor word, de-dash, title-case alpha words.
    return raw
        .replace(/^(claude|anthropic)-/i, "")
        .split(/[-_]/)
        .map((w) => (/^[a-z]+$/.test(w) ? w[0].toUpperCase() + w.slice(1) : w))
        .join(" ");
}

function ctxRatio(ctx: StatusLineContext): number {
    return ctx.context.max > 0 ? ctx.context.used / ctx.context.max : 0;
}

/**
 * One decimal of percent. Always shown to a fixed precision so a value parked on
 * an integer boundary (e.g. 2.5%) reads steadily instead of flickering between
 * its rounded neighbours as the token count jitters by a few.
 */
function pct1(r: number): string {
    return `${(r * 100).toFixed(1)}%`;
}

/** Real per-kind token counts from the cost snapshot. */
function tokenBreakdown(ctx: StatusLineContext) {
    const input = ctx.cost.inputTokens;
    const output = ctx.cost.outputTokens;
    const cached = ctx.cost.cachedInputTokens;
    const total = input + output + cached;
    // Share of input served from cache — our "cache hit" ratio.
    const hit = input + cached > 0 ? cached / (input + cached) : 0;
    return { input, output, cached, total, hit };
}

// ── shared pieces ─────────────────────────────────────────────────────────────

const SEP = dim(" │ ");

function model(ctx: StatusLineContext): string {
    return bold(fg(COLORS.cyan, prettyModel(ctx)));
}

/**
 * The selected agent as a compact `@name` chip — dim for the default agent,
 * highlighted when a custom/sub-agent is active so it stands out.
 */
function agentChip(ctx: StatusLineContext): string {
    const name = ctx.agent || "default";
    return name === "default" ? dim(`@${name}`) : fg(COLORS.orange, `@${name}`);
}

/** Thinking level as a colored chip, or null when the model isn't reasoning. */
function thinking(ctx: StatusLineContext): string | null {
    const v = thinkValue(ctx);
    return v ? fg(COLORS.magenta, v) : null;
}

/**
 * The thinking level as a bare value ("high"), or null when the model doesn't
 * reason or thinking is off — so non-reasoning models never show a stale level.
 */
function thinkValue(ctx: StatusLineContext): string | null {
    return ctx.reasoning && ctx.thinking && ctx.thinking !== "off" ? ctx.thinking : null;
}

function join(parts: Array<string | null>, sep = SEP): string {
    return parts.filter((p): p is string => !!p).join(sep);
}

/**
 * Responsive join: `parts` are in priority order (most important first). Keeps
 * the longest leading run that fits `width`, dropping lower-priority trailing
 * segments — so a narrow terminal sheds the least-important bits (mem, then …)
 * instead of hard-clipping the right edge mid-segment. The first
 * part is always kept (the component clips it if even that overflows).
 */
function fit(parts: Array<string | null>, width: number, sep = SEP): string {
    const items = parts.filter((p): p is string => !!p);
    const sepLen = ansiLen(sep);
    let out = "";
    let len = 0;
    for (const p of items) {
        const add = (out ? sepLen : 0) + ansiLen(p);
        if (out && len + add > width) break;
        out = out ? out + sep + p : p;
        len += add;
    }
    return out;
}

/**
 * Responsive wrap: flow `parts` across as many rows as needed so nothing is
 * dropped or clipped — when a segment won't fit the current row it starts the
 * next one. Keeps a dense dashboard (vitals/tokens) fully visible on a narrow
 * terminal by spilling onto extra rows instead of shedding segments.
 */
function wrap(parts: Array<string | null>, width: number, sep = SEP): string[] {
    const items = parts.filter((p): p is string => !!p);
    const sepLen = ansiLen(sep);
    const rows: string[] = [];
    let cur = "";
    let len = 0;
    for (const p of items) {
        const pLen = ansiLen(p);
        if (cur === "") {
            cur = p;
            len = pLen;
        } else if (len + sepLen + pLen <= width) {
            cur += sep + p;
            len += sepLen + pLen;
        } else {
            rows.push(cur);
            cur = p;
            len = pLen;
        }
    }
    if (cur) rows.push(cur);
    return rows;
}

// Dark ink for text painted on a colored powerline background.
const INK: RGB = { r: 20, g: 20, b: 20 };

/**
 * Render one powerline strip: colored blocks joined by  arrow separators (the
 * arrow is a Nerd Font glyph). Each block's text is dark ink on its bg color.
 * Trailing blocks that wouldn't fit `width` are dropped (at least one is kept),
 * so the strip never runs off a narrow terminal.
 */
function powerline(segs: Array<{ text: string; bg: RGB }>, width = Infinity): string {
    const kept: Array<{ text: string; bg: RGB }> = [];
    let used = 0;
    for (const s of segs) {
        const cols = s.text.length + 2 + 1; // " text " padding + 1-col arrow
        if (kept.length > 0 && used + cols > width) break;
        kept.push(s);
        used += cols;
    }
    let out = "";
    for (let i = 0; i < kept.length; i++) {
        const s = kept[i];
        out += bg(s.bg, fg(INK, ` ${s.text} `));
        const next = kept[i + 1];
        // Arrow: this block's bg as fg, painted over the next block's bg.
        out += next ? bg(next.bg, fg(s.bg, "")) : fg(s.bg, "");
    }
    return out;
}

// ── layouts ───────────────────────────────────────────────────────────────────

export const LAYOUTS: Layout[] = [
    {
        id: "native",
        label: "native",
        description: "the built-in two-row status line (agent/model · session/cost/ctx)",
        sample: "agent default · Opus 4.8  /  session a1b2 · $0.00 · ctx 12k/200k",
        needsVitals: false,
        render: null,
    },
    {
        id: "compact",
        label: "compact",
        description: "agent · model · thinking · context bar · percent · tokens, on one row (Claude-Code style)",
        sample: "@plan │ Opus 4.8 │ high │ [██████░░░░] 30.5% │ 61k/200k tokens",
        needsVitals: false,
        render: (ctx) => {
            const r = ctxRatio(ctx);
            const { filled, empty } = barCells(r, 16);
            const bar = `${dim("[")}${fg(heat(r), filled)}${dim(empty)}${dim("]")}`;
            const pct = fg(heat(r), pct1(r));
            const toks = ctx.context.max
                ? dim(`${fmtTokens(ctx.context.used)}/${fmtTokens(ctx.context.max)} tokens`)
                : dim(`${fmtTokens(ctx.context.used)} tokens`);
            return [fit([agentChip(ctx), model(ctx), thinking(ctx), pct, bar, toks], ctx.width)];
        },
    },
    {
        id: "vitals",
        label: "vitals",
        description:
            "agent · model · thinking · ctx% · tokens · cached · hit% · cost · clock · cpu · mem — the dashboard",
        sample: "@plan │ Opus 4.8 │ high │ 19.0% ctx │ 11.6k tok │ cached 21.6k │ hit 87% │ $0.0042 │ 16:17:06 │ cpu:100% mem:29.3G",
        needsVitals: true,
        render: (ctx, sys) => {
            const r = ctxRatio(ctx);
            const think = thinkValue(ctx);
            const t = tokenBreakdown(ctx);
            const pct = fg(heat(r), `${pct1(r)} ctx`);
            const tok = fg(COLORS.green, `${fmtTokens(ctx.context.used)} tok`);
            // Always shown (even at zero on a fresh session) so the dashboard is complete.
            const cached = fg(COLORS.blue, `cached ${fmtTokens(t.cached)}`);
            const hit = fg(COLORS.orange, `hit ${(t.hit * 100).toFixed(0)}%`);
            const cost = fg(COLORS.yellow, `$${ctx.cost.usd.toFixed(4)}`);
            const clock = fg(COLORS.magenta, fmtClock());
            const cpu = sys.cpu == null ? null : fg(heat(sys.cpu), `cpu:${Math.round(sys.cpu * 100)}%`);
            const mem = fg(COLORS.muted, `mem:${fmtBytes(sys.memUsed)}`);
            const vitals = join([cpu, mem], " ");
            // Wrap onto extra rows when narrow so nothing is dropped — the whole
            // dashboard (incl. cpu/mem) stays visible, just on more lines.
            return wrap(
                [
                    agentChip(ctx),
                    model(ctx),
                    think ? fg(COLORS.magenta, think) : null,
                    pct,
                    tok,
                    cached,
                    hit,
                    cost,
                    clock,
                    vitals || null,
                ],
                ctx.width,
            );
        },
    },
    {
        id: "tokens",
        label: "tokens",
        description: "agent · model · token economics — in · out · cached · total · cache-hit% · cost",
        sample: "@plan │ Opus 4.8 │ high │ in 3.2k · out 98 · cached 21.6k │ total 24.9k │ hit 87% │ $0.0042",
        needsVitals: false,
        render: (ctx) => {
            const t = tokenBreakdown(ctx);
            const breakdown =
                fg(COLORS.blue, `in ${fmtTokens(t.input)}`) +
                dim(" · ") +
                fg(COLORS.magenta, `out ${fmtTokens(t.output)}`) +
                dim(" · ") +
                fg(COLORS.green, `cached ${fmtTokens(t.cached)}`);
            return wrap(
                [
                    agentChip(ctx),
                    model(ctx),
                    thinking(ctx),
                    breakdown,
                    dim(`total ${fmtTokens(t.total)}`),
                    fg(COLORS.orange, `hit ${(t.hit * 100).toFixed(0)}%`),
                    fg(COLORS.yellow, `$${ctx.cost.usd.toFixed(4)}`),
                ],
                ctx.width,
            );
        },
    },
    {
        id: "flex",
        label: "flex",
        description:
            "three-row powerline dashboard: agent/model/ctx/thinking, token breakdown, cache/cost (needs a Nerd Font)",
        sample: " Agent: plan  Model: Opus 4.8  Thinking: high  Ctx: 30.5%  /  In  Out  Cached  Total  /  Cache  Cost ",
        needsVitals: false,
        render: (ctx) => {
            const r = ctxRatio(ctx);
            const t = tokenBreakdown(ctx);
            const think = thinkValue(ctx);
            return [
                powerline(
                    [
                        { text: `Agent: ${ctx.agent || "default"}`, bg: COLORS.faint },
                        { text: `Model: ${prettyModel(ctx)}`, bg: COLORS.red },
                        // Right after the model, and only when the model reasons.
                        ...(think ? [{ text: `Thinking: ${think}`, bg: COLORS.green }] : []),
                        { text: `Ctx: ${pct1(r)}`, bg: heat(r) },
                    ],
                    ctx.width,
                ),
                powerline(
                    [
                        { text: `In: ${fmtTokens(t.input)}`, bg: COLORS.red },
                        { text: `Out: ${fmtTokens(t.output)}`, bg: COLORS.blue },
                        { text: `Cached: ${fmtTokens(t.cached)}`, bg: COLORS.green },
                        { text: `Total: ${fmtTokens(t.total)}`, bg: COLORS.faint },
                    ],
                    ctx.width,
                ),
                powerline(
                    [
                        { text: `Cache: ${(t.hit * 100).toFixed(1)}%`, bg: COLORS.orange },
                        { text: `Cost: $${ctx.cost.usd.toFixed(4)}`, bg: COLORS.green },
                    ],
                    ctx.width,
                ),
            ];
        },
    },
    {
        id: "powerline",
        label: "powerline",
        description: "one row of colored blocks with arrow separators (needs a Nerd Font)",
        sample: " @plan  Opus 4.8  high  30.5% ctx  61k tok  $0.0042 ",
        needsVitals: false,
        render: (ctx) => {
            const r = ctxRatio(ctx);
            const think = thinkValue(ctx);
            const agent = ctx.agent || "default";
            return [
                powerline(
                    [
                        { text: `@${agent}`, bg: agent === "default" ? COLORS.faint : COLORS.orange },
                        { text: prettyModel(ctx), bg: COLORS.cyan },
                        ...(think ? [{ text: think, bg: COLORS.magenta }] : []),
                        { text: `${pct1(r)} ctx`, bg: heat(r) },
                        { text: `${fmtTokens(ctx.context.used)} tok`, bg: COLORS.faint },
                        { text: `$${ctx.cost.usd.toFixed(4)}`, bg: COLORS.green },
                    ],
                    ctx.width,
                ),
            ];
        },
    },
    {
        id: "minimal",
        label: "minimal",
        description: "just the agent, model, context percent, and thinking — stay out of the way",
        sample: "@plan · Opus 4.8 · high · 30.5%",
        needsVitals: false,
        render: (ctx) => {
            const r = ctxRatio(ctx);
            return [fit([agentChip(ctx), model(ctx), thinking(ctx), fg(heat(r), pct1(r))], ctx.width, dim(" · "))];
        },
    },
    {
        id: "bar",
        label: "bar",
        description: "a wide context bar with agent, thinking, tokens and cost alongside the model",
        sample: "@plan Opus 4.8 high  [████████████░░░░░░░░░░] 30.5%  61k/200k · $0.0042",
        needsVitals: false,
        render: (ctx) => {
            const r = ctxRatio(ctx);
            const barWidth = Math.max(10, Math.min(40, ctx.width - 34));
            const { filled, empty } = barCells(r, barWidth);
            const bar = `${dim("[")}${fg(heat(r), filled)}${dim(empty)}${dim("]")}`;
            const pct = fg(heat(r), pct1(r));
            const toks = ctx.context.max
                ? `${fmtTokens(ctx.context.used)}/${fmtTokens(ctx.context.max)}`
                : fmtTokens(ctx.context.used);
            const tail = join([dim(toks), fg(COLORS.green, `$${ctx.cost.usd.toFixed(4)}`)], dim(" · "));
            // Thinking sits next to the model (only when the model reasons & it's on).
            const head = join([agentChip(ctx), model(ctx), thinking(ctx)], " ");
            return [`${head}  ${bar} ${pct}  ${tail}`];
        },
    },
];

export const DEFAULT_LAYOUT: LayoutId = "native";

export function getLayout(id: string | undefined): Layout {
    return LAYOUTS.find((l) => l.id === id) ?? LAYOUTS[0];
}
