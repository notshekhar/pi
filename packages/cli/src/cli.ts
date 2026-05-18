import type { ProviderId } from "@notshekhar/pi-core";
import { runInteractive } from "./interactive/app";
import { parseArgs } from "./args";
import {
  cmdLogin,
  cmdLogout,
  cmdModels,
  cmdRpc,
  cmdRun,
  cmdSessions,
  cmdWhoami,
  printHelp,
  runUpgrade,
} from "./commands";

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
    printHelp(VERSION);
    return;
  }

  switch (args.cmd) {
    case "version":
    case "-v":
      console.log(VERSION);
      return;
    case "help":
      printHelp(VERSION);
      return;
    case "upgrade":
    case "update":
      await runUpgrade(VERSION, { force: Boolean(args.flags.force) });
      return;
    case "login":
      await cmdLogin(args.positional[0]);
      return;
    case "logout":
      cmdLogout(args.positional[0] as ProviderId | undefined);
      return;
    case "sessions":
      await cmdSessions();
      return;
    case "rpc":
      cmdRpc(args);
      return;
    case "run":
      await cmdRun(args);
      return;
    case "models":
      await cmdModels();
      return;
    case "whoami":
      cmdWhoami();
      return;
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
