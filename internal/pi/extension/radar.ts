import { resolve } from "node:path";
import { StringEnum } from "@earendil-works/pi-ai";
import { Type } from "typebox";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const NewWorktree = Type.Object({
  repository: Type.String({ description: "Absolute source repository path returned by radar_workspace_context" }),
  branch_mode: StringEnum(["new"] as const),
  name: Type.String({ description: "Workspace and new branch name (Radar sanitizes it)" }),
  base: Type.String({ description: "Base revision returned by radar_repository_refs, for example origin/main" }),
}, { additionalProperties: false });

const ExistingWorktree = Type.Object({
  repository: Type.String({ description: "Absolute source repository path returned by radar_workspace_context" }),
  branch_mode: StringEnum(["existing"] as const),
  branch: Type.String({ description: "Existing local or origin branch returned by radar_repository_refs" }),
}, { additionalProperties: false });

const SandboxPort = Type.Object({
  host_port: Type.Integer({ minimum: 1, maximum: 65535, description: "IPv4 loopback host port" }),
  sandbox_port: Type.Integer({ minimum: 1, maximum: 65535, description: "TCP port inside the sandbox" }),
}, { additionalProperties: false });

const SandboxMount = Type.Object({
  path: Type.String({ description: "Absolute host directory to mount into the sandbox" }),
  read_only: Type.Optional(Type.Boolean({ description: "Mount read-only; defaults to true when omitted", default: true })),
}, { additionalProperties: false });

const DesiredSandbox = Type.Object({
  additional_mounts: Type.Array(SandboxMount, { description: "Complete agent-requested additional mount set; Radar-managed mounts are separate" }),
  ports: Type.Array(SandboxPort, { description: "Complete desired IPv4-loopback TCP port set" }),
}, { additionalProperties: false });

const DesiredWorkspace = Type.Object({
  worktrees: Type.Array(Type.Union([NewWorktree, ExistingWorktree]), {
    minItems: 1,
    description: "Complete desired Git worktree membership, including every unchanged member",
  }),
  sandbox: Type.Union([DesiredSandbox, Type.Null()], {
    description: "Complete desired additional-mount and port state, or null when this workspace has no sandbox",
  }),
}, { additionalProperties: false });

const ReconcileParameters = Type.Object({
  revision: Type.String({ description: "Revision returned by the latest radar_workspace_context call" }),
  desired: DesiredWorkspace,
}, { additionalProperties: false });

const NoParameters = Type.Object({}, { additionalProperties: false });

const RepositoryParameters = Type.Object({
  repository: Type.String({ description: "Absolute repository path returned by radar_workspace_context" }),
}, { additionalProperties: false });

type Change = {
  action: "add" | "remove" | "replace" | "recreate";
  resource: "worktree" | "sandbox" | "sandbox_mount" | "sandbox_port";
  summary: string;
};

type Plan = {
  workspace_name: string;
  revision: string;
  next_revision: string;
  plan_id: string;
  effective_sandbox_mount_count?: number;
  changes: Change[];
  warnings?: string[];
};

type ReconcileResult = {
  ok?: boolean;
  retryable?: boolean;
  reconfirm_required?: boolean;
  reason?: string;
  plan?: Plan;
  error?: string;
  sandbox_reconciled?: boolean;
  worktrees_added?: number;
  worktrees_removed?: number;
  ports_published?: number;
  ports_unpublished?: number;
  [key: string]: unknown;
};

const BusyOption = "@radar_busy";
const MaxReconcileConfirmations = 3;

async function publishBusy(pi: ExtensionAPI, busy: boolean) {
  const pane = process.env.TMUX_PANE?.trim();
  if (!pane) return;

  const args = busy
    ? ["set-option", "-p", "-t", pane, BusyOption, "1"]
    : ["set-option", "-p", "-u", "-t", pane, BusyOption];
  try {
    await pi.exec("tmux", args, { timeout: 5000 });
  } catch {
    // Busy state is informational and must never interfere with the session.
  }
}

function reconcileArgs(params: Record<string, unknown>, cwd: string, preview: boolean, plan?: Plan): string[] {
  const args = ["reconcile-workspace", "--workspace", cwd, "--request", JSON.stringify(params)];
  if (preview) args.push("--preview");
  if (plan) args.push("--plan", plan.plan_id, "--plan-changes", String(plan.changes.length));
  return args;
}

function parseJSON<T>(toolName: string, stdout: string, phase: string): T {
  try {
    return JSON.parse(stdout) as T;
  } catch (error) {
    throw new Error(`${toolName} ${phase} returned malformed JSON: ${error instanceof Error ? error.message : error}`);
  }
}

function commandFailure(toolName: string, phase: string, result: { code: number; stdout: string; stderr: string }): Error {
  const detail = result.stderr.trim() || result.stdout.trim() || `exit code ${result.code}`;
  return new Error(`${toolName} ${phase} failed: ${detail}`);
}

function confirmation(plan: Plan): string {
  const lines = [`Radar workspace: ${plan.workspace_name}`];
  if (plan.changes.length === 0) lines.push("No changes are required.");
  for (const change of plan.changes) {
    const marker = change.action === "add" ? "+" : change.action === "remove" ? "-" : "~";
    lines.push(`${marker} ${change.summary}`);
  }
  for (const warning of plan.warnings ?? []) lines.push(`WARNING: ${warning}`);
  return lines.join("\n");
}

function countLabel(count: number, singular: string): string {
  return `${count} ${singular}${count === 1 ? "" : "s"}`;
}

function retryableResultText(result: ReconcileResult): string {
  const progress: string[] = [];
  const added = Number(result.worktrees_added ?? 0);
  const removed = Number(result.worktrees_removed ?? 0);
  if (added > 0) progress.push(`${countLabel(added, "worktree")} added`);
  if (removed > 0) progress.push(`${countLabel(removed, "worktree")} removed`);

  let failure = "workspace reconciliation did not finish";
  if (result.sandbox_reconciled !== true) {
    failure = "sandbox recreation failed";
  } else if (Number(result.ports_published ?? 0) > 0 || Number(result.ports_unpublished ?? 0) > 0) {
    failure = "sandbox port reconciliation failed";
  }
  const prefix = progress.length > 0 ? `${progress.join(", ")}; ` : "";
  return `${prefix}${failure}. Re-inspect and retry.\n${JSON.stringify(result)}`;
}

async function runRadar(pi: ExtensionAPI, binary: string, args: string[], signal: AbortSignal | undefined, toolName: string, phase: string) {
  const result = await pi.exec(binary, args, { signal });
  if (result.code !== 0) throw commandFailure(toolName, phase, result);
  return result.stdout;
}

export default function radarExtension(pi: ExtensionAPI) {
  pi.on("session_start", async () => publishBusy(pi, false));
  pi.on("agent_start", async () => publishBusy(pi, true));
  pi.on("agent_settled", async () => publishBusy(pi, false));
  pi.on("session_shutdown", async () => publishBusy(pi, false));

  pi.registerTool({
    name: "radar_workspace_context",
    label: "Inspect Radar Workspace",
    description: "Inspect the current logical Radar workspace from the host. Returns a revision, capabilities, complete desired state, member branches and dirty status, current host resources, and repositories discovered through Radar configuration.",
    promptSnippet: "Inspect the current Radar workspace and obtain its revision and desired state before reconciling host resources",
    promptGuidelines: [
      "Use radar_workspace_context before radar_reconcile_workspace and copy its complete desired state before modifying only the requested resources.",
      "Use the absolute repository paths returned by radar_workspace_context; do not guess host paths from sandbox-visible directories.",
      "Respect radar_workspace_context capabilities: leave sandbox null when sandbox, additional_mounts, and port_forwarding are false.",
    ],
    parameters: NoParameters,
    executionMode: "sequential",

    async execute(_toolCallId, _params, signal, _onUpdate, ctx: ExtensionContext) {
      const toolName = "radar_workspace_context";
      const binary = process.env.RADAR_BINARY?.trim() || "radar";
      const text = await runRadar(pi, binary, ["workspace-context", "--workspace", resolve(ctx.cwd)], signal, toolName, "inspect");
      const result = parseJSON<Record<string, unknown>>(toolName, text, "inspect");
      return { content: [{ type: "text", text: JSON.stringify(result) }], details: result };
    },
  });

  pi.registerTool({
    name: "radar_repository_refs",
    label: "Inspect Radar Repository Refs",
    description: "Fetch and prune origin for one host repository, then return its default branch, valid base refs, canonical local/origin branches, and paths where branches are already checked out.",
    promptSnippet: "Inspect branches and valid base refs for a repository selected from radar_workspace_context",
    promptGuidelines: [
      "Use radar_repository_refs after selecting a repository from radar_workspace_context when the branch or new-branch base is not already known exactly.",
      "Pass a canonical branch name or base ref from radar_repository_refs in radar_reconcile_workspace desired worktrees.",
    ],
    parameters: RepositoryParameters,
    executionMode: "sequential",

    async execute(_toolCallId, params, signal, _onUpdate, _ctx: ExtensionContext) {
      const toolName = "radar_repository_refs";
      const binary = process.env.RADAR_BINARY?.trim() || "radar";
      const repository = String((params as { repository: string }).repository);
      const text = await runRadar(pi, binary, ["repository-refs", "--repo", repository], signal, toolName, "inspect");
      const result = parseJSON<Record<string, unknown>>(toolName, text, "inspect");
      return { content: [{ type: "text", text: JSON.stringify(result) }], details: result };
    },
  });

  pi.registerTool({
    name: "radar_reconcile_workspace",
    label: "Reconcile Radar Workspace",
    description: "Preview, confirm, and reconcile the complete desired host workspace state. It can add or remove clean member worktrees, additional host mounts, and IPv4-loopback TCP ports for an existing optional SBX sandbox. It cannot enable SBX for a non-sandbox workspace or remove the primary worktree.",
    promptSnippet: "Reconcile typed worktree and optional sandbox mount/port desired state after user confirmation",
    promptGuidelines: [
      "Use radar_reconcile_workspace for host workspace changes instead of direct git, tmux, or sbx commands because Radar must validate and persist the complete resource bundle.",
      "Always start from the latest radar_workspace_context revision and complete desired object; omitted worktrees, additional mounts, and ports are removals.",
      "radar_reconcile_workspace supports multiple branches from one repository, but each repository-and-branch pair must be unique.",
      "Before omitting a worktree, check its dirty status from radar_workspace_context; dirty worktrees cannot be removed until their changes are committed, stashed, or discarded.",
      "Use read_only true for radar_reconcile_workspace additional mounts unless writable host access is necessary and explicitly intended.",
      "When exposing a service, first use its configured port as host_port. If apply fails because that host port is unavailable, call radar_workspace_context again and retry with a randomly selected host_port from 49152 through 65535 while keeping sandbox_port unchanged.",
      "Never invent a sandbox object when radar_workspace_context reports sandbox false; sandbox-less worktree reconciliation is fully supported.",
    ],
    parameters: ReconcileParameters,
    executionMode: "sequential",

    async execute(_toolCallId, params, signal, _onUpdate, ctx: ExtensionContext) {
      if (!ctx.hasUI) {
        throw new Error("radar_reconcile_workspace requires an interactive confirmation channel; no changes were applied");
      }
      const binary = process.env.RADAR_BINARY?.trim() || "radar";
      const input = params as unknown as Record<string, unknown>;
      const cwd = resolve(ctx.cwd);
      const previewText = await runRadar(pi, binary, reconcileArgs(input, cwd, true), signal, "radar_reconcile_workspace", "preview");
      let plan = parseJSON<Plan>("radar_reconcile_workspace", previewText, "preview");
      const plans: Plan[] = [plan];

      for (let confirmationAttempt = 0; confirmationAttempt < MaxReconcileConfirmations; confirmationAttempt++) {
        if (plan.changes.length === 0) {
          return { content: [{ type: "text", text: JSON.stringify({ ok: true, unchanged: true, revision: plan.revision }) }], details: { plans, unchanged: true } };
        }
        const title = confirmationAttempt === 0 ? "Reconcile Radar workspace?" : "Workspace plan changed. Reconcile updated plan?";
        const approved = await ctx.ui.confirm(title, confirmation(plan));
        if (!approved) {
          return { content: [{ type: "text", text: JSON.stringify({ ok: false, cancelled: true }) }], details: { cancelled: true, plans } };
        }
        const resultText = await runRadar(pi, binary, reconcileArgs(input, cwd, false, plan), signal, "radar_reconcile_workspace", "apply");
        const result = parseJSON<ReconcileResult>("radar_reconcile_workspace", resultText, "apply");
        if (result.ok !== true && result.reconfirm_required === true && result.reason === "plan_changed") {
          if (!result.plan) {
            throw new Error(`radar_reconcile_workspace requested reconfirmation without an updated plan\n${resultText.trim()}`);
          }
          plan = result.plan;
          plans.push(plan);
          continue;
        }
        if (result.ok !== true && result.retryable === true) {
          const partial = retryableResultText(result);
          return { content: [{ type: "text", text: partial }], details: { plans, result, partial } };
        }
        if (result.ok !== true) {
          throw new Error(`radar_reconcile_workspace apply did not converge: ${String(result.error ?? "unknown error")}\n${resultText.trim()}`);
        }
        return { content: [{ type: "text", text: JSON.stringify(result) }], details: { plans, result } };
      }
      throw new Error(`radar_reconcile_workspace plan changed after ${MaxReconcileConfirmations} confirmations; call radar_workspace_context and retry`);
    },
  });
}
