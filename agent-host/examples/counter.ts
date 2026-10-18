import {
  createModels,
  createRegistry,
  defineDoc,
  defineTask,
  present,
  type AgentDefinition,
  type AgentEvent,
} from "../src/index.ts";

const Counter = defineDoc<{ value: number }>({
  kind: "example.counter",
  version: 1,
  scope: "session",
  initial: () => ({ value: 0 }),
});
const Count = defineTask<AgentEvent, { phase: "count" }, number>({
  name: "example.count",
  version: 1,
  initial: () => ({ phase: "count" }),
  phases: {
    count: async (_task, runtime, context) => {
      await runtime.commit(async (tx) => {
        const counter = await tx.doc(Counter);
        counter.value++;
        await present(tx, {
          title: "Counter",
          summary: `${counter.value} events received`,
          message: true,
          actions: [
            {
              id: "count",
              label: "Count again",
              payload: { operation: "count" },
            },
          ],
        });
        return {
          status: "terminal",
          outcome: { status: "completed", result: counter.value },
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
  configure: () => ({ models: createModels(), registry: createRegistry() }),
  eventTask: Count,
} satisfies AgentDefinition;
