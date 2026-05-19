import { spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import {
  getActiveProvider,
  getCatalog,
  listAuthorizedProviders,
  loginApiKey,
  loginXaiOAuth,
  logout,
  SessionManager,
  startSocketServer,
  startStdioServer,
} from "@notshekhar/pi-core";
import type { ProviderId } from "@notshekhar/pi-core";
import { readStdinLine, type Args } from "./args";
import { runPrint } from "./print";
import { openBrowser } from "./open-browser";

const REPO_SLUG = "notshekhar/pi";
const UPGRADE_URL = `https://raw.githubusercontent.com/${REPO_SLUG}/main/install.sh`;
const RELEASES_API = `https://api.github.com/repos/${REPO_SLUG}/releases/latest`;
const NPM_PACKAGE = "@notshekhar/pi";

type InstallMethod = "binary" | "npm" | "source" | "unknown";

// Identify how the running `pi` was installed so `pi upgrade` uses the
// matching upgrade path. The installer writes `.install-method` next to the
// binary (binary/source); npm installs have node as execPath.
function detectInstallMethod(): InstallMethod {
  const execDir = dirname(process.execPath);
  const markerFile = join(execDir, ".install-method");
  if (existsSync(markerFile)) {
    const v = readFileSync(markerFile, "utf8").trim();
    if (v === "binary" || v === "npm" || v === "source") return v;
  }
  const execName = basename(process.execPath).toLowerCase();
  if (execName === "node" || execName === "node.exe") return "npm";
  return "binary";
}

function semverGt(a: string, b: string): boolean {
  const norm = (v: string) => v.replace(/^v/, "").split(".").map((n) => Number.parseInt(n, 10) || 0);
  const [a1, a2, a3] = norm(a);
  const [b1, b2, b3] = norm(b);
  if (a1 !== b1) return a1 > b1;
  if (a2 !== b2) return a2 > b2;
  return a3 > b3;
}

async function fetchLatestTag(): Promise<string | null> {
  try {
    const r = await fetch(RELEASES_API, { headers: { accept: "application/vnd.github+json" } });
    if (!r.ok) return null;
    const j = (await r.json()) as { tag_name?: string };
    return j.tag_name ?? null;
  } catch {
    return null;
  }
}

export function printHelp(version: string): void {
  console.log(`pi/agent — terminal coding agent (v${version})

Usage:
  pi                       Start interactive TUI
  pi run <prompt>          Run a single prompt and exit
  pi login [provider]      Configure provider auth
  pi logout [provider]     Remove auth
  pi sessions              List sessions in current cwd
  pi models                List available models
  pi whoami                Show active provider + auth status
  pi rpc [--socket]        Start JSON-RPC server
  pi upgrade               Pull latest and rebuild
  pi version | -v          Print version

Flags:
  --model <provider/id>    Override default model
  --provider <id>          Override active provider
  --cwd <path>             Working directory
  --session <id>           Resume session by id`);
}

export async function runUpgrade(version: string, opts: { force?: boolean } = {}): Promise<void> {
  console.log(`▶ Checking for updates (current v${version})…`);
  const latest = await fetchLatestTag();
  if (!opts.force && latest) {
    if (!semverGt(latest, `v${version}`)) {
      console.log(`✓ Up to date (latest ${latest})`);
      return;
    }
    console.log(`▶ Upgrading ${version} → ${latest}`);
  } else if (!latest) {
    console.log("▶ Could not query latest release; running installer anyway.");
  }

  const method = detectInstallMethod();
  console.log(`▶ Install method: ${method}`);

  let cmd: string;
  const env: NodeJS.ProcessEnv = { ...process.env };
  if (opts.force) env.PI_FORCE = "1";

  switch (method) {
    case "npm":
      cmd = `npm install -g ${NPM_PACKAGE}@latest`;
      break;
    case "source":
      env.PI_FROM_SOURCE = "1";
      cmd = `curl -fsSL ${UPGRADE_URL} | bash`;
      break;
    case "binary":
    case "unknown":
    default:
      cmd = `curl -fsSL ${UPGRADE_URL} | bash`;
      break;
  }

  const r = spawnSync("bash", ["-c", cmd], { stdio: "inherit", env });
  process.exit(r.status ?? 1);
}

export async function cmdLogin(provider?: string): Promise<void> {
  const p = (provider ?? "xai") as ProviderId;
  if (p === "xai") {
    const mode = await readStdinLine("xAI: [1] OAuth subscription  [2] API key  > ");
    if (mode === "2") {
      const key = await readStdinLine("XAI_API_KEY: ");
      loginApiKey("xai", key);
      console.log("xAI API key saved.");
    } else {
      await loginXaiOAuth(({ url, instructions }) => {
        console.log(instructions);
        console.log(url);
        const opened = openBrowser(url);
        console.log(opened ? "(opened in browser)" : "(open this URL in a browser)");
      });
      console.log("xAI OAuth login complete.");
    }
    return;
  }
  const key = await readStdinLine(`${p.toUpperCase()}_API_KEY: `);
  loginApiKey(p, key);
  console.log(`${p} API key saved.`);
}

export function cmdLogout(target?: ProviderId): void {
  logout(target);
  console.log(target ? `Logged out of ${target}.` : "Logged out of all providers.");
}

export async function cmdSessions(): Promise<void> {
  const mgr = new SessionManager();
  const sessions = mgr.list(process.cwd());
  if (sessions.length === 0) {
    console.log("No sessions in this cwd.");
    return;
  }
  for (const s of sessions) {
    console.log(`${s.id}  ${s.model}  ${new Date(s.mtime).toISOString()}  ${s.firstUserMessage ?? ""}`);
  }
}

export function cmdRpc(args: Args): void {
  const sub = args.positional[0];
  if (sub === "stop") {
    // TODO: send SIGTERM via rpc.pid file
    console.log("not implemented");
    return;
  }
  if (args.flags.socket) {
    const { socketPath } = startSocketServer();
    console.log(`pi RPC daemon listening on ${socketPath}`);
    return;
  }
  startStdioServer();
}

export async function cmdRun(args: Args): Promise<void> {
  const prompt = args.positional.join(" ");
  await runPrint({
    prompt,
    modelId: (args.flags.model as string) || undefined,
    cwd: (args.flags.cwd as string) || process.cwd(),
  });
}

export async function cmdModels(): Promise<void> {
  const cat = await getCatalog();
  for (const m of Object.values(cat)) {
    console.log(`${m.id}\tctx:${m.contextWindow}\t$${m.cost.input}/$${m.cost.output}`);
  }
}

export function cmdWhoami(): void {
  console.log(`Active provider: ${getActiveProvider() ?? "none"}`);
  console.log(`Authorized: ${listAuthorizedProviders().join(", ") || "none"}`);
}
