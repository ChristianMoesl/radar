import {
  createModels, createRegistry, defineDoc, defineTask, present,
  type AgentDefinition, type AgentEvent, type JsonValue,
} from "../src/index.ts";
import { createMcpTools } from "../src/mcp-tools.ts";

// A deterministic MCP qualification example, not an anomaly detector. All reads
// and arguments come from trusted non-secret definition config, never the event.
type Read = { server: string; tool: string; arguments: Record<string, JsonValue> };
let selected: Awaited<ReturnType<typeof createMcpTools>>;
let reads: Read[];
const Evidence = defineDoc<{ occurrence: string; reads: JsonValue[]; errors: string[]; inspection: JsonValue }>({
  kind: "example.mcp-check", version: 1, scope: "session",
  initial: () => ({ occurrence: "", reads: [], errors: [], inspection: null }),
});
const Check = defineTask<AgentEvent, { phase: "read" }, JsonValue>({
  name: "example.mcp-check", version: 1, initial: () => ({ phase: "read" }),
  phases: {
    read: async (task, runtime, context) => {
      const event = task.input.payload as { schedule?: { id?: string; scheduledAt?: string } } | null;
      const occurrence = task.input.source === "radar.schedule"
        ? event?.schedule?.scheduledAt : task.input.id;
      if (!occurrence || (task.input.source === "radar.schedule" &&
          (!event?.schedule?.id || !Number.isFinite(Date.parse(occurrence)))))
        throw new Error("Calendar MCP check requires its original schedule identity and intended time");
      const results: JsonValue[] = [], errors: string[] = [];
      for (const [index, read] of reads.entries()) {
        try {
          const key = `mcp-read-${index}`;
          let result = await runtime.memo(key, context);
          if (result === undefined) {
            const evidence = await selected.read(read.server, read.tool, read.arguments, runtime.signal);
            result = await runtime.memo(key, evidence as unknown as JsonValue, context);
          }
          results.push({ server: read.server, tool: read.tool, evidence: result });
        } catch {
          runtime.signal.throwIfAborted();
          errors.push(`Configured MCP read ${index + 1} failed; evidence is incomplete`);
        }
      }
      return runtime.commit(async tx => {
        const evidence = await tx.doc(Evidence);
        Object.assign(evidence, { occurrence, reads: results, errors,
          inspection: selected.inspection as unknown as JsonValue });
        const summary = errors.length
          ? `Incomplete MCP check: ${errors.join("; ")}`
          : `Collected ${results.length} configured MCP read(s). Evidence only, not a health assessment.`;
        await present(tx, {
          title: "Scoped MCP check", summary: `${summary}\n${JSON.stringify(evidence)}`,
          attention: errors.length > 0, message: errors.length > 0,
        });
        return { status: "terminal", outcome: { status: "completed", result: {
          partial: errors.length > 0, occurrence, reads: results, errors,
        } } };
      }, context);
    },
  },
  abort: async (_task, runtime, context) => runtime.commit(
    () => ({ status: "terminal", outcome: { status: "aborted" } }), context),
});

export default {
  version: 1,
  configure: async host => {
    const configured = host.config.reads;
    if (!Array.isArray(configured) || !configured.length || configured.length > 10)
      throw new Error("Configure one to ten fixed MCP reads");
    selected = await createMcpTools(host.execution, host.config.mcp);
    reads = structuredClone(configured) as Read[];
    for (const read of reads) {
      if (!read || typeof read !== "object" ||
          Object.keys(read).some(key => !["server", "tool", "arguments"].includes(key)) ||
          !read.arguments || typeof read.arguments !== "object" || Array.isArray(read.arguments) ||
          !selected.inspection.connections.some(binding => binding.server === read.server && binding.tool === read.tool))
        throw new Error("Each fixed read must name an explicitly selected server/tool and native arguments");
    }
    const registry = createRegistry(); registry.install(selected.extension);
    return { models: createModels(), registry };
  },
  eventTask: Check,
} satisfies AgentDefinition;
