import { createInterface } from "node:readline";
import { readFileSync, appendFileSync } from "node:fs";
const [settingsPath, callsPath] = process.argv.slice(2);
for await (const line of createInterface({ input: process.stdin })) {
  const message = JSON.parse(line);
  if (message.id === undefined) continue;
  const settings = JSON.parse(readFileSync(settingsPath, "utf8"));
  const schema = {
    type: "object", properties: { count: { type: "integer", minimum: 1, maximum: 5 } },
    required: ["count"], additionalProperties: false,
  };
  const read = { name: "read", description: "Read fixture evidence", inputSchema: settings.invalidSchema ? { type: "bad" } : schema, annotations: { readOnlyHint: true } };
  let result;
  switch (message.method) {
    case "initialize": result = { protocolVersion: "2025-11-25", capabilities: { tools: {} }, serverInfo: { name: "fixture", version: "1" } }; break;
    case "tools/list": result = { tools: [
      ...(!settings.missing ? [read] : []),
      ...(settings.duplicate ? [read] : []),
      { ...read, name: "write", annotations: { readOnlyHint: false } },
      ...(settings.extra ? [{ ...read, name: "new_read" }] : []),
    ] }; break;
    case "tools/call": {
      appendFileSync(callsPath, JSON.stringify(message.params) + "\n");
      if (settings.delay) await new Promise(resolve => setTimeout(resolve, 10000));
      result = { content: [{ type: "text", text: settings.oversized ? "x".repeat(1024 * 1024) : "synthetic evidence" }], structuredContent: { count: message.params.arguments.count },
        ...(settings.partial ? { _meta: { partial: true } } : {}), ...(settings.error ? { isError: true } : {}) };
      break;
    }
    default: throw new Error("unexpected fixture method");
  }
  process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id: message.id, result }) + "\n");
}
