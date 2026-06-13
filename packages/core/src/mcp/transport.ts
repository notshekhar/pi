/**
 * Maps a server config entry to an AI SDK MCP transport. stdio entries become
 * a StdioMCPTransport instance; http/sse entries become a plain transport
 * config object the client turns into a streamable-HTTP (or SSE) transport.
 */
import { Experimental_StdioMCPTransport } from "@ai-sdk/mcp/mcp-stdio";
import type { MCPClientConfig, OAuthClientProvider } from "@ai-sdk/mcp";
import { isHttpServer, resolveSecretMap, type McpServerConfig } from "./config";

// The client accepts either a transport-config object (http/sse) or a custom
// transport instance (stdio). MCPTransportConfig isn't re-exported, so derive
// the accepted shape straight from the client config.
export type TransportInput = MCPClientConfig["transport"];

/**
 * `authProvider` is supplied only for OAuth servers (Phase B). Static-header
 * auth needs nothing here — the headers ride the http transport config.
 */
export function buildTransport(cfg: McpServerConfig, authProvider?: OAuthClientProvider): TransportInput {
    if (isHttpServer(cfg)) {
        return {
            type: cfg.type,
            url: cfg.url,
            headers: resolveSecretMap(cfg.headers),
            authProvider,
        };
    }
    return new Experimental_StdioMCPTransport({
        command: cfg.command,
        args: cfg.args,
        env: resolveSecretMap(cfg.env),
    });
}
