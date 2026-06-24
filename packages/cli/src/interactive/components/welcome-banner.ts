import { type Component, type TUI, visibleWidth } from "@notshekhar/loop-tui";
import chalk from "chalk";

/**
 * Startup welcome banner — loop's answer to Claude Code's masthead, minus the
 * border box. A pixelated "loop" ring sits on the left with a comet of light
 * chasing its way around (the animation), identity + tips to the right.
 *
 * The ring spins for a couple of rotations on appearance, then settles to a
 * static glow and stops its timer so it never churns renders once it has
 * scrolled up into the terminal's scrollback.
 */

export interface WelcomeBannerInfo {
    /** Greeting name (OS username). */
    name: string;
    /** "provider/model" id, or empty when no model is selected. */
    model: string;
    /** Session id or "unsaved". */
    session: string;
    /** Non-default agent name, or null. */
    agent: string | null;
    /** Working directory (already ~-shortened). */
    cwd: string;
    /** loop version, if known. */
    version?: string;
}

// 4×4 pixel ring (each cell renders as a 2-wide block so it reads square in a
// terminal). Full square perimeter — corners filled — with a hollow centre so
// the settled shape clearly reads as a loop. The animation walks a bright
// "head" clockwise around the perimeter with a fading tail behind it: a loop,
// going in a circle.
const RING: ReadonlyArray<readonly [number, number]> = [
    [0, 0],
    [0, 1],
    [0, 2],
    [0, 3],
    [1, 3],
    [2, 3],
    [3, 3],
    [3, 2],
    [3, 1],
    [3, 0],
    [2, 0],
    [1, 0],
];

const GRID_SIZE = 4;
const PIXEL = "██";
const GAP = "  ";
const FRAME_MS = 90;
const ROTATIONS = 2; // spin twice, then settle

// Comet palette: head brightest, two trailing pixels dimmer, rest a faint glow.
const HEAD = chalk.hex("#d6fff7").bold;
const TRAIL1 = chalk.hex("#8abeb7").bold;
const TRAIL2 = chalk.hex("#5e8e88").bold;
const REST = chalk.hex("#3a4f4c").bold;
const SETTLED = chalk.hex("#8abeb7").bold;

const ORANGE = chalk.hex("#e09956");

export class WelcomeBanner implements Component {
    private frame = 0;
    private timer: NodeJS.Timeout | null = null;
    private settled = false;
    private cachedWidth?: number;
    private cachedLines?: string[];
    /** Optional "update available" line, set asynchronously after the network check. */
    private updateNotice?: string;

    constructor(
        private readonly tui: TUI,
        private readonly info: WelcomeBannerInfo,
    ) {}

    /** Show (or clear) the update-available line under the masthead and repaint. */
    setUpdateNotice(text: string | undefined): void {
        this.updateNotice = text;
        this.cachedLines = undefined;
        this.tui.requestRender();
    }

    start(): void {
        if (this.timer) return;
        this.timer = setInterval(() => {
            this.frame++;
            this.cachedLines = undefined;
            // After ROTATIONS full loops, freeze on the settled ring.
            if (this.frame >= RING.length * ROTATIONS) {
                this.settled = true;
                this.stop();
            }
            this.tui.requestRender();
        }, FRAME_MS);
    }

    stop(): void {
        if (this.timer) {
            clearInterval(this.timer);
            this.timer = null;
        }
    }

    invalidate(): void {
        this.cachedLines = undefined;
        this.cachedWidth = undefined;
    }

    /** Color a ring pixel by its distance behind the comet head. */
    private pixel(index: number): string {
        if (this.settled) return SETTLED(PIXEL);
        const head = this.frame % RING.length;
        const dist = (head - index + RING.length) % RING.length;
        if (dist === 0) return HEAD(PIXEL);
        if (dist === 1) return TRAIL1(PIXEL);
        if (dist === 2) return TRAIL2(PIXEL);
        return REST(PIXEL);
    }

    /** The 5 rendered icon rows (10 visible cols each). */
    private iconRows(): string[] {
        const rows: string[] = [];
        for (let r = 0; r < GRID_SIZE; r++) {
            let row = "";
            for (let c = 0; c < GRID_SIZE; c++) {
                const ringIndex = RING.findIndex(([rr, cc]) => rr === r && cc === c);
                row += ringIndex >= 0 ? this.pixel(ringIndex) : "  ";
            }
            rows.push(row);
        }
        return rows;
    }

    render(width: number): string[] {
        if (this.cachedLines && this.cachedWidth === width) return this.cachedLines;

        const icon = this.iconRows();

        const modelLabel = this.info.model || chalk.yellow("no model — run /login or /provider");
        const sessionLabel = this.info.session === "unsaved" ? chalk.dim("unsaved") : this.info.session;
        const idLine =
            chalk.bold("loop") +
            chalk.dim(" · ") +
            modelLabel +
            chalk.dim(" · ") +
            sessionLabel +
            (this.info.agent ? chalk.dim(" · agent ") + this.info.agent : "");

        // One text row beside each of the icon's 4 rows.
        const textRows: string[] = [
            chalk.bold(`Welcome to loop, ${this.info.name}!`) +
                (this.info.version ? chalk.dim(`  v${this.info.version}`) : ""),
            idLine,
            chalk.dim(this.info.cwd),
            ORANGE("Tips") + chalk.dim("  /help · Shift+Tab cycles agents · Ctrl+C twice to quit"),
        ];

        const lines: string[] = [""];
        for (let i = 0; i < GRID_SIZE; i++) {
            const text = textRows[i] ?? "";
            // " " left margin + icon + gap + text
            lines.push(` ${icon[i]}${GAP}${text}`);
        }
        // Update-available line, aligned under the text column (no icon beside it).
        if (this.updateNotice) {
            const indent = " " + " ".repeat(GRID_SIZE * 2) + GAP;
            lines.push(indent + chalk.yellow(this.updateNotice));
        }
        lines.push("");

        // Pad each line to full width so the differential renderer overwrites
        // cleanly (no stale trailing chars from a previous longer line).
        const padded = lines.map((l) => {
            const pad = Math.max(0, width - visibleWidth(l));
            return l + " ".repeat(pad);
        });

        this.cachedLines = padded;
        this.cachedWidth = width;
        return padded;
    }
}
