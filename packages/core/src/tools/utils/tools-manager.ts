import { type SpawnSyncReturns, spawnSync } from "node:child_process";
import { chmodSync, createWriteStream, existsSync, mkdirSync, readdirSync, renameSync, rmSync } from "node:fs";
import { arch, platform } from "node:os";
import { join } from "node:path";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";
import { getBinDir } from "./shell";

const TOOLS_DIR = getBinDir();
const NETWORK_TIMEOUT_MS = 10_000;
const DOWNLOAD_TIMEOUT_MS = 120_000;
const APP_NAME = "loop";

function isOfflineModeEnabled(): boolean {
    const v = process.env.LOOP_OFFLINE;
    if (!v) return false;
    return v === "1" || v.toLowerCase() === "true" || v.toLowerCase() === "yes";
}

interface ToolConfig {
    name: string;
    repo: string;
    binaryName: string;
    systemBinaryNames?: string[];
    tagPrefix: string;
    getAssetName: (version: string, plat: string, architecture: string) => string | null;
}

const TOOLS: Record<string, ToolConfig> = {
    fd: {
        name: "fd",
        repo: "sharkdp/fd",
        binaryName: "fd",
        systemBinaryNames: ["fd", "fdfind"],
        tagPrefix: "v",
        getAssetName: (version, plat, architecture) => {
            if (plat === "darwin") {
                const archStr = architecture === "arm64" ? "aarch64" : "x86_64";
                return `fd-v${version}-${archStr}-apple-darwin.tar.gz`;
            } else if (plat === "linux") {
                const archStr = architecture === "arm64" ? "aarch64" : "x86_64";
                return `fd-v${version}-${archStr}-unknown-linux-gnu.tar.gz`;
            } else if (plat === "win32") {
                const archStr = architecture === "arm64" ? "aarch64" : "x86_64";
                return `fd-v${version}-${archStr}-pc-windows-msvc.zip`;
            }
            return null;
        },
    },
    rg: {
        name: "ripgrep",
        repo: "BurntSushi/ripgrep",
        binaryName: "rg",
        tagPrefix: "",
        getAssetName: (version, plat, architecture) => {
            if (plat === "darwin") {
                const archStr = architecture === "arm64" ? "aarch64" : "x86_64";
                return `ripgrep-${version}-${archStr}-apple-darwin.tar.gz`;
            } else if (plat === "linux") {
                if (architecture === "arm64") return `ripgrep-${version}-aarch64-unknown-linux-gnu.tar.gz`;
                return `ripgrep-${version}-x86_64-unknown-linux-musl.tar.gz`;
            } else if (plat === "win32") {
                const archStr = architecture === "arm64" ? "aarch64" : "x86_64";
                return `ripgrep-${version}-${archStr}-pc-windows-msvc.zip`;
            }
            return null;
        },
    },
};

function commandExists(cmd: string): boolean {
    try {
        const r = spawnSync(cmd, ["--version"], { stdio: "pipe" });
        return r.error === undefined || r.error === null;
    } catch {
        return false;
    }
}

export function getToolPath(tool: "fd" | "rg"): string | null {
    const config = TOOLS[tool];
    if (!config) return null;
    const localPath = join(TOOLS_DIR, config.binaryName + (platform() === "win32" ? ".exe" : ""));
    if (existsSync(localPath)) return localPath;
    const names = config.systemBinaryNames ?? [config.binaryName];
    for (const n of names) if (commandExists(n)) return n;
    return null;
}

async function getLatestVersion(repo: string): Promise<string> {
    const r = await fetch(`https://api.github.com/repos/${repo}/releases/latest`, {
        headers: { "User-Agent": `${APP_NAME}-coding-agent` },
        signal: AbortSignal.timeout(NETWORK_TIMEOUT_MS),
    });
    if (!r.ok) throw new Error(`GitHub API error: ${r.status}`);
    const data = (await r.json()) as { tag_name: string };
    return data.tag_name.replace(/^v/, "");
}

async function downloadFile(url: string, dest: string): Promise<void> {
    const r = await fetch(url, { signal: AbortSignal.timeout(DOWNLOAD_TIMEOUT_MS) });
    if (!r.ok) throw new Error(`Failed to download: ${r.status}`);
    if (!r.body) throw new Error("No response body");
    const file = createWriteStream(dest);
    await pipeline(Readable.fromWeb(r.body as never), file);
}

function findBinaryRecursively(rootDir: string, binaryFileName: string): string | null {
    const stack: string[] = [rootDir];
    while (stack.length > 0) {
        const cur = stack.pop();
        if (!cur) continue;
        const entries = readdirSync(cur, { withFileTypes: true });
        for (const e of entries) {
            const full = join(cur, e.name);
            if (e.isFile() && e.name === binaryFileName) return full;
            if (e.isDirectory()) stack.push(full);
        }
    }
    return null;
}

function formatSpawnFailure(r: SpawnSyncReturns<Buffer>): string {
    if (r.error?.message) return r.error.message;
    const stderr = r.stderr?.toString().trim();
    if (stderr) return stderr;
    const stdout = r.stdout?.toString().trim();
    if (stdout) return stdout;
    return `exit status ${r.status ?? "unknown"}`;
}

function runExtractionCommand(command: string, args: string[]): string | null {
    const r = spawnSync(command, args, { stdio: "pipe" });
    if (!r.error && r.status === 0) return null;
    return `${command}: ${formatSpawnFailure(r)}`;
}

function extractTarGzArchive(archivePath: string, extractDir: string, assetName: string): void {
    const failure = runExtractionCommand("tar", ["xzf", archivePath, "-C", extractDir]);
    if (failure) throw new Error(`Failed to extract ${assetName}: ${failure}`);
}

function getWindowsTarCommand(): string {
    const sysRoot = process.env.SystemRoot ?? process.env.WINDIR;
    if (sysRoot) {
        const sysTar = join(sysRoot, "System32", "tar.exe");
        if (existsSync(sysTar)) return sysTar;
    }
    return "tar.exe";
}

function extractZipArchive(archivePath: string, extractDir: string, assetName: string): void {
    const failures: string[] = [];
    if (platform() === "win32") {
        const tarFailure = runExtractionCommand(getWindowsTarCommand(), ["xf", archivePath, "-C", extractDir]);
        if (!tarFailure) return;
        failures.push(tarFailure);
        const script =
            "& { param($archive, $destination) $ErrorActionPreference = 'Stop'; Expand-Archive -LiteralPath $archive -DestinationPath $destination -Force }";
        const psFailure = runExtractionCommand("powershell.exe", [
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-ExecutionPolicy",
            "Bypass",
            "-Command",
            script,
            archivePath,
            extractDir,
        ]);
        if (!psFailure) return;
        failures.push(psFailure);
    } else {
        const unzipFailure = runExtractionCommand("unzip", ["-q", archivePath, "-d", extractDir]);
        if (!unzipFailure) return;
        failures.push(unzipFailure);
        const tarFailure = runExtractionCommand("tar", ["xf", archivePath, "-C", extractDir]);
        if (!tarFailure) return;
        failures.push(tarFailure);
    }
    throw new Error(`Failed to extract ${assetName}: ${failures.join("; ")}`);
}

async function downloadTool(tool: "fd" | "rg"): Promise<string> {
    const config = TOOLS[tool];
    if (!config) throw new Error(`Unknown tool: ${tool}`);
    const plat = platform();
    const architecture = arch();
    let version = await getLatestVersion(config.repo);
    if (tool === "fd" && plat === "darwin" && architecture === "x64") version = "10.3.0";
    const assetName = config.getAssetName(version, plat, architecture);
    if (!assetName) throw new Error(`Unsupported platform: ${plat}/${architecture}`);
    mkdirSync(TOOLS_DIR, { recursive: true });
    const downloadUrl = `https://github.com/${config.repo}/releases/download/${config.tagPrefix}${version}/${assetName}`;
    const archivePath = join(TOOLS_DIR, assetName);
    const binaryExt = plat === "win32" ? ".exe" : "";
    const binaryPath = join(TOOLS_DIR, config.binaryName + binaryExt);
    await downloadFile(downloadUrl, archivePath);
    const extractDir = join(
        TOOLS_DIR,
        `extract_tmp_${config.binaryName}_${process.pid}_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`,
    );
    mkdirSync(extractDir, { recursive: true });
    try {
        if (assetName.endsWith(".tar.gz")) extractTarGzArchive(archivePath, extractDir, assetName);
        else if (assetName.endsWith(".zip")) extractZipArchive(archivePath, extractDir, assetName);
        else throw new Error(`Unsupported archive format: ${assetName}`);
        const binaryFileName = config.binaryName + binaryExt;
        const extractedDir = join(extractDir, assetName.replace(/\.(tar\.gz|zip)$/, ""));
        const candidates = [join(extractedDir, binaryFileName), join(extractDir, binaryFileName)];
        let extractedBinary = candidates.find((c) => existsSync(c));
        if (!extractedBinary) extractedBinary = findBinaryRecursively(extractDir, binaryFileName) ?? undefined;
        if (extractedBinary) renameSync(extractedBinary, binaryPath);
        else throw new Error(`Binary not found in archive: expected ${binaryFileName} under ${extractDir}`);
        if (plat !== "win32") chmodSync(binaryPath, 0o755);
    } finally {
        rmSync(archivePath, { force: true });
        rmSync(extractDir, { recursive: true, force: true });
    }
    return binaryPath;
}

const TERMUX_PACKAGES: Record<string, string> = { fd: "fd", rg: "ripgrep" };

export async function ensureTool(tool: "fd" | "rg", silent = false): Promise<string | undefined> {
    const existing = getToolPath(tool);
    if (existing) return existing;
    const config = TOOLS[tool];
    if (!config) return undefined;
    if (isOfflineModeEnabled()) {
        if (!silent) console.log(`${config.name} not found. Offline mode enabled, skipping download.`);
        return undefined;
    }
    if (platform() === "android") {
        const pkg = TERMUX_PACKAGES[tool] ?? tool;
        if (!silent) console.log(`${config.name} not found. Install with: pkg install ${pkg}`);
        return undefined;
    }
    if (!silent) console.log(`${config.name} not found. Downloading...`);
    try {
        const p = await downloadTool(tool);
        if (!silent) console.log(`${config.name} installed to ${p}`);
        return p;
    } catch (e) {
        if (!silent) console.log(`Failed to download ${config.name}: ${e instanceof Error ? e.message : e}`);
        return undefined;
    }
}
