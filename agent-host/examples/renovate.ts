import { createHash } from "node:crypto";
import {
  createModels,
  createRegistry,
  defineDoc,
  defineDocFamily,
  defineTask,
  present,
  type AgentDefinition,
  type AgentEvent,
  type HostContext,
  type TaskId,
} from "../src/index.ts";

type PullRequest = {
  type: "github.pull_request";
  nodeId: string;
  repository: string;
  number: number;
  author: string;
  authorType: string;
  state: string;
  url: string;
};
type Record = {
  pr: PullRequest | null;
  skipped: boolean;
  owner: number;
  attempted: boolean;
  commentId: number;
  commentUrl: string;
  text: string;
};
const PullRequests = defineDocFamily<Record, null>({
  kind: "renovate.pull-request",
  version: 1,
  scope: "session",
  family: true,
  initial: () => ({
    pr: null,
    skipped: false,
    owner: 0,
    attempted: false,
    commentId: 0,
    commentUrl: "",
    text: "",
  }),
});
const Failures = defineDoc<{ items: { [key: string]: string } }>({
  kind: "renovate.failures",
  version: 1,
  scope: "session",
  initial: () => ({ items: {} }),
});
let host: HostContext;
let author: string;
let comment: string;
let includeExisting: boolean;
const digest = (value: string) =>
  createHash("sha256").update(value).digest("hex");
const marker = (key: string) =>
  `<!-- radar-renovate:${digest(`${host.id}:${key}`)} -->`;
const actor = (login: string) => login.toLowerCase().replace(/\[bot\]$/, "");

async function github<T>(
  path: string,
  args: string[],
  signal: AbortSignal,
): Promise<T> {
  return JSON.parse(
    await host.execution.exec(
      ["gh", "api", path, ...args],
      host.workspace.path,
      signal,
    ),
  ) as T;
}
type Comment = { id: number; node_id: string; body: string; html_url: string };
async function findComment(
  pr: PullRequest,
  key: string,
  signal: AbortSignal,
): Promise<Comment | undefined> {
  // Read every page before deciding a comment is absent. A failure never grants
  // permission to post, and an uncertain POST is never automatically repeated.
  for (let page = 1; page <= 1000; page++) {
    const comments = await github<Comment[]>(
      `repos/${pr.repository}/issues/${pr.number}/comments?per_page=100&page=${page}`,
      [],
      signal,
    );
    if (!Array.isArray(comments))
      throw new Error("Invalid GitHub comment page");
    const found = comments.find(
      (c) => typeof c.body === "string" && c.body.includes(marker(key)),
    );
    if (found) return found;
    if (comments.length < 100) return undefined;
  }
  throw new Error("GitHub comment pagination limit reached; posting withheld");
}
function pullRequest(value: unknown): PullRequest | undefined {
  const pr = value as PullRequest | undefined;
  if (
    !pr ||
    pr.type !== "github.pull_request" ||
    typeof pr.nodeId !== "string" ||
    !pr.nodeId ||
    typeof pr.repository !== "string" ||
    !/^[a-z0-9_.-]+\/[a-z0-9_.-]+$/i.test(pr.repository) ||
    !Number.isSafeInteger(pr.number) ||
    pr.number <= 0 ||
    typeof pr.author !== "string"
  )
    return;
  return pr;
}
type State = { phase: "route" } | { phase: "work"; key: string };
const Event = defineTask<AgentEvent, State, string>({
  name: "renovate.tag",
  version: 1,
  initial: () => ({ phase: "route" }),
  phases: {
    route: async (task, runtime, context) => {
      const event = task.input.payload as {
        change?: string;
        baseline?: boolean;
        data?: unknown;
        type?: string;
        payload?: { key?: string };
      };
      await runtime.commit(async (tx) => {
        let key: string;
        const done = (result: string) => ({
          status: "terminal" as const,
          outcome: { status: "completed" as const, result },
        });
        if (
          task.input.origin === "user" &&
          event?.type === "radar.action" &&
          event.payload?.key
        ) {
          key = event.payload.key;
        } else {
          const pr = pullRequest(event?.data);
          if (
            task.input.source !== "radar.collection.github" ||
            !pr ||
            !["snapshot", "discovered", "changed"].includes(event.change ?? "")
          )
            return done("Ignored event");
          key = pr.nodeId;
          const record = await tx.doc(PullRequests, key, null);
          // Remember baseline PRs permanently, including later rebases/reopens.
          if (
            (event.baseline === true || event.change === "snapshot") &&
            !includeExisting
          )
            record.skipped = true;
          if (record.skipped || record.commentId)
            return done("Already handled or excluded baseline PR");
          if (pr.state !== "OPEN" || actor(pr.author) !== actor(author))
            return done("PR does not match policy");
          record.pr = pr;
          if (!record.text) record.text = `${comment}\n\n${marker(key)}`;
        }
        const record = await tx.doc(PullRequests, key, null);
        if (!record.pr || record.skipped || record.commentId)
          return done("Already handled or excluded PR");
        if (record.owner && record.owner !== Number(task.id)) {
          const owner = await tx.task(record.owner as TaskId);
          if (owner && owner.state.status !== "terminal")
            return done("PR already has active work");
        }
        record.owner = Number(task.id);
        return { status: "running", checkpoint: { phase: "work", key } };
      }, context);
    },
    work: async (task, runtime, context) => {
      const { key } = task.state.checkpoint;
      const record = await runtime.snapshot(PullRequests, key, context);
      if (!record?.pr) throw new Error("Missing PR state");
      const pr = record.pr;
      let posted: Comment | undefined;
      let problem = "";
      try {
        posted = await findComment(pr, key, runtime.signal);
        if (!posted) {
          if (record.attempted)
            throw new Error(
              "Comment outcome uncertain; marker not visible. Reconcile again after checking GitHub. Posting withheld.",
            );
          // Recheck live identity and policy before acting on a delayed event.
          const current = await github<{
            node_id: string;
            state: string;
            user: { login: string };
          }>(`repos/${pr.repository}/pulls/${pr.number}`, [], runtime.signal);
          if (
            current.node_id !== pr.nodeId ||
            current.state !== "open" ||
            actor(current.user.login) !== actor(author)
          ) {
            await runtime.commit(
              () => ({
                status: "terminal",
                outcome: {
                  status: "completed",
                  result: "PR no longer matches policy",
                },
              }),
              context,
            );
            return;
          }
          // No task-state change here: recovery re-enters this phase, sees the
          // attempted flag and reconciles instead of sending a second mention.
          await runtime.commit(async (tx) => {
            (await tx.doc(PullRequests, key, null)).attempted = true;
            return undefined;
          }, context);
          try {
            posted = await github<Comment>(
              `repos/${pr.repository}/issues/${pr.number}/comments`,
              ["--method", "POST", "-f", `body=${record.text}`],
              runtime.signal,
            );
            if (!posted?.id)
              throw new Error("GitHub returned no comment identity");
          } catch (error) {
            posted = await findComment(pr, key, runtime.signal);
            if (!posted) throw error;
          }
        }
        if (!Number.isSafeInteger(posted.id) || posted.id <= 0)
          throw new Error("Invalid GitHub comment identity");
      } catch (error) {
        if (runtime.signal.aborted) throw error;
        problem = String(error);
      }
      await runtime.commit(async (tx) => {
        const saved = await tx.doc(PullRequests, key, null);
        const failures = await tx.doc(Failures);
        if (problem)
          failures.items[key] = `${pr.repository}#${pr.number}: ${problem}`;
        else {
          saved.commentId = posted!.id;
          saved.commentUrl = posted!.html_url;
          delete failures.items[key];
        }
        const pending = Object.entries(failures.items);
        await present(tx, {
          title: "Renovate review requests",
          attention: pending.length > 0,
          summary: [
            problem
              ? "Comment requires attention"
              : `Review requested: ${saved.commentUrl}`,
            ...pending.map(([, message]) => message),
          ].join("\n"),
          actions: pending
            .slice(0, 100)
            .map(([key, message]) => ({
              id: `reconcile-${digest(key)}`,
              label: `Reconcile ${message.split(":")[0]}`,
              payload: { key },
            })),
        });
        return {
          status: "terminal",
          outcome: {
            status: "completed",
            result: problem || `Comment ${saved.commentId}`,
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
  configure: (input) => {
    host = input;
    const config = host.config;
    if (
      typeof config.renovateAuthor !== "string" ||
      !config.renovateAuthor.trim() ||
      typeof config.comment !== "string" ||
      !/@[a-z0-9][a-z0-9-]*/i.test(config.comment) ||
      config.comment.includes("<") ||
      typeof config.includeExisting !== "boolean"
    )
      throw new Error(
        "Confirm renovateAuthor, the exact comment mentioning the review agent, and includeExisting (true or false) before starting",
      );
    author = config.renovateAuthor;
    comment = config.comment;
    includeExisting = config.includeExisting;
    return { models: createModels(), registry: createRegistry() };
  },
  eventTask: Event,
} satisfies AgentDefinition;
