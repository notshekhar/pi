// AI SDK prints advisory warnings to the console mid-stream, which tears the
// TUI's differential rendering. Disable globally before anything runs.
(globalThis as Record<string, unknown>).AI_SDK_LOG_WARNINGS = false;

import type { ProviderId } from "@notshekhar/pi-core";
import { parseArgs } from "./args";

// The interactive app and subcommands transitively pull in pi-tui, highlight.js,
// and core (~400ms of module eval). Dynamic-import them per command so
// --version/--help stay instant and each command only loads what it needs.
const commands = () => import("./commands");
const interactive = () => import("./interactive/app");

// injected at build time via tsup define
declare const __PI_VERSION__: string;
const VERSION = typeof __PI_VERSION__ !== "undefined" ? __PI_VERSION__ : "0.0.0";

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2));

  if (args.flags.version || args.flags.v) {
    console.log(VERSION);
    return;
  }
  if (args.flags.help || args.flags.h) {
    (await commands()).printHelp(VERSION);
    return;
  }

  switch (args.cmd) {
    case "version":
    case "-v":
      console.log(VERSION);
      return;
    case "help":
      (await commands()).printHelp(VERSION);
      return;
    case "upgrade":
    case "update":
      await (await commands()).runUpgrade(VERSION, { force: Boolean(args.flags.force) });
      return;
    case "login":
      await (await commands()).cmdLogin(args.positional[0]);
      return;
    case "logout":
      (await commands()).cmdLogout(args.positional[0] as ProviderId | undefined);
      return;
    case "sessions":
      await (await commands()).cmdSessions();
      return;
    case "rpc":
      (await commands()).cmdRpc(args);
      return;
    case "run":
      await (await commands()).cmdRun(args);
      return;
    case "models":
      await (await commands()).cmdModels();
      return;
    case "whoami":
      (await commands()).cmdWhoami();
      return;
    case undefined:
    default:
      await (await interactive()).runInteractive({
        modelId: (args.flags.model as string) || undefined,
        provider: (args.flags.provider as ProviderId) || undefined,
        cwd: (args.flags.cwd as string) || process.cwd(),
        sessionId: (args.flags.session as string) || undefined,
        version: VERSION,
      });
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
