import { createHash } from "node:crypto";
import { isAbsolute } from "node:path";
import { defineExtension, defineTool } from "@earendil-works/pi-durable";
import type { TSchema } from "typebox";
import type { Tool, CallToolResult } from "@earendil-works/pi-mcp";
import { snapshotMcpSelection, type McpServices, type McpToolSelection } from "./mcp.ts";
import { mcpInputValidator } from "./mcp-schema.ts";
import type { WorkspaceExecution } from "./environment.ts";

const MAX_BYTES = 1024 * 1024;

// Imported in the selected workspace, never from the host installation. That
// environment owns its MCP config, OAuth store, server processes and packages.
const bridge = String.raw`
import {McpServices} from '@christianmoesl/radar-agent-host/mcp';
const request=JSON.parse(process.argv[1]);
const client=new McpServices(request.paths,request.connections);
const controller=new AbortController();
process.once('SIGTERM',()=>controller.abort());
process.once('SIGINT',()=>controller.abort());
try {
 const result=request.operation==='tools'
   ? await client.tools(request.server,controller.signal)
   : await client.call(request.server,request.tool,request.args,controller.signal,true);
 process.stdout.write(JSON.stringify({ok:true,result}));
} catch {
 process.stdout.write(JSON.stringify({ok:false}));
} finally {await client.close()}
`;

export function mcpModelToolName(server: string, tool: string): string {
  return `mcp_${createHash("sha256").update(`${server}\0${tool}`).digest("hex").slice(0, 40)}`;
}

/** Non-secret persisted definition configuration. Paths are interpreted only
 * inside the selected workspace execution environment. */
export type WorkspaceMcpConfig = {
  config: string;
  state: string;
  connections: { server: string; tools: string[] }[];
};

/** Definition-owned native tools. No unrestricted model dispatcher is installed. */
export async function createMcpTools(execution: WorkspaceExecution, value: unknown) {
  const config = value as WorkspaceMcpConfig | undefined;
  if (!config || typeof config !== "object" ||
      Object.keys(config).some(key => !["config", "state", "connections"].includes(key)) ||
      typeof config.config !== "string" || !isAbsolute(config.config) ||
      typeof config.state !== "string" || !isAbsolute(config.state))
    throw new Error("Configure non-secret absolute workspace MCP config/state paths and exact connections");
  const connections = snapshotMcpSelection(config.connections);
  const paths = { config: config.config, state: config.state };
  const binding = { ...execution.binding };

  async function request<T>(operation: "tools" | "call", server: string,
    tool?: string, args?: Record<string, unknown>, signal?: AbortSignal): Promise<T> {
    // This closed-over selection is never taken from model arguments or a tool
    // refresh. Recheck immediately before invoking the workspace bridge.
    const selected = connections.find(connection => connection.server === server);
    if (!selected || (tool !== undefined && !selected.tools.includes(tool)))
      throw new Error("MCP server or tool is not explicitly selected");
    const encoded = JSON.stringify({ operation, server, tool, args, paths, connections });
    if (Buffer.byteLength(encoded) > MAX_BYTES) throw new Error("MCP request exceeds the limit");
    try {
      signal?.throwIfAborted();
      const text = await execution.exec(
        ["node", "--input-type=module", "-e", bridge, encoded], binding.path, signal);
      if (Buffer.byteLength(text) > MAX_BYTES) throw new Error();
      const response = JSON.parse(text);
      if (response.ok !== true) throw new Error();
      return response.result as T;
    } catch {
      // Exec errors can contain argv, stderr or credentials. Never expose them.
      throw new Error(`Workspace MCP ${server} failed in ${binding.mode}; verify the selected environment, package, server and authentication. No host fallback was attempted.`);
    }
  }

  return selectedMcpTools(connections, request, binding);
}

/** Trusted host-service placement is explicit, never a workspace fallback. */
export async function createHostMcpTools(parent: McpServices, value: unknown) {
  const connections = snapshotMcpSelection(value);
  const client = parent.scoped(connections);
  async function request<T>(operation: "tools" | "call", server: string,
    tool?: string, args?: Record<string, unknown>, signal?: AbortSignal): Promise<T> {
    const selected = connections.find(connection => connection.server === server);
    if (!selected || (tool !== undefined && !selected.tools.includes(tool)))
      throw new Error("MCP server or tool is not explicitly selected");
    if (operation === "call")
      return await client.call(server, tool!, args!, signal, true) as T;
    const catalog = await client.tools(server, signal);
    return catalog.filter(tool => selected.tools.includes(tool.name)) as T;
  }
  return selectedMcpTools(connections, request, { mode: "host-service" as const });
}

async function selectedMcpTools<Binding extends object>(
  connections: McpToolSelection[],
  request: <T>(operation: "tools" | "call", server: string, tool?: string,
    args?: Record<string, unknown>, signal?: AbortSignal) => Promise<T>,
  binding: Binding,
) {
  const bindings: { server: string; tool: string; modelName: string }[] = [];
  const validators = new Map<string, (args: unknown) => void>();
  async function read(server: string, tool: string, args: Record<string, unknown>, signal?: AbortSignal) {
    const validate = validators.get(mcpModelToolName(server, tool));
    if (!validate) throw new Error("MCP server or tool is not explicitly selected");
    validate(args);
    const result = await request<CallToolResult>("call", server, tool, args, signal);
    const meta = (result as { _meta?: { partial?: boolean; truncated?: boolean } })._meta;
    const structured = result.structuredContent as { partial?: boolean; truncated?: boolean } | undefined;
    if (result.isError || meta?.partial || meta?.truncated || structured?.partial || structured?.truncated)
      throw new Error("MCP evidence is incomplete; do not report an all-clear");
    return result;
  }
  const tools = [];
  for (const connection of connections) {
    if (!connection.tools.length) continue;
    const catalog = await request<Tool[]>("tools", connection.server);
    if (!Array.isArray(catalog) || catalog.length !== connection.tools.length ||
        new Set(catalog.map(tool => tool.name)).size !== catalog.length ||
        catalog.some(tool => !connection.tools.includes(tool.name) || tool.annotations?.readOnlyHint !== true))
      throw new Error("Selected workspace MCP catalog is missing, ambiguous or not read-only");
    for (const tool of catalog) {
      const parameters = structuredClone(tool.inputSchema) as TSchema;
      const validate = mcpInputValidator(parameters);
      const name = mcpModelToolName(connection.server, tool.name);
      validators.set(name, validate);
      bindings.push({ server: connection.server, tool: tool.name, modelName: name });
      tools.push(defineTool({
        name,
        description: `[${connection.server}/${tool.name}] ${tool.description ?? "Selected read-only MCP tool"}`,
        parameters,
        replay: "safe",
        outputLimits: { maxBytes: MAX_BYTES, maxLines: 10000 },
        // Native tooling otherwise may coerce model arguments before execute.
        prepareArguments: args => { validate(args); return args; },
        execute: async (args, _api, context) => {
          const result = await read(connection.server, tool.name, args as Record<string, unknown>, context.abortSignal);
          // Keep native content, structured evidence and metadata together rather
          // than silently dropping provider sampling/completeness information.
          return { output: [{ type: "text" as const, text: JSON.stringify(result) }] };
        },
      }));
    }
  }
  if (new Set(bindings.map(binding => binding.modelName)).size !== bindings.length)
    throw new Error("MCP model tool names are ambiguous");
  return {
    extension: defineExtension({ name: "radar.mcp", tools }),
    inspection: { execution: binding, connections: bindings },
    // For the definition's own native event tasks; not a registered model tool.
    read,
  };
}
