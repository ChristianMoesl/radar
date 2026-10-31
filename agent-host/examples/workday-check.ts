import {
  createModels,
  createRegistry,
  defineDoc,
  defineTask,
  present,
  type AgentDefinition,
  type AgentEvent,
} from "../src/index.ts";

// A deterministic, read-only fixture. Real telemetry, observation windows,
// thresholds, reports and overlap policy belong in a separate definition.
const Report = defineDoc<{ runs: number; scheduledAt: string }>({
  kind: "example.workday-check",
  version: 1,
  scope: "session",
  initial: () => ({ runs: 0, scheduledAt: "" }),
});

const Check = defineTask<AgentEvent, { phase: "check" }, string>({
  name: "example.workday-check",
  version: 1,
  initial: () => ({ phase: "check" }),
  phases: {
    check: async (task, runtime, context) => {
      const payload = task.input.payload as {
        schedule?: { id?: string; name?: string; scheduledAt?: string };
        data?: { type?: string; windowHours?: number };
      } | null;
      const schedule = payload?.schedule;
      const windowHours = payload?.data?.windowHours;
      if (
        task.input.source !== "radar.schedule" ||
        !schedule?.id ||
        !schedule.name ||
        !schedule.scheduledAt ||
        !Number.isFinite(Date.parse(schedule.scheduledAt)) ||
        payload?.data?.type !== "daily.check" ||
        typeof windowHours !== "number" ||
        windowHours <= 0 ||
        !Number.isFinite(windowHours)
      ) {
        throw new Error("Expected a calendar daily.check event with a positive windowHours");
      }
      const end = new Date(schedule.scheduledAt);
      const start = new Date(end.getTime() - windowHours * 60 * 60 * 1000);
      return runtime.commit(async (tx) => {
        const report = await tx.doc(Report);
        report.runs++;
        report.scheduledAt = end.toISOString();
        const summary = `Fixture daily check: ${report.runs} occurrence(s); ${schedule.name}; observation window ${start.toISOString()} to ${end.toISOString()}. Synthetic evidence only.`;
        await present(tx, { title: "Workday check fixture", summary, attention: false });
        return { status: "terminal", outcome: { status: "completed", result: summary } };
      }, context);
    },
  },
  abort: async (_task, runtime, context) => runtime.commit(
    () => ({ status: "terminal", outcome: { status: "aborted" } }),
    context,
  ),
});

export default {
  version: 1,
  configure: (host) => {
    if (host.config.fixture !== true) throw new Error("The workday-check example requires fixture: true");
    return { models: createModels(), registry: createRegistry() };
  },
  eventTask: Check,
} satisfies AgentDefinition;
