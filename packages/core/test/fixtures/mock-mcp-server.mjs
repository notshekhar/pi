#!/usr/bin/env node
/**
 * Minimal stdio MCP server for tests. Speaks line-delimited JSON-RPC 2.0 and
 * implements just enough of the protocol (initialize, tools/list, tools/call)
 * for the AI SDK MCP client to connect and call one tool.
 */
import { createInterface } from "node:readline";

const rl = createInterface({ input: process.stdin });

function send(msg) {
    process.stdout.write(`${JSON.stringify(msg)}\n`);
}

const TOOLS = [
    {
        name: "echo",
        description: "Echo back the provided text.",
        inputSchema: {
            type: "object",
            properties: { text: { type: "string" } },
            required: ["text"],
        },
    },
];

rl.on("line", (line) => {
    if (!line.trim()) return;
    let req;
    try {
        req = JSON.parse(line);
    } catch {
        return;
    }
    const { id, method, params } = req;

    if (method === "initialize") {
        send({
            jsonrpc: "2.0",
            id,
            result: {
                protocolVersion: params?.protocolVersion ?? "2024-11-05",
                capabilities: { tools: {} },
                serverInfo: { name: "mock-mcp", version: "0.0.0" },
            },
        });
        return;
    }
    if (method === "notifications/initialized") return; // notification, no reply
    if (method === "tools/list") {
        send({ jsonrpc: "2.0", id, result: { tools: TOOLS } });
        return;
    }
    if (method === "tools/call") {
        const text = params?.arguments?.text ?? "";
        send({
            jsonrpc: "2.0",
            id,
            result: { content: [{ type: "text", text: `echo: ${text}` }] },
        });
        return;
    }
    if (id !== undefined) {
        send({ jsonrpc: "2.0", id, error: { code: -32601, message: `method not found: ${method}` } });
    }
});
