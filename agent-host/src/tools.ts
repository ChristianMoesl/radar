import { execFile, spawn } from "node:child_process";
import { once } from "node:events";
import { promisify } from "node:util";
import { Type } from "typebox";
import { defineExtension, defineTool } from "@earendil-works/pi-durable";
import type { JsonValue } from "@earendil-works/chord";
import type { WorkspaceExecution } from "./environment.ts";

const execute = promisify(execFile);

export async function radar<T = Record<string, unknown>>(
  args: string[],
  signal?: AbortSignal,
): Promise<T> {
  const { stdout } = await execute(
    process.env.RADAR_BINARY ?? "radar",
    [...args, "--json"],
    { signal, maxBuffer: 1024 * 1024 },
  );
  return JSON.parse(stdout) as T;
}

export type WorkspaceRequest = {
  workspace?: string;
  repository?: string;
  base?: string;
  name: string;
  operationId: string;
};
export type WorkspacePlan = {
  plan_id: string;
  auto_confirm: boolean;
  note?: JsonValue;
  changes: { resource: string; path?: string }[];
};

function workspaceArgs(request: WorkspaceRequest): string[] {
  if (!request.operationId)
    throw new Error("stable workspace operationId is required");
  const args = [
    "create",
    "--prepare-only",
    "--name",
    request.name,
    "--operation-id",
    request.operationId,
  ];
  if (request.workspace) args.push("--workspace", request.workspace);
  if (request.repository) {
    if (!request.base)
      throw new Error("workspace base revision is required with repository");
    args.push("--repo", request.repository, "--base", request.base);
  }
  return args;
}

export function previewWorkspace(
  request: WorkspaceRequest,
  signal?: AbortSignal,
): Promise<WorkspacePlan> {
  return radar([...workspaceArgs(request), "--preview"], signal);
}

export async function prepareWorkspace(
  request: WorkspaceRequest,
  plan: WorkspacePlan,
  signal?: AbortSignal,
): Promise<{ path: string; warning?: string }> {
  return radar(
    [
      ...workspaceArgs(request),
      "--plan-id",
      plan.plan_id,
      ...(plan.note ? ["--prepared-note", JSON.stringify(plan.note)] : []),
    ],
    signal,
  );
}

/** The helper's OS lock survives daemon restart and is released if this process dies. */
export async function withWorkspaceLease<T>(
  workspace: string,
  body: (signal: AbortSignal) => Promise<T>,
  signal?: AbortSignal,
): Promise<T> {
  const child = spawn(
    process.env.RADAR_BINARY ?? "radar",
    ["workspace-lease", "--workspace", workspace, "--json"],
    { stdio: ["pipe", "pipe", "pipe"] },
  );
  const exited = once(child, "exit").catch(() => []);
  const controller = new AbortController();
  let lost = false,
    errorOutput = "";
  child.stderr.on("data", (data) => {
    errorOutput = (errorOutput + data).slice(-65536);
  });
  child.once("exit", () => {
    lost = true;
    controller.abort(new Error("workspace lease helper exited"));
  });
  const abort = () => controller.abort(signal?.reason);
  signal?.addEventListener("abort", abort, { once: true });
  try {
    await new Promise<void>((done, reject) => {
      let buffer = "";
      const timer = setTimeout(() => {
        reject(new Error("workspace lease startup timed out"));
        child.kill("SIGTERM");
      }, 5000);
      child.once("error", (error) => {
        clearTimeout(timer);
        reject(error);
      });
      child.once("exit", () => {
        clearTimeout(timer);
        reject(
          new Error(errorOutput || "workspace lease exited before acquisition"),
        );
      });
      child.stdout.on("data", (chunk) => {
        buffer += chunk.toString();
        if (buffer.length > 65536) {
          clearTimeout(timer);
          reject(new Error("invalid lease response"));
          child.kill("SIGTERM");
          return;
        }
        if (!buffer.includes("\n")) return;
        clearTimeout(timer);
        try {
          const response = JSON.parse(buffer.split("\n")[0]!);
          if (!response.ok) reject(new Error(response.error));
          else done();
        } catch (error) {
          reject(error);
        }
      });
    });
    if (signal?.aborted) controller.abort(signal.reason);
    const result = await body(controller.signal);
    if (lost)
      throw new Error(
        "workspace lease was lost; inspect resources before retrying",
      );
    return result;
  } finally {
    signal?.removeEventListener("abort", abort);
    child.stdin.end();
    const timer = setTimeout(() => child.kill("SIGTERM"), 1000);
    await exited;
    clearTimeout(timer);
  }
}

export function createRadarTools(instanceId: string) {
  const context = defineTool({
    name: "radar_workspace_context",
    description:
      "Inspect a Radar workspace and its member repositories, resources, and skills",
    parameters: Type.Object({ workspace: Type.String() }),
    replay: "safe",
    execute: async (args, _api, ctx) => ({
      output: [
        {
          type: "text" as const,
          text: JSON.stringify(
            await radar(
              ["workspace-context", "--workspace", args.workspace],
              ctx.abortSignal,
            ),
          ),
        },
      ],
    }),
  });
  const refs = defineTool({
    name: "radar_repository_refs",
    description:
      "Inspect available branches and references in an explicitly selected repository",
    parameters: Type.Object({ repository: Type.String() }),
    replay: "safe",
    execute: async (args, _api, ctx) => ({
      output: [
        {
          type: "text" as const,
          text: JSON.stringify(
            await radar(
              ["repository-refs", "--repo", args.repository],
              ctx.abortSignal,
            ),
          ),
        },
      ],
    }),
  });
  const create = defineTool({
    name: "radar_prepare_workspace",
    description:
      "Prepare a workspace without launching Pi. If confirmation is required, return its exact plan for a human decision.",
    parameters: Type.Object({
      name: Type.String(),
      repository: Type.Optional(Type.String()),
      base: Type.Optional(Type.String()),
    }),
    replay: "safe",
    execute: async (args, api, ctx) => {
      const request = {
        name: args.name,
        repository: args.repository,
        base: args.base,
        operationId: `agent:${instanceId}:tool:${api.taskId}`,
      };
      const prior = await api.memo("workspace.plan", ctx);
      const plan =
        prior ??
        (await api.memo(
          "workspace.plan",
          await previewWorkspace(request, ctx.abortSignal),
          ctx,
        ));
      const p = plan as unknown as WorkspacePlan;
      if (!p.auto_confirm)
        return {
          output: [
            {
              type: "text" as const,
              text: JSON.stringify({
                needsConfirmation: true,
                request,
                plan: p,
              }),
            },
          ],
        };
      const workspace = await prepareWorkspace(request, p, ctx.abortSignal);
      return {
        output: [{ type: "text" as const, text: JSON.stringify(workspace) }],
      };
    },
  });
  const reconcile = defineTool({
    name: "radar_reconcile_workspace",
    description:
      "Reconcile a workspace using automatic confirmation policy, or return a full plan for a definition-owned human decision",
    parameters: Type.Object({
      workspace: Type.String(),
      request: Type.Unknown(),
    }),
    replay: "safe",
    execute: async (args, _api, ctx) => {
      const command = [
        "reconcile-workspace",
        "--prepare-only",
        "--workspace",
        args.workspace,
        "--request",
        JSON.stringify(args.request),
      ];
      const plan = await radar<WorkspacePlan>(
        [...command, "--preview"],
        ctx.abortSignal,
      );
      if (!plan.auto_confirm)
        return {
          output: [
            {
              type: "text" as const,
              text: JSON.stringify({
                needsConfirmation: true,
                workspace: args.workspace,
                request: args.request,
                plan,
              }),
            },
          ],
        };
      const result = await radar(
        [...command, "--plan", plan.plan_id],
        ctx.abortSignal,
      );
      return {
        output: [{ type: "text" as const, text: JSON.stringify(result) }],
      };
    },
  });
  return defineExtension({
    name: "radar.workspace",
    tools: [context, refs, create, reconcile],
  });
}

export type LoadedSkill = {
  name: string;
  path: string;
  root: string;
  hash: string;
  instructions: string;
};
/** No shell or write tools are installed. Symlinks must resolve inside an explicit read root. */
export function createReadOnlyTools(
  roots: string[] | ((cwd: string | undefined) => string[]),
  execution: WorkspaceExecution,
) {
  if (Array.isArray(roots) && !roots.length)
    throw new Error("read-only tools require explicit roots");
  const read = defineTool({
    name: "read_workspace_file",
    description: "Read a text file inside an allowed workspace or skill root",
    parameters: Type.Object({
      path: Type.String(),
      root: Type.Optional(Type.Integer({ minimum: 0 })),
    }),
    replay: "safe",
    execute: async (args, api, context) => {
      const selected =
        typeof roots === "function"
          ? roots((await api.agent(context)).cwd)
          : roots;
      const root = selected[args.root ?? 0];
      if (!root) throw new Error("unknown read root");
      return {
        output: [
          {
            type: "text" as const,
            text: await execution.readOnly(
              root,
              args.path,
              context.abortSignal,
            ),
          },
        ],
      };
    },
  });
  const list = defineTool({
    name: "list_workspace_files",
    description: "List one directory inside an allowed read root",
    parameters: Type.Object({
      path: Type.Optional(Type.String()),
      root: Type.Optional(Type.Integer({ minimum: 0 })),
    }),
    replay: "safe",
    execute: async (args, api, context) => {
      const selected =
        typeof roots === "function"
          ? roots((await api.agent(context)).cwd)
          : roots;
      const root = selected[args.root ?? 0];
      if (!root) throw new Error("unknown read root");
      return {
        output: [
          {
            type: "text" as const,
            text: (
              await execution.listOnly(
                root,
                args.path ?? ".",
                context.abortSignal,
              )
            ).join("\n"),
          },
        ],
      };
    },
  });
  return defineExtension({ name: "radar.read-only", tools: [read, list] });
}
