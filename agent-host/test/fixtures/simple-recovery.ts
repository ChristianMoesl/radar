import { access, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fauxProvider, fauxAssistantMessage, fauxToolCall } from "@earendil-works/pi-ai/providers/faux";
import { createModels, defineAgent } from "../../src/index.ts";
import { mcpModelToolName } from "../../src/mcp-tools.ts";

const directory = process.env.RADAR_SIMPLE_RECOVERY_DIR!;
const faux = fauxProvider();
faux.setResponses(Array.from({ length: 20 }, () => async (context, options) => {
  if (!context.messages.some(message => message.role === "toolResult"))
    return fauxAssistantMessage(fauxToolCall(mcpModelToolName("fixture", "read"), { count: 1 }), { stopReason: "toolUse" });
  await writeFile(join(directory, "answer-started"), "ready");
  while (true) {
    try { await access(join(directory, "allow-answer")); break; } catch {}
    await delay(20, undefined, { signal: options?.signal });
  }
  return fauxAssistantMessage("Recovered fixture investigation; evidence and limits retained.");
}));
export default defineAgent({
  name: "simple-recovery", instructions: "Read the selected fixture tool", model: { provider: "faux", modelId: "faux-1" },
  models: () => { const models = createModels(); models.setProvider(faux.provider); return models; },
  mcp: { placement: "host", connections: [{ server: "fixture", tools: ["read"] }] },
  limits: { maxToolCalls: 1, maxTurns: 2, maxDurationMs: 20000 },
});
