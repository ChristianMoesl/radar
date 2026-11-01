import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

// Names are assigned by Pi from the exact MCP server name. Do not infer
// availability from mcp.json, credentials, unrelated servers or tool summaries.
export function isLinearTool(name: string): boolean {
  return name.startsWith("mcp__linear__");
}

export function linearAvailable(pi: ExtensionAPI): boolean {
  const active = new Set(pi.getActiveTools());
  return pi.getAllTools().some(tool => {
    const exposure = (tool as typeof tool & { exposure?: string }).exposure ?? "direct";
    return /^(?:mcp__linear__)(?:create_issue|save_issue)$/.test(tool.name) && exposure !== "hidden" &&
      (active.has(tool.name) || ((exposure === "codemode" || exposure === "deferred") &&
        (active.has("codemode") || active.has("tool_search"))));
  });
}
