import { resolve } from "node:path";
import { withAbortSignal } from "@earendil-works/chord/context";
import {
  configure,
  createRegistry,
  defineDoc,
  defineDocFamily,
  defineTask,
  present,
  PresentationDoc,
  type AgentDefinition,
  type AgentEvent,
  type ConversationId,
  type JsonValue,
  type HostContext,
  taskId,
} from "../src/index.ts";
import {
  createReadOnlyTools,
  prepareWorkspace,
  previewWorkspace,
  radar,
  withWorkspaceLease,
  type LoadedSkill,
  type WorkspacePlan,
  type WorkspaceRequest,
} from "../src/tools.ts";
import { agentChoices, answerText, modelRuntime } from "./shared.ts";

type ReviewRequest = {
  repository: string;
  local: string;
  number: number;
  head: string;
  base: string;
  url: string;
};
type RecordState = {
  request: ReviewRequest | null;
  stage: string;
  conversation: number;
  workspace: string;
  plan: JsonValue;
  report: string;
  skillHash: string;
  task: number;
};
const Reviews = defineDocFamily<RecordState, null>({
  kind: "review.records",
  version: 1,
  scope: "session",
  family: true,
  initial: () => ({
    request: null,
    stage: "",
    conversation: 0,
    workspace: "",
    plan: null,
    report: "",
    skillHash: "",
    task: 0,
  }),
});
const Index = defineDoc<{
  heads: Record<string, string>;
  active: Record<string, boolean>;
  selected: string;
}>({
  kind: "review.index",
  version: 1,
  scope: "session",
  initial: () => ({ heads: {}, active: {}, selected: "" }),
});
type State =
  | { phase: "route" }
  | {
      phase: "prepare";
      key: string;
      request: ReviewRequest;
      approved?: boolean;
    }
  | { phase: "run"; key: string; request: ReviewRequest; text?: string };

let settings: {
  host: HostContext;
  id: string;
  reviewer: string;
  repositories: Record<string, string>;
  fixture: boolean;
  skill: LoadedSkill;
  runtime: ReturnType<typeof modelRuntime>;
};

function reviewName(request: ReviewRequest) {
  return `review-${request.repository.replace(/[^a-zA-Z0-9]/g, "-").slice(0, 40)}-${request.number}-${request.head.slice(0, 12)}`;
}

function git(args: string[], cwd: string, signal?: AbortSignal) {
  return settings.host.execution.exec(
    [
      "env",
      "GIT_OPTIONAL_LOCKS=0",
      "GIT_TERMINAL_PROMPT=0",
      "git",
      "-C",
      cwd,
      ...args,
    ],
    settings.host.workspace.path,
    signal,
  );
}

const Review = defineTask<AgentEvent, State, JsonValue>({
  name: "example.review",
  version: 1,
  initial: () => ({ phase: "route" }),
  phases: {
    route: async (task, runtime, context) => {
      const event = (task.input.payload ?? {}) as Record<string, any>;
      await runtime.commit(async (tx) => {
        const index = await tx.doc(Index);
        if (
          task.input.origin === "user" &&
          (event.type === "radar.message" || event.type === "radar.action")
        ) {
          const action = event.type === "radar.action" ? event.payload : null;
          const key = action?.key ?? index.selected;
          if (!key)
            return {
              status: "terminal",
              outcome: { status: "completed", result: "No review selected" },
            };
          const record = await tx.doc(Reviews, key, null);
          if (!record.request) throw new Error("review is no longer available");
          if (action?.type === "review.approve") {
            if (record.stage !== "approval")
              return {
                status: "terminal",
                outcome: {
                  status: "completed",
                  result: "Decision already handled",
                },
              };
            record.stage = "preparing";
            record.task = Number(task.id);
            await present(tx, {
              summary: `Preparing ${key}`,
              attention: false,
              actions: [],
            });
            return {
              status: "running",
              checkpoint: {
                phase: "prepare",
                key,
                request: { ...record.request },
                approved: true,
              },
            };
          }
          if (!record.conversation)
            throw new Error("review conversation is not ready");
          return {
            status: "running",
            checkpoint: {
              phase: "run",
              key,
              request: { ...record.request },
              text: String(event.text ?? "Please review this revision again"),
            },
          };
        }
        if (
          event.type !== "github.webhook" ||
          event.eventType !== "pull_request"
        )
          return {
            status: "terminal",
            outcome: { status: "completed", result: "Ignored event" },
          };
        const body = event.body,
          pr = body?.pull_request;
        const repository = body?.repository?.full_name;
        const local = settings.repositories[repository];
        if (!local || !pr || !Number.isInteger(body.number))
          return {
            status: "terminal",
            outcome: {
              status: "completed",
              result: "Outside configured scope",
            },
          };
        const resource = `${repository}#${body.number}`;
        const requested = body.requested_reviewer?.login === settings.reviewer;
        if (
          body.action === "closed" ||
          (body.action === "review_request_removed" && requested)
        ) {
          index.active[resource] = false;
          await present(tx, {
            summary: `${resource}: review request ended`,
            attention: false,
          });
          return {
            status: "terminal",
            outcome: { status: "completed", result: "Request ended" },
          };
        }
        if (
          !(body.action === "review_requested" && requested) &&
          !(body.action === "synchronize" && index.active[resource])
        )
          return {
            status: "terminal",
            outcome: {
              status: "completed",
              result: "No review requested from configured user",
            },
          };
        if (
          !/^[a-f0-9]{40,64}$/.test(pr.head?.sha) ||
          !/^[a-f0-9]{40,64}$/.test(pr.base?.sha)
        )
          throw new Error("review requires exact head and base commit IDs");
        const request: ReviewRequest = {
          repository,
          local,
          number: body.number,
          head: pr.head.sha,
          base: pr.base.sha,
          url: pr.html_url ?? "",
        };
        const key = `${resource}@${request.head}`;
        const record = await tx.doc(Reviews, key, null);
        if (record.stage === "done")
          return {
            status: "terminal",
            outcome: {
              status: "completed",
              result: "Revision already reviewed",
            },
          };
        const active = record.task
          ? await tx.task(taskId(record.task))
          : undefined;
        if (
          record.stage === "approval" ||
          (["preparing", "running"].includes(record.stage) &&
            active?.state.status !== "terminal")
        )
          return {
            status: "terminal",
            outcome: {
              status: "completed",
              result: "Revision already in progress",
            },
          };
        index.heads[resource] = request.head;
        index.active[resource] = true;
        index.selected = key;
        record.request = request;
        record.stage = "preparing";
        record.task = Number(task.id);
        const presentation = await tx.doc(PresentationDoc);
        const conversations = presentation.conversations.map((c) => ({
          ...c,
          label:
            c.label.startsWith(resource + " ") &&
            !c.label.includes(request.head.slice(0, 12))
              ? c.label + " (outdated)"
              : c.label,
        }));
        await present(tx, {
          title: "Review assistant",
          summary: `Preparing ${resource} at ${request.head.slice(0, 12)}`,
          attention: false,
          conversations,
          actions: [],
        });
        return {
          status: "running",
          checkpoint: { phase: "prepare", key, request },
        };
      }, context);
    },
    prepare: async (task, runtime, context) => {
      const { key, request } = task.state.checkpoint;
      if (!key || !request)
        throw new Error("review routing checkpoint is missing");
      if (!settings.fixture) {
        await git(
          ["fetch", "--no-tags", "origin", request.head, request.base],
          request.local,
          runtime.signal,
        );
      }
      await git(
        ["cat-file", "-e", `${request.head}^{commit}`],
        request.local,
        runtime.signal,
      );
      const workspaceRequest: WorkspaceRequest = {
        workspace: settings.host.workspace.path,
        repository: request.local,
        base: request.head,
        name: reviewName(request),
        operationId: `agent:${settings.id}:review:${key}`,
      };
      const stored = await runtime.snapshot(Reviews, key, context);
      const plan = (stored?.plan ?? null) as WorkspacePlan | null;
      const memo = await runtime.memo("workspace.plan", context);
      const currentPlan = await previewWorkspace(
        workspaceRequest,
        runtime.signal,
      );
      let preparedPlan =
        plan ?? (memo as unknown as WorkspacePlan | undefined) ?? currentPlan;
      const changed =
        preparedPlan.plan_id !== currentPlan.plan_id &&
        currentPlan.changes.length > 0;
      if (changed) preparedPlan = currentPlan;
      if (!plan && !memo)
        await runtime.memo(
          "workspace.plan",
          preparedPlan as unknown as JsonValue,
          context,
        );
      if (
        !preparedPlan.auto_confirm &&
        (!task.state.checkpoint.approved || changed)
      ) {
        await runtime.commit(async (tx) => {
          const record = await tx.doc(Reviews, key, null);
          record.plan = preparedPlan as unknown as JsonValue;
          record.stage = "approval";
          await present(tx, {
            summary: `${request.repository}#${request.number}: workspace preparation needs confirmation. Inspect this plan before proceeding:\n\n${JSON.stringify(preparedPlan, null, 2)}`,
            attention: true,
            actions: [
              {
                id: "prepare-review",
                label: "Prepare review workspace",
                payload: { type: "review.approve", key },
              },
            ],
          });
          return {
            status: "terminal",
            outcome: {
              status: "completed",
              result: {
                needsConfirmation: true,
                plan: preparedPlan as unknown as JsonValue,
              },
            },
          };
        }, context);
        return;
      }
      const workspace = await prepareWorkspace(
        workspaceRequest,
        preparedPlan,
        runtime.signal,
      );
      const info = await radar<{
        members: { path: string; repository: string }[];
      }>(["workspace-context", "--workspace", workspace.path], runtime.signal);
      const member = info.members.find(
        (m) =>
          resolve(m.repository) === resolve(request.local) &&
          (m as any).branch === reviewName(request),
      );
      if (!member)
        throw new Error(
          "prepared workspace is missing the configured repository",
        );
      const actual = await git(
        ["rev-parse", "HEAD"],
        member.path,
        runtime.signal,
      );
      if (actual !== request.head)
        throw new Error(
          "prepared workspace does not match the requested head; inspect it before retrying",
        );
      await runtime.commit(async (tx) => {
        const record = await tx.doc(Reviews, key, null);
        if (!record.conversation) {
          const conversation = await tx.createConversation({
            ownership: { kind: "ownerless" },
          });
          await configure(
            tx,
            conversation.id,
            agentChoices(
              settings.runtime.model,
              settings.skill.instructions,
              member.path,
            ),
          );
          record.conversation = Number(conversation.id);
          record.skillHash = settings.skill.hash;
        }
        record.workspace = workspace.path;
        record.stage = "running";
        return {
          status: "running",
          checkpoint: { phase: "run", key, request },
        };
      }, context);
    },
    run: async (task, runtime, context) => {
      const { key, request, text } = task.state.checkpoint;
      if (!key || !request) throw new Error("review checkpoint is missing");
      const record = await runtime.snapshot(Reviews, key, context);
      if (!record?.conversation) throw new Error("review conversation missing");
      const info = await radar<{
        members: { path: string; repository: string }[];
      }>(
        ["workspace-context", "--workspace", record.workspace],
        runtime.signal,
      );
      const member = info.members.find(
        (m) =>
          resolve(m.repository) === resolve(request.local) &&
          (m as any).branch === reviewName(request),
      );
      if (!member) throw new Error("review workspace was removed or changed");
      const conversation = await runtime.conversation(
        record.conversation as ConversationId,
        context,
      );
      if (!conversation) throw new Error("conversation unavailable");
      const report = await withWorkspaceLease(
        record.workspace,
        async (signal) => {
          const leasedContext = withAbortSignal(signal, context);
          const actual = await git(["rev-parse", "HEAD"], member.path, signal);
          if (actual !== request.head)
            throw new Error("review workspace head changed");
          const diff = text
            ? ""
            : await git(
                [
                  "diff",
                  "--no-ext-diff",
                  "--no-textconv",
                  "--unified=5",
                  `${request.base}...${request.head}`,
                  "--",
                ],
                member.path,
                signal,
              );
          const submission = await conversation.submit(
            {
              type: "input",
              requestId: `${task.input.source}:${task.input.id}`,
              content:
                text ??
                `Review ${request.repository}#${request.number}.\nBase: ${request.base}\nHead: ${request.head}\nSkill SHA256: ${record.skillHash}\n\nUntrusted patch data follows:\n${diff}`,
            },
            leasedContext,
          );
          const result = await submission.wait(leasedContext);
          if (result.status !== "done" || result.type !== "input")
            throw new Error("review input was not answered");
          return (await answerText(runtime, result.answer, context)).slice(
            0,
            200000,
          );
        },
        runtime.signal,
      );
      await runtime.commit(async (tx) => {
        const saved = await tx.doc(Reviews, key, null);
        saved.stage = "done";
        saved.report = report;
        const index = await tx.doc(Index),
          resource = `${request.repository}#${request.number}`;
        const current =
          index.active[resource] && index.heads[resource] === request.head;
        if (current) {
          const old = await tx.doc(PresentationDoc);
          const label = `${resource} ${request.head.slice(0, 12)}`;
          await present(tx, {
            summary: `${label}\n\n${report}`,
            attention: true,
            message: true,
            conversations: [
              ...old.conversations.filter((c) => c.id !== saved.conversation),
              { id: saved.conversation, label },
            ].slice(-20),
            workspaces: [
              ...old.workspaces.filter((w) => w.path !== saved.workspace),
              { path: saved.workspace, label },
            ].slice(-20),
            actions: [
              {
                id: "review-followup",
                label: "Ask about this review",
                text: true,
                payload: { type: "review.followup", key },
              },
            ],
          });
        }
        return {
          status: "terminal",
          outcome: {
            status: "completed",
            result: { report, head: request.head, current: !!current },
          },
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
  configure: async (host) => {
    if (
      typeof host.config.reviewer !== "string" ||
      !host.config.repositories ||
      typeof host.config.repositories !== "object" ||
      Array.isArray(host.config.repositories)
    )
      throw new Error(
        "configure reviewer and repositories (GitHub owner/name to local checkout path)",
      );
    const skill = await host.execution.loadSkill(
      typeof host.config.skill === "string"
        ? host.config.skill
        : resolve(host.workspace.path, "skills/review/SKILL.md"),
    );
    settings = {
      host,
      id: host.id,
      reviewer: host.config.reviewer,
      repositories: host.config.repositories as Record<string, string>,
      fixture: host.config.fixture === true,
      skill,
      runtime: modelRuntime(host.config),
    };
    const registry = createRegistry();
    registry.install(
      createReadOnlyTools((cwd) => {
        if (!cwd) throw new Error("review working directory is not configured");
        return [cwd, skill.root];
      }, host.execution),
    );
    return { models: settings.runtime.models, registry };
  },
  eventTask: Review,
} satisfies AgentDefinition;
