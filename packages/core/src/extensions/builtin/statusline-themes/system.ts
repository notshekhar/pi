/**
 * A tiny background sampler for host vitals (CPU %, used memory). The status-line
 * transform must be cheap and synchronous, so we never probe the OS during a
 * repaint — instead we refresh a cached snapshot on an interval and the layout
 * just reads the last value. Only started while a layout that needs it is active;
 * stopped (and the interval cleared) otherwise. Everything sampled here is an
 * in-process read (os.*) — we deliberately never spawn a subprocess per tick,
 * which under Bun would inflate the allocator's high-water mark (RSS that never
 * returns to the OS).
 */
import os from "node:os";

export interface Vitals {
    /** 0..1 aggregate CPU utilization, or null until the first delta is known. */
    cpu: number | null;
    /** Used system memory in bytes (total − free). */
    memUsed: number;
    /** Total system memory in bytes. */
    memTotal: number;
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
    };

    /**
     * Idempotent: start the interval if not already running. `onTick` fires after
     * each sample (e.g. to repaint the status line so the clock/CPU stay live).
     */
    start(onTick?: () => void, intervalMs = 1000): void {
        if (this.timer) return;
        this.prev = cpuSample();
        this.tick(); // prime memory immediately
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
    }
}
