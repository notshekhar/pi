import {
  startStdioServer,
  startSocketServer,
  loginApiKey,
  loginXaiOAuth,
  logout,
  listAuthorizedProviders,
  getActiveProvider,
  SessionManager,
  getCatalog,
} from "@pi/core";
import type { ProviderId } from "@pi/core";
import { runInteractive } from "./interactive/app";
import { runPrint } from "./print";

interface Args {
  cmd?: string;
  positional: string[];
  flags: Record<string, string | boolean>;
}

function parseArgs(argv: string[]): Args {
  const out: Args = { positional: [], flags: {} };
  let i = 0;
  if (argv[0] && !argv[0].startsWith("-")) {
    out.cmd = argv[0];
    i = 1;
  }
  for (; i < argv.length; i++) {
    const a = argv[i];
    if (a.startsWith("--")) {
      const eq = a.indexOf("=");
      if (eq > 0) {
        out.flags[a.slice(2, eq)] = a.slice(eq + 1);
      } else {
        const next = argv[i + 1];
        if (next && !next.startsWith("--")) {
          out.flags[a.slice(2)] = next;
          i++;
        } else {
          out.flags[a.slice(2)] = true;
        }
      }
    } else {
      out.positional.push(a);
    }
  }
  return out;
}

async function readStdinLine(prompt: string): Promise<string> {
  process.stdout.write(prompt);
  return new Promise((resolve) => {
    let data = "";
    process.stdin.setEncoding("utf8");
    const onData = (chunk: string) => {
      data += chunk;
      const nl = data.indexOf("\n");
      if (nl >= 0) {
        process.stdin.off("data", onData);
        process.stdin.pause();
        resolve(data.slice(0, nl).trim());
      }
    };
    process.stdin.resume();
    process.stdin.on("data", onData);
  });
}

async function cmdLogin(provider?: string): Promise<void> {
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
      });
      console.log("xAI OAuth login complete.");
    }
    return;
  }
  const key = await readStdinLine(`${p.toUpperCase()}_API_KEY: `);
  loginApiKey(p, key);
  console.log(`${p} API key saved.`);
}

async function cmdSessions(): Promise<void> {
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

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2));

  switch (args.cmd) {
    case "login":
      await cmdLogin(args.positional[0]);
      return;
    case "logout": {
      const target = args.positional[0] as ProviderId | undefined;
      logout(target);
      console.log(target ? `Logged out of ${target}.` : "Logged out of all providers.");
      return;
    }
    case "sessions":
      await cmdSessions();
      return;
    case "rpc": {
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
      return;
    }
    case "run": {
      const prompt = args.positional.join(" ");
      await runPrint({
        prompt,
        modelId: (args.flags.model as string) || undefined,
        cwd: (args.flags.cwd as string) || process.cwd(),
      });
      return;
    }
    case "models": {
      const cat = await getCatalog();
      for (const m of Object.values(cat)) {
        console.log(`${m.id}\tctx:${m.contextWindow}\t$${m.cost.input}/$${m.cost.output}`);
      }
      return;
    }
    case "whoami": {
      console.log(`Active provider: ${getActiveProvider() ?? "none"}`);
      console.log(`Authorized: ${listAuthorizedProviders().join(", ") || "none"}`);
      return;
    }
    case undefined:
    default:
      await runInteractive({
        modelId: (args.flags.model as string) || undefined,
        provider: (args.flags.provider as ProviderId) || undefined,
        cwd: (args.flags.cwd as string) || process.cwd(),
        sessionId: (args.flags.session as string) || undefined,
      });
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
