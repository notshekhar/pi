/**
 * SOCKS5 forward proxy with domain-allowlist filtering.
 *
 * Ported/adapted from anthropic-experimental/sandbox-runtime (Apache-2.0):
 * https://github.com/anthropic-experimental/sandbox-runtime — see NOTICE.md.
 * Divergence: the upstream/parent-proxy chaining and SOCKS auth-token paths
 * are not ported; this dials destinations directly after the filter allows.
 */
import type { Server as NetServer, Socket } from "node:net";
import type { Socks5Server } from "@pondwader/socks5-server";
import { createServer } from "@pondwader/socks5-server";
import { logForDebugging } from "./debug";
import { dialDirect, isValidHost } from "./net-utils";

export interface SocksProxyServerOptions {
    filter(port: number, host: string): Promise<boolean> | boolean;
}

export interface SocksProxyWrapper {
    server: Socks5Server;
    getPort(): number | undefined;
    listen(port: number, hostname: string): Promise<number>;
    close(): Promise<void>;
    unref(): void;
}

export function createSocksProxyServer(options: SocksProxyServerOptions): SocksProxyWrapper {
    const socksServer = createServer();

    socksServer.setRulesetValidator(async (conn) => {
        try {
            const hostname = conn.destAddress;
            const port = conn.destPort;
            // SOCKS5 DOMAINNAME is unvalidated bytes — reject control chars
            // before they reach the allowlist matcher.
            if (!isValidHost(hostname)) {
                logForDebugging(`Rejecting malformed SOCKS host: ${JSON.stringify(hostname)}`, { level: "error" });
                return false;
            }
            const allowed = await options.filter(port, hostname);
            if (!allowed) {
                logForDebugging(`SOCKS connection blocked to ${hostname}:${port}`, { level: "error" });
                return false;
            }
            return true;
        } catch (error) {
            logForDebugging(`Error validating SOCKS connection: ${error}`, { level: "error" });
            return false;
        }
    });

    socksServer.setConnectionHandler((conn, sendStatus) => {
        const host = conn.destAddress;
        const port = conn.destPort;

        let clientGone = false;
        let upstreamRef: Socket | undefined;
        conn.socket.once("close", () => {
            clientGone = true;
            upstreamRef?.destroy();
        });
        conn.socket.on("error", () => upstreamRef?.destroy());

        dialDirect(host, port)
            .then((upstream) => {
                upstreamRef = upstream;
                upstream.on("error", () => conn.socket.destroy());
                if (clientGone) {
                    upstream.destroy();
                    return;
                }
                sendStatus("REQUEST_GRANTED");
                upstream.pipe(conn.socket);
                conn.socket.pipe(upstream);
                upstream.on("close", () => conn.socket.destroy());
            })
            .catch((err) => {
                logForDebugging(`SOCKS connect to ${host}:${port} failed: ${(err as Error).message}`, {
                    level: "error",
                });
                if (!clientGone) {
                    try {
                        sendStatus("HOST_UNREACHABLE");
                    } catch {
                        // socket may have closed between the check and the write
                    }
                }
            });
    });

    // Track accepted sockets so close() can tear them down immediately rather
    // than wait for in-flight relays/dials to drain (which can hang reset()).
    const internalServer = (socksServer as unknown as { server?: NetServer })?.server;
    const openSockets = new Set<Socket>();
    internalServer?.on("connection", (socket: Socket) => {
        openSockets.add(socket);
        socket.once("close", () => openSockets.delete(socket));
    });

    return {
        server: socksServer,
        getPort(): number | undefined {
            try {
                if (internalServer && typeof internalServer.address === "function") {
                    const address = internalServer.address();
                    if (address && typeof address === "object" && "port" in address) return address.port;
                }
            } catch (error) {
                logForDebugging(`Error getting SOCKS port: ${error}`, { level: "error" });
            }
            return undefined;
        },
        listen(port: number, hostname: string): Promise<number> {
            return new Promise((resolve, reject) => {
                internalServer?.once("error", reject);
                const onListening = (): void => {
                    internalServer?.removeListener("error", reject);
                    const actualPort = this.getPort();
                    if (actualPort) resolve(actualPort);
                    else reject(new Error("Failed to get SOCKS proxy server port"));
                };
                socksServer.listen(port, hostname, onListening);
            });
        },
        async close(): Promise<void> {
            return new Promise((resolve, reject) => {
                socksServer.close((error) => {
                    if (error) {
                        const msg = error.message?.toLowerCase() || "";
                        const alreadyClosed =
                            msg.includes("not running") ||
                            msg.includes("already closed") ||
                            msg.includes("not listening");
                        if (!alreadyClosed) {
                            reject(error);
                            return;
                        }
                    }
                    resolve();
                });
                for (const socket of openSockets) socket.destroy();
                openSockets.clear();
            });
        },
        unref(): void {
            try {
                if (internalServer && typeof internalServer.unref === "function") internalServer.unref();
            } catch (error) {
                logForDebugging(`Error calling SOCKS unref: ${error}`, { level: "error" });
            }
        },
    };
}
