import type { ProviderId } from "@notshekhar/pi-core";
import { runInteractive } from "./interactive/app";

// Silence ai-sdk warning logger. We intentionally implement
// LanguageModelV2 manually (e.g. for cursor) — the "compatibility mode"
// notice is noise for end users.
(globalThis as unknown as { AI_SDK_LOG_WARNINGS: false }).AI_SDK_LOG_WARNINGS = false;

// Cursor SDK uses connectRPC over HTTP/2 with multiple streams; one of
// them sometimes closes with NGHTTP2_FRAME_SIZE_ERROR AFTER the actual
// data stream completes successfully. Swallow that specific noise so it
// doesn't print a scary stack trace below a working response.
function isCursorTransportNoise(err: unknown): boolean {
  const msg = err instanceof Error ? err.message : String(err);
  return msg.includes("NGHTTP2_FRAME_SIZE_ERROR") ||
         msg.includes("Stream closed with error code");
}
process.on("unhandledRejection", (err) => {
  if (isCursorTransportNoise(err)) return;
  // Re-raise non-cursor unhandled rejections so we don't hide real bugs.
  console.error("Unhandled rejection:", err);
  process.exitCode = 1;
});
process.on("uncaughtException", (err) => {
  if (isCursorTransportNoise(err)) return;
  console.error("Uncaught exception:", err);
  process.exitCode = 1;
});
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
