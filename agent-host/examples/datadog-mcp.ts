import type { McpServices } from "../src/mcp.ts";
import type { JsonValue } from "../src/index.ts";

const READ_TOOLS = new Set([
  "get_datadog_metric",
  "search_datadog_monitors",
  "search_datadog_logs",
  "search_datadog_spans",
]);
export type DatadogRead = {
  tool: string;
  arguments: Record<string, JsonValue>;
  /** Native input field names from radar-mcp tools; no template language. */
  timeRange?: { from: string; to: string };
};

export function validateDatadogReads(
  value: unknown,
): Record<string, DatadogRead> {
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new Error("Configure Datadog reads with native MCP tool arguments");
  const reads = value as Record<string, DatadogRead>;
  if (Object.keys(reads).length > 10)
    throw new Error("Configure at most ten Datadog MCP reads");
  for (const [name, read] of Object.entries(reads)) {
    if (
      !name ||
      !read ||
      !READ_TOOLS.has(read.tool) ||
      !read.arguments ||
      typeof read.arguments !== "object" ||
      Array.isArray(read.arguments) ||
      (read.timeRange &&
        (typeof read.timeRange.from !== "string" ||
          typeof read.timeRange.to !== "string" ||
          !read.timeRange.from ||
          !read.timeRange.to ||
          read.timeRange.from === read.timeRange.to ||
          [read.timeRange.from, read.timeRange.to].some(
            (k) =>
              ["__proto__", "constructor", "prototype"].includes(k) ||
              Object.hasOwn(read.arguments, k),
          )))
    )
      throw new Error(
        "Datadog investigation permits configured metric, monitor, log and span MCP reads only",
      );
  }
  return reads;
}

export async function datadogRead(
  mcp: McpServices,
  server: string,
  read: DatadogRead,
  from: string,
  to: string,
  signal?: AbortSignal,
): Promise<JsonValue> {
  validateDatadogReads({ selected: read });
  const args = { ...read.arguments };
  if (read.timeRange) {
    if (
      !Number.isFinite(Date.parse(from)) ||
      !Number.isFinite(Date.parse(to)) ||
      Date.parse(from) > Date.parse(to)
    )
      throw new Error(
        "Datadog MCP time windows require ordered ISO timestamps",
      );
    args[read.timeRange.from] = from;
    args[read.timeRange.to] = to;
  }
  // Preserve MCP's structured content, text, sampling and truncation metadata.
  // A successful tool response is evidence, never a claim of exhaustive data.
  return (await mcp.call(
    server,
    read.tool,
    args,
    signal,
    true,
  )) as unknown as JsonValue;
}
