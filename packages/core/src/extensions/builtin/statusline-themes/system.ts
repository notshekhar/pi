/**
 * A tiny background sampler for host vitals (CPU %, used memory, battery). The
 * status-line transform must be cheap and synchronous, so we never probe the OS
 * during a repaint — instead we refresh a cached snapshot on an interval and the
 * layout just reads the last value. Only started while a layout that needs it is
 * active; stopped (and the interval cleared) otherwise.
 */
import os from "node:os";
import { execFile } from "node:child_process";

export interface Vitals {
    /** 0..1 aggregate CPU utilization, or null until the first delta is known. */
    cpu: number | null;
    /** Used system memory in bytes (total − free). */
    memUsed: number;
    /** Total system memory in bytes. */
    memTotal: number;
    /** Battery 0..1, or null when unknown / no battery. */
    battery: number | null;
    /** True while charging / on AC. */
    charging: boolean;
}

interface CpuSample {
    idle: number;
    total: number;
}

function cpuSample(): CpuSample {
    let idle = 0;
    let total = 0;
    for (const c of os.cpus()) {
        for (const t of Object.values(c.times)) total += t;
        idle += c.times.idle;
    }
    return { idle, total };
}

export class SystemSampler {
    private timer: ReturnType<typeof setInterval> | null = null;
    private prev: CpuSample | null = null;
    private snapshot: Vitals = {
        cpu: null,
        memUsed: 0,
        memTotal: os.totalmem(),
        battery: null,
        charging: false,
    };

    /**
     * Idempotent: start the interval if not already running. `onTick` fires after
     * each sample (e.g. to repaint the status line so the clock/CPU stay live).
     */
    start(onTick?: () => void, intervalMs = 1000): void {
        if (this.timer) return;
        this.prev = cpuSample();
        this.tick(); // prime memory/battery immediately
        this.timer = setInterval(() => {
            this.tick();
            onTick?.();
        }, intervalMs);
        // Don't keep the process alive just for the sampler.
        this.timer.unref?.();
    }

    stop(): void {
        if (this.timer) clearInterval(this.timer);
        this.timer = null;
        this.prev = null;
    }

    get(): Vitals {
        return this.snapshot;
    }

    private tick(): void {
        // CPU: utilization across the interval = 1 − Δidle/Δtotal.
        const now = cpuSample();
        if (this.prev) {
            const dIdle = now.idle - this.prev.idle;
            const dTotal = now.total - this.prev.total;
            if (dTotal > 0) this.snapshot.cpu = Math.min(1, Math.max(0, 1 - dIdle / dTotal));
        }
        this.prev = now;

        this.snapshot.memTotal = os.totalmem();
        this.snapshot.memUsed = os.totalmem() - os.freemem();

        this.readBattery();
    }

    /** macOS-only (via pmset). Elsewhere battery stays null and layouts hide it. */
    private readBattery(): void {
        if (process.platform !== "darwin") return;
        execFile("pmset", ["-g", "batt"], { timeout: 1500 }, (err, stdout) => {
            if (err) return;
            const pct = stdout.match(/(\d+)%/);
            if (pct) this.snapshot.battery = Math.min(1, Number(pct[1]) / 100);
            this.snapshot.charging = /\b(charging|charged|AC Power)\b/i.test(stdout);
        });
    }
}
