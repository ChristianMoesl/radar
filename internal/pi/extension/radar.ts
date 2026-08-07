import { resolve } from "node:path";
import { StringEnum } from "@earendil-works/pi-ai";
import { Type } from "typebox";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const NewBranch = Type.Object({
  repository: Type.String({ description: "Absolute path to the source Git repository" }),
  branch_mode: StringEnum(["new"] as const),
  name: Type.String({ description: "Workspace and new branch name (Radar sanitizes it)" }),
  base: Type.String({ description: "Base revision, for example origin/main" }),
}, { additionalProperties: false });

const ExistingBranch = Type.Object({
  repository: Type.String({ description: "Absolute path to the source Git repository" }),
  branch_mode: StringEnum(["existing"] as const),
  branch: Type.String({ description: "Existing local or origin branch" }),
}, { additionalProperties: false });

const Parameters = Type.Union([NewBranch, ExistingBranch], {
  description: "Choose exactly one branch mode and provide only its corresponding fields",
});

const NoParameters = Type.Object({}, { additionalProperties: false });

const RepositoryParameters = Type.Object({
  repository: Type.String({ description: "Absolute repository path returned by radar_workspace_context" }),
}, { additionalProperties: false });

type Plan = {
  workspace_name: string;
  repository: string;
  branch_mode: "new" | "existing";
  branch: string;
  base?: string;
  path: string;
  session_name?: string;
  sandbox_name?: string;
  recreate_sandbox: boolean;
  warnings?: string[];
};

function commandArgs(params: Record<string, unknown>, cwd: string, preview: boolean): string[] {
  const args = ["add-worktree", "--workspace", cwd, "--repo", String(params.repository), "--branch-mode", String(params.branch_mode)];
  if (params.branch_mode === "new") {
    args.push("--name", String(params.name), "--base", String(params.base));
  } else {
    args.push("--branch", String(params.branch));
  }
  if (preview) args.push("--preview");
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
  const lines = [
    `Repository: ${plan.repository}`,
    `Branch mode: ${plan.branch_mode}`,
    `Branch: ${plan.branch}`,
  ];
  if (plan.base) lines.push(`Base: ${plan.base}`);
  lines.push(`Destination: ${plan.path}`, `Radar workspace: ${plan.workspace_name}`);
  if (plan.session_name) lines.push(`tmux session: ${plan.session_name}`);
  if (plan.sandbox_name) lines.push(`SBX sandbox: ${plan.sandbox_name}`);
  if (plan.recreate_sandbox) lines.push("WARNING: recreating this sandbox interrupts processes currently running inside it.");
  for (const warning of plan.warnings ?? []) lines.push(`Warning: ${warning}`);
  return lines.join("\n");
}

async function runRadar(pi: ExtensionAPI, binary: string, args: string[], signal: AbortSignal | undefined, toolName: string, phase: string) {
  const result = await pi.exec(binary, args, { signal });
  if (result.code !== 0) throw commandFailure(toolName, phase, result);
  return result.stdout;
}

export default function radarExtension(pi: ExtensionAPI) {
  pi.registerTool({
    name: "radar_workspace_context",
    label: "Inspect Radar Workspace",
    description: "Inspect the current logical Radar workspace from the host. Returns its primary and member Git worktrees, branches, tmux and SBX resources, and repositories discovered through Radar configuration. Marks repositories that already belong to the workspace.",
    promptSnippet: "Inspect the current Radar workspace and discover host repositories before adding a worktree",
    promptGuidelines: [
      "Use radar_workspace_context before radar_add_worktree when the repository is not already known exactly or when you need to inspect existing workspace members.",
      "Use the absolute repository paths returned by radar_workspace_context; do not guess host paths from sandbox-visible directories.",
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
      "Pass a canonical branch name to radar_add_worktree existing mode and one of base_refs to new mode.",
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
    name: "radar_add_worktree",
    label: "Add Radar Worktree",
    description: "Build a validated plan, ask the user for explicit confirmation, and add a Git worktree from another repository to the current logical Radar workspace. Supports either a sanitized new branch based on a revision or an existing local/origin branch. Reuses the workspace's current tmux session and SBX sandbox.",
    promptSnippet: "Add a repository worktree to the current logical Radar workspace after user confirmation",
    promptGuidelines: [
      "Use radar_add_worktree when the user asks to add another repository or Git worktree to the current Radar workspace; choose either new or existing branch mode and all required values.",
      "Never emulate radar_add_worktree with direct git, tmux, or sbx commands because Radar must preserve workspace membership and shared resources.",
    ],
    parameters: Parameters,
    executionMode: "sequential",

    async execute(_toolCallId, params, signal, _onUpdate, ctx: ExtensionContext) {
      if (!ctx.hasUI) {
        throw new Error("radar_add_worktree requires an interactive confirmation channel; no changes were applied");
      }
      const binary = process.env.RADAR_BINARY?.trim() || "radar";
      const input = params as unknown as Record<string, unknown>;
      const cwd = resolve(ctx.cwd);
      const previewText = await runRadar(pi, binary, commandArgs(input, cwd, true), signal, "radar_add_worktree", "preview");
      const plan = parseJSON<Plan>("radar_add_worktree", previewText, "preview");
      const approved = await ctx.ui.confirm("Add worktree to Radar workspace?", confirmation(plan));
      if (!approved) {
        return { content: [{ type: "text", text: JSON.stringify({ ok: false, cancelled: true }) }], details: { cancelled: true, plan } };
      }
      const resultText = await runRadar(pi, binary, commandArgs(input, cwd, false), signal, "radar_add_worktree", "apply");
      const result = parseJSON<Record<string, unknown>>("radar_add_worktree", resultText, "apply");
      if (result.ok !== true) {
        throw new Error(`radar_add_worktree apply did not converge: ${String(result.error ?? "unknown error")}\n${resultText.trim()}`);
      }
      return { content: [{ type: "text", text: JSON.stringify(result) }], details: { plan, result } };
    },
  });
}
