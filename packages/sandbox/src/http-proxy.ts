/**
 * HTTP forward proxy with domain-allowlist filtering. Handles HTTPS via the
 * CONNECT method (opaque TCP tunnel) and plain HTTP via absolute-form requests.
 *
 * Adapted from anthropic-experimental/sandbox-runtime (Apache-2.0):
 * https://github.com/anthropic-experimental/sandbox-runtime — see NOTICE.md.
 * Divergence: no MITM/TLS termination, no upstream/parent-proxy chaining, no
 * proxy-auth token, no per-request filterRequest callback (those are the
 * experimental/optional upstream features). Host-level allow/deny is enforced.
 */
import http from "node:http";
import type { Socket } from "node:net";
import { logForDebugging } from "./debug";
import { dialDirect, isValidHost, stripBrackets } from "./net-utils";

export interface HttpProxyServerOptions {
    filter(port: number, host: string): Promise<boolean> | boolean;
}

/** Parse "host:port" (incl. bracketed IPv6) into host + port with a default. */
function parseAuthority(authority: string, defaultPort: number): { host: string; port: number } {
    const trimmed = authority.trim();
    if (trimmed.startsWith("[")) {
        const close = trimmed.indexOf("]");
        const host = trimmed.slice(1, close);
        const rest = trimmed.slice(close + 1);
        const port = rest.startsWith(":") ? Number(rest.slice(1)) : defaultPort;
        return { host, port: Number.isInteger(port) ? port : defaultPort };
    }
    const colon = trimmed.lastIndexOf(":");
    if (colon === -1) return { host: trimmed, port: defaultPort };
    const port = Number(trimmed.slice(colon + 1));
    return { host: trimmed.slice(0, colon), port: Number.isInteger(port) ? port : defaultPort };
}

/** An http.Server configured as a filtered forward proxy. */
export function createHttpProxyServer(options: HttpProxyServerOptions): http.Server {
    const server = http.createServer();

    // HTTPS / arbitrary TCP: CONNECT host:port, filter, then blind-tunnel.
    server.on("connect", (req: http.IncomingMessage, clientSocket: Socket, head: Buffer) => {
        const { host, port } = parseAuthority(req.url ?? "", 443);
        const bare = stripBrackets(host);

        const reject = (status: string, reason: string) => {
            logForDebugging(`CONNECT ${host}:${port} ${reason}`, { level: "error" });
            try {
                clientSocket.write(`HTTP/1.1 ${status}\r\nX-Proxy-Error: blocked-by-loop-sandbox\r\n\r\n`);
            } catch {
                // client may already be gone
            }
            clientSocket.destroy();
        };

        if (!isValidHost(bare)) return reject("400 Bad Request", "malformed host");

        Promise.resolve(options.filter(port, bare))
            .then((allowed) => {
                if (!allowed) return reject("403 Forbidden", "blocked by allowlist");
                dialDirect(bare, port)
                    .then((upstream) => {
                        clientSocket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
                        if (head && head.length) upstream.write(head);
                        upstream.pipe(clientSocket);
                        clientSocket.pipe(upstream);
                        const teardown = () => {
                            upstream.destroy();
                            clientSocket.destroy();
                        };
                        upstream.on("error", teardown);
                        clientSocket.on("error", teardown);
                        upstream.on("close", () => clientSocket.destroy());
                    })
                    .catch((err) => reject("502 Bad Gateway", `upstream dial failed: ${(err as Error).message}`));
            })
            .catch((err) => reject("403 Forbidden", `filter error: ${(err as Error).message}`));
    });

    // Plain HTTP: absolute-form request URL (http://host/path). Filter the host,
    // then forward the request and stream the response back.
    server.on("request", (req: http.IncomingMessage, res: http.ServerResponse) => {
        const url = req.url ?? "";
        let target: URL;
        try {
            target = new URL(url);
        } catch {
            res.writeHead(400, { "X-Proxy-Error": "blocked-by-loop-sandbox" });
            res.end("malformed absolute-form request URL\n");
            return;
        }
        const host = stripBrackets(target.hostname);
        const port = target.port ? Number(target.port) : 80;

        const deny = (status: number, reason: string) => {
            logForDebugging(`HTTP ${host}:${port} ${reason}`, { level: "error" });
            if (!res.headersSent) {
                res.writeHead(status, { "Content-Type": "text/plain", "X-Proxy-Error": "blocked-by-loop-sandbox" });
            }
            res.end(reason + "\n");
        };

        if (!isValidHost(host)) return deny(400, "malformed host");

        Promise.resolve(options.filter(port, host))
            .then((allowed) => {
                if (!allowed) return deny(403, "blocked by allowlist");
                const upstream = http.request(
                    {
                        host,
                        port,
                        method: req.method,
                        path: target.pathname + target.search,
                        headers: req.headers,
                    },
                    (upRes) => {
                        res.writeHead(upRes.statusCode ?? 502, upRes.headers);
                        upRes.pipe(res);
                    },
                );
                upstream.on("error", (err) => deny(502, `upstream request failed: ${err.message}`));
                req.pipe(upstream);
            })
            .catch((err) => deny(403, `filter error: ${(err as Error).message}`));
    });

    return server;
}
