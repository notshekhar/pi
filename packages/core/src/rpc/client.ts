import { createConnection, type Socket } from "node:net";
import { EventEmitter } from "node:events";
import type { RpcRequest, RpcResponse, RpcNotification } from "./protocol";

export class RpcClient extends EventEmitter {
    private socket: Socket;
    private buffer = "";
    private nextId = 1;
    private pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: unknown) => void }>();

    constructor(socketPath: string) {
        super();
        this.socket = createConnection(socketPath);
        this.socket.on("data", (chunk: Buffer) => {
            this.buffer += chunk.toString();
            let idx: number;
            while ((idx = this.buffer.indexOf("\n")) >= 0) {
                const line = this.buffer.slice(0, idx).trim();
                this.buffer = this.buffer.slice(idx + 1);
                if (line) this.handle(line);
            }
        });
        this.socket.on("error", (err) => this.emit("error", err));
    }

    private handle(line: string): void {
        const msg = JSON.parse(line) as RpcResponse | RpcNotification;
        if ("id" in msg && msg.id !== null && msg.id !== undefined) {
            const pending = this.pending.get(msg.id as number);
            if (!pending) return;
            this.pending.delete(msg.id as number);
            if (msg.error) pending.reject(msg.error);
            else pending.resolve(msg.result);
        } else if ("method" in msg) {
            this.emit("notification", msg);
            this.emit(msg.method, msg.params);
        }
    }

    call<T = unknown>(method: string, params?: unknown): Promise<T> {
        const id = this.nextId++;
        const req: RpcRequest = { jsonrpc: "2.0", id, method, params };
        return new Promise((resolve, reject) => {
            this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject });
            this.socket.write(JSON.stringify(req) + "\n");
        });
    }

    close(): void {
        this.socket.end();
    }
}
