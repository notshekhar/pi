/**
 * Install / link / remove extensions. Dependency resolution rides the Bun
 * runtime already inside the loop binary: we re-exec the binary itself with
 * BUN_BE_BUN=1, which makes a `bun build --compile` executable behave as the
 * full `bun` CLI, so `bun add`/`bun install` work with no second binary shipped.
 *
 * Install model: each extension gets its own wrapper dir,
 * ~/.loop/extensions/<name>/, whose package.json declares the extension as its
 * single dependency. `bun add <spec>` then pulls the extension AND its
 * transitive deps into that dir's node_modules. The entry is resolved from
 * node_modules/<pkg>. `loop link` instead loads a local path in place (after a
 * `bun install` there) for development.
 */
import { existsSync, mkdirSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { parseSource } from "./sources";
import { assertLoadable } from "./manifest";
import { deleteRecord, extensionDir, extensionsDir, getRecord, putRecord } from "./store";
import type { ExtensionManifest } from "./api";

/** Hard cap on one package-manager run — a stalled registry must not hang `loop install` forever. */
const RUN_BUN_TIMEOUT_MS = 300_000;

/** Spawn the loop binary as bun (BUN_BE_BUN) to run a package-manager command. */
async function runBun(cwd: string, args: string[]): Promise<void> {
    const proc = Bun.spawn([process.execPath, ...args], {
        cwd,
        env: { ...process.env, BUN_BE_BUN: "1" },
        stdout: "pipe",
        stderr: "pipe",
    });
    let timedOut = false;
    const timer = setTimeout(() => {
        timedOut = true;
        proc.kill();
    }, RUN_BUN_TIMEOUT_MS);
    // Drain both streams alongside exit so a chatty install can't stall on a
    // full pipe, and the output is available for the error message.
    const [stdout, stderr, code] = await Promise.all([
        new Response(proc.stdout).text(),
        new Response(proc.stderr).text(),
        proc.exited,
    ]).finally(() => clearTimeout(timer));
    if (code !== 0) {
        const detail = [stderr.trim(), stdout.trim()].filter(Boolean).join("\n");
        const why = timedOut ? `timed out after ${RUN_BUN_TIMEOUT_MS / 1000}s` : `exit ${code}`;
        throw new Error(`bun ${args.join(" ")} failed (${why}):\n${detail}`);
    }
}

function readManifest(pkgJsonPath: string): ExtensionManifest {
    const raw = JSON.parse(readFileSync(pkgJsonPath, "utf8")) as ExtensionManifest;
    if (!raw.name) throw new Error(`extension package.json missing "name": ${pkgJsonPath}`);
    return raw;
}

/**
 * After `bun add`, the wrapper's dependency key is the installed pkg. Normally
 * there is exactly one; if the resolver ever adds more, prefer the one that
 * looks like a loop extension rather than silently picking whatever sorts first.
 */
function installedDepName(wrapperDir: string): string {
    const pkg = JSON.parse(readFileSync(join(wrapperDir, "package.json"), "utf8")) as {
        dependencies?: Record<string, string>;
    };
    const names = Object.keys(pkg.dependencies ?? {});
    if (names.length === 0) throw new Error("install produced no dependency");
    if (names.length === 1) return names[0];
    const withLoopField = names.filter((n) => {
        try {
            const m = JSON.parse(
                readFileSync(join(wrapperDir, "node_modules", n, "package.json"), "utf8"),
            ) as ExtensionManifest;
            return m.loop !== undefined;
        } catch {
            return false;
        }
    });
    if (withLoopField.length === 1) return withLoopField[0];
    throw new Error(`install produced multiple dependencies (${names.join(", ")}); cannot tell which is the extension`);
}

/**
 * Remove `.staging-*` leftovers from installs that died before their own
 * cleanup ran. Only sweeps dirs older than an hour so a concurrent install's
 * live staging dir is never touched.
 */
function sweepStaleStaging(): void {
    const root = extensionsDir();
    if (!existsSync(root)) return;
    for (const name of readdirSync(root)) {
        const ts = name.startsWith(".staging-") ? Number(name.slice(".staging-".length)) : NaN;
        if (Number.isFinite(ts) && Date.now() - ts > 3_600_000) {
            rmSync(join(root, name), { recursive: true, force: true });
        }
    }
}

export interface InstallResult {
    name: string;
    version?: string;
    dir: string;
}

export async function installExtension(input: string): Promise<InstallResult> {
    const src = parseSource(input);
    if (src.kind === "local") return linkExtension(src.spec);

    sweepStaleStaging();

    // Stage in a provisional dir, then rename to the real package name.
    const stageDir = extensionDir(`.staging-${Date.now()}`);
    rmSync(stageDir, { recursive: true, force: true });
    mkdirSync(stageDir, { recursive: true });
    writeFileSync(
        join(stageDir, "package.json"),
        JSON.stringify({ name: "loop-extension-host", private: true, type: "module" }, null, 2),
    );

    try {
        await runBun(stageDir, ["add", src.spec]);
        const pkgName = installedDepName(stageDir);
        const stagedPkgDir = join(stageDir, "node_modules", pkgName);
        const manifest = readManifest(join(stagedPkgDir, "package.json"));
        // Reject incompatible/broken extensions here, not as a warning at the
        // next session's startup.
        assertLoadable(stagedPkgDir, manifest);

        const finalDir = extensionDir(manifest.name);
        rmSync(finalDir, { recursive: true, force: true });
        // Scoped names (@scope/pkg) nest a directory — ensure the parent exists
        // before the same-parent rename of the staging dir into place.
        mkdirSync(dirname(finalDir), { recursive: true });
        renameSync(stageDir, finalDir);

        putRecord({
            name: manifest.name,
            version: manifest.version,
            source: input,
            sourceKind: src.kind,
            enabled: true,
            installedAt: Date.now(),
        });
        return { name: manifest.name, version: manifest.version, dir: finalDir };
    } catch (err) {
        rmSync(stageDir, { recursive: true, force: true });
        throw err;
    }
}

/** Dev workflow: load a local extension in place, after installing its deps. */
export async function linkExtension(absPath: string): Promise<InstallResult> {
    const pkgJsonPath = join(absPath, "package.json");
    if (!existsSync(pkgJsonPath)) throw new Error(`no package.json at ${absPath}`);
    const manifest = readManifest(pkgJsonPath);
    // Resolve the linked extension's own deps in place.
    await runBun(absPath, ["install"]);
    assertLoadable(absPath, manifest);
    putRecord({
        name: manifest.name,
        version: manifest.version,
        source: absPath,
        sourceKind: "local",
        enabled: true,
        linkPath: absPath,
        installedAt: Date.now(),
    });
    return { name: manifest.name, version: manifest.version, dir: absPath };
}

export function removeExtension(name: string): boolean {
    const rec = getRecord(name);
    if (!rec) return false;
    // Linked extensions live outside our tree — never delete the user's source.
    if (rec.sourceKind !== "local") rmSync(extensionDir(name), { recursive: true, force: true });
    return deleteRecord(name);
}

/** Repair: reinstall deps for one extension (or all) from its recorded source. */
export async function syncExtensions(name?: string): Promise<void> {
    const { listRecords } = await import("./store");
    const targets = name ? [getRecord(name)].filter(Boolean) : listRecords();
    for (const rec of targets) {
        if (!rec) continue;
        const dir = rec.linkPath ?? extensionDir(rec.name);
        if (!existsSync(dir)) {
            await installExtension(rec.source);
        } else {
            await runBun(dir, ["install"]);
        }
    }
}
