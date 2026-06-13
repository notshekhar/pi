/**
 * Localhost HTTP listener that catches the OAuth redirect (`?code=&state=`).
 * Mirrors the xAI OAuth callback pattern in auth/xai-oauth.ts.
 */
import { createServer, type Server } from "node:http";

const CALLBACK_HOST = process.env.PI_MCP_OAUTH_CALLBACK_HOST || "127.0.0.1";
const CALLBACK_PATH = "/callback";
/** Try a stable port first (nicer for allow-listed redirect URIs), else any. */
const PREFERRED_PORT = Number(process.env.PI_MCP_OAUTH_CALLBACK_PORT) || 8976;

export interface CallbackResult {
    code: string;
    state?: string;
}

export interface CallbackServer {
    redirectUri: string;
    waitForCode(timeoutMs: number): Promise<CallbackResult>;
    close(): void;
}

export async function startCallbackServer(): Promise<CallbackServer> {
    let resolveResult: (r: CallbackResult) => void;
    let rejectResult: (e: Error) => void;
    const resultPromise = new Promise<CallbackResult>((resolve, reject) => {
        resolveResult = resolve;
        rejectResult = reject;
    });

    const server = createServer((req, res) => {
        const url = new URL(req.url ?? "/", `http://${CALLBACK_HOST}`);
        if (url.pathname !== CALLBACK_PATH) {
            res.writeHead(404).end();
            return;
        }
        const error = url.searchParams.get("error");
        const code = url.searchParams.get("code");
        res.writeHead(200, { "content-type": "text/html" });
        if (error) {
            res.end(page(`Authorization failed: ${error}`));
            rejectResult(new Error(`OAuth error: ${error}`));
            return;
        }
        if (!code) {
            res.end(page("Authorization failed: no code returned"));
            rejectResult(new Error("OAuth callback missing authorization code"));
            return;
        }
        res.end(page("Authorized. You can close this tab and return to pi."));
        resolveResult({ code, state: url.searchParams.get("state") ?? undefined });
    });

    const port = await listen(server);
    return {
        redirectUri: `http://${CALLBACK_HOST}:${port}${CALLBACK_PATH}`,
        waitForCode: (timeoutMs) =>
            Promise.race([
                resultPromise,
                new Promise<never>((_, reject) =>
                    setTimeout(() => reject(new Error("timed out waiting for OAuth callback")), timeoutMs),
                ),
            ]),
        close: () => server.close(),
    };
}

function listen(server: Server): Promise<number> {
    const tryPort = (port: number) =>
        new Promise<number>((resolve, reject) => {
            server.once("error", reject);
            server.listen(port, CALLBACK_HOST, () => {
                server.removeAllListeners("error");
                const addr = server.address();
                resolve(typeof addr === "object" && addr ? addr.port : port);
            });
        });
    return tryPort(PREFERRED_PORT).catch(() => tryPort(0));
}

function page(message: string): string {
    return `<!doctype html><meta charset="utf-8"><body style="font-family:system-ui;padding:3rem;text-align:center"><p>${message}</p></body>`;
}
