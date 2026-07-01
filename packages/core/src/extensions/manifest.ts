/**
 * Manifest helpers shared by the host (load time) and install/link (install
 * time), so an extension that can't load is rejected at install instead of
 * surfacing as a startup warning one session later.
 */
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { ExtensionManifest } from "./api";
import { EXTENSION_API_VERSION } from "./api";
import { extensionDir, type ExtensionRecord } from "./store";

/** Resolve the on-disk directory holding the extension's package + its entry. */
export function resolvePkgDir(record: ExtensionRecord): string {
    if (record.linkPath) return record.linkPath;
    const wrapper = extensionDir(record.name);
    // Installed-as-dependency: the package lives under the wrapper's
    // node_modules, keyed by its package name — which is the record name.
    const direct = join(wrapper, "node_modules", record.name);
    if (existsSync(join(direct, "package.json"))) return direct;
    // Fallback for older installs whose record name may not match the dep key.
    const wrapperPkg = join(wrapper, "package.json");
    if (existsSync(wrapperPkg)) {
        try {
            const deps = (JSON.parse(readFileSync(wrapperPkg, "utf8")) as { dependencies?: Record<string, string> })
                .dependencies;
            const dep = deps && Object.keys(deps)[0];
            if (dep) return join(wrapper, "node_modules", dep);
        } catch {
            /* fall through */
        }
    }
    return wrapper;
}

export function resolveEntry(pkgDir: string, manifest: ExtensionManifest): string {
    const rel = manifest.loop?.entry ?? manifest.module ?? manifest.main ?? "index.ts";
    return join(pkgDir, rel);
}

/**
 * Compat check against the host API version. Majors must match; while the API
 * is 0.x, semver treats minors as breaking too, so those must also match (an
 * engines value without a minor, e.g. "^0", accepts any 0.x).
 */
export function isCompatible(manifest: ExtensionManifest): boolean {
    const want = manifest.loop?.engines?.loop;
    if (!want) return true; // unspecified = assume compatible
    const wantParts = want.replace(/^[\^~>=<\s]+/, "").split(".");
    const haveParts = EXTENSION_API_VERSION.split(".");
    if (wantParts[0] !== haveParts[0]) return false;
    if (haveParts[0] === "0") return wantParts[1] === undefined || wantParts[1] === haveParts[1];
    return true;
}

/** Throw (with the reason) unless the manifest is loadable by this host. */
export function assertLoadable(pkgDir: string, manifest: ExtensionManifest): void {
    if (!isCompatible(manifest)) {
        throw new Error(`requires loop API ${manifest.loop?.engines?.loop}, host is ${EXTENSION_API_VERSION}`);
    }
    const entry = resolveEntry(pkgDir, manifest);
    if (!existsSync(entry)) throw new Error(`entry not found: ${entry}`);
}
