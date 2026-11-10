import { defineAgent } from "../src/index.ts";

// Plain JavaScript syntax: no provider setup, task phases, query pipeline or docs.
// Configure/authenticate the named host MCP profile once, outside this definition.
export default defineAgent({
  name: "datadog-health",
  instructions: "Investigate the supplied Datadog alert using read-only telemetry. Choose relevant queries yourself. Report observations, query windows, sampling limits, hypotheses and concrete next actions. Missing data never proves health. Do not remediate.",
  model: { provider: "openai", modelId: "gpt-4.1-mini" },
  mcp: {
    placement: "host",
    connections: [{ server: "datadog", tools: [
      "search_datadog_monitors", "get_datadog_metric", "search_datadog_logs",
    ] }],
  },
});
