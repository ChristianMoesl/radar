import { homedir } from "node:os";
import { readdir, readFile } from "node:fs/promises";
import { join, resolve, sep } from "node:path";
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
    description: "Complete desired Git worktree membership, including every unchanged member; an empty list is valid",
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
  path?: string;
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

type WorkspaceMember = {
  repository: string;
  path: string;
  instruction_files?: string[];
  skill_paths?: string[];
};

type WorkspaceContextResult = {
  workspace_path?: string;
  note?: { path: string; workspace_path: string };
  members?: WorkspaceMember[];
  [key: string]: unknown;
};

type ResourceSnapshot = { contextPaths: string[]; skillPaths: string[] };

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
const ResourceEntry = "radar-workspace-resources";

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

function homeRelativePath(path: string): string {
  const home = homedir();
  if (!home) return path;
  if (path === home) return "~";
  if (path.startsWith(home + sep)) return `~${path.slice(home.length)}`;
  return path;
}

function displayChangeSummary(change: Change): string {
  if (!change.path) return change.summary;
  const shortened = homeRelativePath(change.path);
  if (shortened === change.path) return change.summary;
  return change.summary.replace(change.path, shortened);
}

function confirmation(plan: Plan): string {
  const lines = [`Radar workspace: ${plan.workspace_name}`];
  if (plan.changes.length === 0) lines.push("No changes are required.");
  for (const change of plan.changes) {
    const marker = change.action === "add" ? "+" : change.action === "remove" ? "-" : "~";
    lines.push(`${marker} ${displayChangeSummary(change)}`);
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

async function inspectWorkspace(pi: ExtensionAPI, cwd: string, signal?: AbortSignal): Promise<WorkspaceContextResult> {
  const binary = process.env.RADAR_BINARY?.trim() || "radar";
  const text = await runRadar(pi, binary, ["workspace-context", "--workspace", resolve(cwd)], signal, "radar_workspace_context", "inspect");
  return parseJSON<WorkspaceContextResult>("radar_workspace_context", text, "inspect");
}

async function skillFiles(root: string): Promise<string[]> {
  const found: string[] = [];
  async function visit(directory: string) {
    let entries;
    try {
      entries = await readdir(directory, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) await visit(path);
      else if (entry.isFile() && (entry.name === "SKILL.md" || (directory === root && root.includes(`${sep}.pi${sep}`) && entry.name.endsWith(".md")))) found.push(path);
    }
  }
  await visit(root);
  return found.sort();
}

async function skillName(path: string): Promise<string | undefined> {
  try {
    const text = await readFile(path, "utf8");
    if (!text.startsWith("---\n")) return undefined;
    const end = text.indexOf("\n---", 4);
    if (end < 0) return undefined;
    const match = text.slice(4, end).match(/^name:\s*['\"]?([^'\"\n]+)['\"]?\s*$/m);
    return match?.[1]?.trim();
  } catch {
    return undefined;
  }
}

async function discoverResources(context: WorkspaceContextResult, notify?: (message: string, level: "info" | "warning" | "error") => void): Promise<ResourceSnapshot> {
  const contextPaths = [...new Set((context.members ?? []).flatMap((member) => member.instruction_files ?? []))].sort();
  const candidates = (await Promise.all([...new Set((context.members ?? []).flatMap((member) => member.skill_paths ?? []))].map(skillFiles))).flat();
  const byName = new Map<string, string[]>();
  for (const path of candidates) {
    const name = await skillName(path);
    if (!name) continue;
    byName.set(name, [...(byName.get(name) ?? []), path]);
  }
  const duplicates = [...byName.entries()].filter(([, paths]) => paths.length > 1);
  for (const [name, paths] of duplicates) notify?.(`Radar did not load duplicate skill ${name}: ${paths.join(", ")}`, "warning");
  const duplicatePaths = new Set(duplicates.flatMap(([, paths]) => paths));
  const skillPaths = candidates.filter((path) => !duplicatePaths.has(path)).sort();
  return { contextPaths, skillPaths };
}

function resourceChanges(previous: ResourceSnapshot, next: ResourceSnapshot): string[] {
  const lines: string[] = [];
  for (const path of next.contextPaths.filter((path) => !previous.contextPaths.includes(path))) lines.push(`added context ${path}`);
  for (const path of previous.contextPaths.filter((path) => !next.contextPaths.includes(path))) lines.push(`removed context ${path}`);
  for (const path of next.skillPaths.filter((path) => !previous.skillPaths.includes(path))) lines.push(`added skill ${path}`);
  for (const path of previous.skillPaths.filter((path) => !next.skillPaths.includes(path))) lines.push(`removed skill ${path}`);
  return lines;
}

export default function radarExtension(pi: ExtensionAPI) {
  let previousResources: ResourceSnapshot = { contextPaths: [], skillPaths: [] };
  let knownContext: WorkspaceContextResult | undefined;
  pi.on("session_start", async (_event, ctx) => {
    await publishBusy(pi, false);
    for (const entry of ctx.sessionManager.getEntries()) {
      if (entry.type === "custom" && entry.customType === ResourceEntry) {
        previousResources = entry.data as ResourceSnapshot;
      }
    }
  });
  pi.on("resources_discover", async (event, ctx) => {
    try {
      knownContext = await inspectWorkspace(pi, event.cwd);
      const discovered = await discoverResources(knownContext, (message, level) => ctx.ui.notify(message, level));
      const resources = ctx.isProjectTrusted() ? discovered : { ...discovered, skillPaths: [] };
      if (!ctx.isProjectTrusted() && discovered.skillPaths.length > 0) ctx.ui.notify("Radar did not load member skills because the workspace is not trusted", "warning");
      const changes = resourceChanges(previousResources, resources);
      if (event.reason === "reload" && changes.length > 0) ctx.ui.notify(`Radar workspace resources refreshed: ${changes.join("; ")}`, "info");
      previousResources = resources;
      pi.appendEntry(ResourceEntry, resources);
      return { skillPaths: resources.skillPaths };
    } catch (error) {
      if (event.reason === "reload") ctx.ui.notify(`Radar workspace resource refresh failed; keeping the previous resource set: ${error instanceof Error ? error.message : error}`, "warning");
      return { skillPaths: previousResources.skillPaths };
    }
  });
  pi.on("before_agent_start", async (event, ctx) => {
    try {
      knownContext = await inspectWorkspace(pi, ctx.cwd, ctx.signal);
      const resources = await discoverResources(knownContext);
      const blocks: string[] = [];
      for (const path of resources.contextPaths) {
        try {
          blocks.push(`<project_instructions path="${path}">\n${await readFile(path, "utf8")}\n</project_instructions>`);
        } catch {
          // A later reload will report files that disappeared during the turn.
        }
      }
      const guidelines = [
        "Radar workspace instructions:",
        knownContext.note ? `- note.md is the canonical Obsidian task note at ${knownContext.note.path}. Its body may be empty. Do not invent a template unless the user asks or the work requires one.` : "",
        "- Member worktrees are direct children of the workspace. Use Radar's typed tools for membership, sandbox mount, and port changes.",
        "- Instructions from a member repository context file apply only to files under that repository. Nested context files apply to their containing subtree. The most specific applicable directory wins. Global and workspace instructions apply to every member.",
      ].filter(Boolean).join("\n");
      return { systemPrompt: [event.systemPrompt, guidelines, ...blocks].join("\n\n") };
    } catch {
      return undefined;
    }
  });
  pi.registerCommand("radar-reload-workspace-resources", {
    description: "Reload member repository context and skills after workspace reconciliation",
    handler: async (_args, ctx) => {
      await ctx.reload();
      return;
    },
  });
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
    description: "Try to fetch and prune origin for one host repository, then return its default branch, valid base refs, canonical local/origin branches, and checkout paths. If the fetch fails, return locally cached refs with a warning.",
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
    description: "Preview, confirm, and reconcile the complete desired host workspace state. It can add or remove clean member worktrees, including the last one, plus additional host mounts and IPv4-loopback TCP ports for an existing optional SBX sandbox. It cannot enable SBX for a non-sandbox workspace.",
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
          if (Number(result.worktrees_added ?? 0) > 0 || Number(result.worktrees_removed ?? 0) > 0) {
            pi.sendUserMessage("/radar-reload-workspace-resources", { deliverAs: "followUp", expandPromptTemplates: true });
          }
          const partial = retryableResultText(result);
          return { content: [{ type: "text", text: partial }], details: { plans, result, partial } };
        }
        if (result.ok !== true) {
          throw new Error(`radar_reconcile_workspace apply did not converge: ${String(result.error ?? "unknown error")}\n${resultText.trim()}`);
        }
        if (Number(result.worktrees_added ?? 0) > 0 || Number(result.worktrees_removed ?? 0) > 0) {
          pi.sendUserMessage("/radar-reload-workspace-resources", { deliverAs: "followUp", expandPromptTemplates: true });
        }
        return { content: [{ type: "text", text: JSON.stringify(result) }], details: { plans, result } };
      }
      throw new Error(`radar_reconcile_workspace plan changed after ${MaxReconcileConfirmations} confirmations; call radar_workspace_context and retry`);
    },
  });
}
