import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout } from "node:timers/promises";
import {
  createModels,
  createRegistry,
  defineDoc,
  defineTask,
  present,
  type AgentDefinition,
  type AgentEvent,
} from "../../src/index.ts";

let dataDir: string;
const Counter = defineDoc<{ count: number }>({
  kind: "fixture.recovery-count",
  scope: "session",
  version: 1,
  initial: () => ({ count: 0 }),
});
const Work = defineTask<
  AgentEvent,
  { phase: "start" } | { phase: "finish" },
  number
>({
  name: "fixture.recovery",
  version: 1,
  initial: () => ({ phase: "start" }),
  phases: {
    start: async (_task, runtime, context) => {
      await runtime.commit(
        () => ({ status: "running", checkpoint: { phase: "finish" } }),
        context,
      );
    },
    finish: async (_task, runtime, context) => {
      await writeFile(join(dataDir, "started"), "finish");
      await setTimeout(750, undefined, { signal: runtime.signal });
      await runtime.commit(async (tx) => {
        const doc = await tx.doc(Counter);
        doc.count++;
        await present(tx, { summary: `${doc.count} completed` });
        return {
          status: "terminal",
          outcome: { status: "completed", result: doc.count },
        };
      }, context);
    },
  },
  abort: async (_task, runtime, context) => {
    await runtime.commit(
      () => ({ status: "terminal", outcome: { status: "aborted" } }),
      context,
    );
  },
});
export default {
  version: 1,
  configure: (host) => {
    dataDir = host.dataDir;
    return { models: createModels(), registry: createRegistry() };
  },
  eventTask: Work,
} satisfies AgentDefinition;
