import { createServer, type Server, type Socket } from "node:net";
import { EventEmitter } from "node:events";
import { existsSync, unlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { getPiDir } from "../auth/storage";
import { SessionManager, type Session } from "../sessions";
import { runTurn, CostTracker, runCompact } from "../agent";
import { getCatalog } from "../catalog";
import { CommandRegistry, registerBuiltins } from "../commands";
import { listAuthorizedProviders, getActiveProvider, loginApiKey } from "../auth";
import { RpcErrorCode, type RpcNotification, type RpcRequest, type RpcResponse } from "./protocol";
import type { ProviderId } from "../types";

interface ActiveSession {
  session: Session;
  tracker: CostTracker;
  abort: AbortController;
  emitter: EventEmitter;
  modelId: string;
}

type Transport = {
  send(msg: RpcResponse | RpcNotification): void;
};

export class RpcServer {
  private sessions = new Map<string, ActiveSession>();
  private manager = new SessionManager();
  private commands = new CommandRegistry();

  constructor() {
    // fire-and-forget: registry is only read after the event loop turns
    void registerBuiltins(this.commands);
  }

  attach(transport: Transport): { feed: (chunk: Buffer | string) => void } {
    let buffer = "";
    const feed = (chunk: Buffer | string) => {
      buffer += typeof chunk === "string" ? chunk : chunk.toString();
      let idx: number;
      while ((idx = buffer.indexOf("\n")) >= 0) {
        const line = buffer.slice(0, idx).trim();
        buffer = buffer.slice(idx + 1);
        if (line) this.handleLine(line, transport);
      }
    };
    return { feed };
  }

  private async handleLine(line: string, transport: Transport): Promise<void> {
    let req: RpcRequest;
    try {
      req = JSON.parse(line) as RpcRequest;
    } catch {
      transport.send({
        jsonrpc: "2.0",
        id: null,
        error: { code: RpcErrorCode.PARSE_ERROR, message: "Parse error" },
      });
      return;
    }
    try {
      const result = await this.dispatch(req, transport);
      if (req.id !== undefined) {
        transport.send({ jsonrpc: "2.0", id: req.id, result });
      }
    } catch (err) {
      if (req.id !== undefined) {
        transport.send({
          jsonrpc: "2.0",
          id: req.id,
          error: { code: RpcErrorCode.INTERNAL_ERROR, message: (err as Error).message },
        });
      }
    }
  }

  private async dispatch(req: RpcRequest, transport: Transport): Promise<unknown> {
    const params = (req.params ?? {}) as Record<string, unknown>;
    switch (req.method) {
      case "session.create": {
        const cwd = String(params.cwd ?? process.cwd());
        const provider = (params.provider as ProviderId) ?? getActiveProvider() ?? "xai";
        const model = String(params.model ?? "");
        const session = await this.manager.create({ cwd, provider, model });
        const emitter = new EventEmitter();
        const ctx: ActiveSession = {
          session,
          tracker: new CostTracker(),
          abort: new AbortController(),
          emitter,
          modelId: model,
        };
        this.wireEmitter(session.id, emitter, transport);
        this.sessions.set(session.id, ctx);
        return { sessionId: session.id };
      }
      case "session.list": {
        return this.manager.list(params.cwd as string | undefined);
      }
      case "session.open": {
        const id = String(params.sessionId);
        const session = await this.manager.open(id);
        const emitter = new EventEmitter();
        const ctx: ActiveSession = {
          session,
          tracker: new CostTracker(),
          abort: new AbortController(),
          emitter,
          modelId: session.info.model,
        };
        this.wireEmitter(session.id, emitter, transport);
        this.sessions.set(session.id, ctx);
        return { sessionId: session.id, info: session.info };
      }
      case "session.send": {
        const id = String(params.sessionId);
        const ctx = this.requireSession(id);
        const input = String(params.input ?? "");
        const modelId = String(params.model ?? ctx.modelId);
        ctx.modelId = modelId;
        // run async; events stream via notifications
        runTurn({
          session: ctx.session,
          modelId,
          userInput: input,
          cwd: ctx.session.info.cwd,
          abortSignal: ctx.abort.signal,
          tracker: ctx.tracker,
          emitter: ctx.emitter,
        }).catch((err) => {
          transport.send({
            jsonrpc: "2.0",
            method: "session.event",
            params: { sessionId: id, part: { type: "error", error: String(err) } },
          });
        });
        return { ok: true };
      }
      case "session.cancel": {
        const id = String(params.sessionId);
        const ctx = this.requireSession(id);
        ctx.abort.abort();
        ctx.abort = new AbortController();
        return { ok: true };
      }
      case "session.compact": {
        const id = String(params.sessionId);
        const ctx = this.requireSession(id);
        const result = await runCompact({ session: ctx.session, modelId: ctx.modelId, keepTurns: 0 });
        return result;
      }
      case "auth.status":
        return { providers: listAuthorizedProviders(), active: getActiveProvider() };
      case "auth.login": {
        const provider = params.provider as ProviderId;
        const key = String(params.apiKey ?? "");
        if (!provider || !key) {
          throw new Error("provider and apiKey required");
        }
        loginApiKey(provider, key);
        return { ok: true };
      }
      case "catalog.list": {
        const cat = await getCatalog();
        const wanted = params.provider as ProviderId | undefined;
        const list = Object.values(cat);
        return wanted ? list.filter((m) => m.provider === wanted) : list;
      }
      case "cost.session": {
        const id = String(params.sessionId);
        const ctx = this.requireSession(id);
        return ctx.tracker.sessionBreakdown();
      }
      case "cost.lifetime": {
        const tracker = new CostTracker();
        return tracker.lifetimeBreakdown();
      }
      default:
        throw new Error(`Method not found: ${req.method}`);
    }
  }

  private requireSession(id: string): ActiveSession {
    const ctx = this.sessions.get(id);
    if (!ctx) throw new Error(`Unknown sessionId: ${id}`);
    return ctx;
  }

  private wireEmitter(sessionId: string, emitter: EventEmitter, transport: Transport): void {
    const fwd = (event: string) =>
      emitter.on(event, (data) => {
        transport.send({
          jsonrpc: "2.0",
          method: "session.event",
          params: { sessionId, part: { type: event, data } },
        });
      });
    for (const ev of ["text-delta", "tool-call", "tool-result", "finish", "compact-start", "compact-end", "error"]) {
      fwd(ev);
    }
  }
}

export function startStdioServer(): void {
  const server = new RpcServer();
  const transport: Transport = {
    send(msg) {
      process.stdout.write(JSON.stringify(msg) + "\n");
    },
  };
  const { feed } = server.attach(transport);
  process.stdin.on("data", feed);
  process.stdin.on("end", () => process.exit(0));
}

export function startSocketServer(): { server: Server; socketPath: string; pidPath: string } {
  const dir = join(getPiDir(), "agent");
  const socketPath = join(dir, "rpc.sock");
  const pidPath = join(dir, "rpc.pid");
  if (existsSync(socketPath)) unlinkSync(socketPath);

  const server = new RpcServer();
  const net = createServer((socket: Socket) => {
    const transport: Transport = {
      send(msg) {
        socket.write(JSON.stringify(msg) + "\n");
      },
    };
    const { feed } = server.attach(transport);
    socket.on("data", feed);
    socket.on("error", () => socket.destroy());
  });

  net.listen(socketPath, () => {
    writeFileSync(pidPath, String(process.pid));
  });

  return { server: net, socketPath, pidPath };
}
