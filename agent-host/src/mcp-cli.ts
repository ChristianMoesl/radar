#!/usr/bin/env node
import { McpServices } from "./mcp.ts";

process.umask(0o077);
const [operation, server] = process.argv.slice(2);
const mcp = new McpServices();
try {
  if (!server || !["login", "tools"].includes(operation!))
    throw new Error(
      "Usage: radar-mcp login <profile> | radar-mcp tools <profile>",
    );
  if (operation === "login") {
    await mcp.login(server, (url) =>
      process.stdout.write(`Approve MCP access in your browser:\n${url}\n`),
    );
    process.stdout.write("MCP OAuth login completed.\n");
  }
  const tools = await mcp.tools(server);
  process.stdout.write(
    JSON.stringify(
      tools.map(({ name, description, inputSchema, annotations }) => ({
        name,
        description,
        inputSchema,
        annotations,
      })),
      null,
      2,
    ) + "\n",
  );
} catch (error) {
  process.stderr.write((error as Error).message + "\n");
  process.exitCode = 1;
} finally {
  await mcp.close();
}
