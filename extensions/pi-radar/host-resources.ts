import { resolve } from "node:path";
import { Type } from "typebox";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

type ClonePlan = {
  plan_id: string; remote: string; repository_dir: string; destination: string;
  repository: string; status: "clone" | "reused"; requires_confirmation: boolean;
};
type OpenPlan = {
  plan_id: string; requires_confirmation: boolean;
  resources: { id: string; label: string; url: string }[];
};

function textResult(result: unknown, plan?: unknown) {
  return { content: [{ type: "text" as const, text: JSON.stringify(result) }], details: { plan, result } };
}

function cancelled() { return textResult({ ok: false, cancelled: true }); }

function checkSignal(signal?: AbortSignal) { signal?.throwIfAborted(); }

function validText(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && !/[\x00-\x1f\x7f]/.test(value);
}

function requireConfirmation(ctx: ExtensionContext, tool: string) {
  if (!ctx.hasUI) throw new Error(`${tool} requires an interactive confirmation channel; no host changes or browser launches were made`);
}

// Only these fixed CLI operations are reachable. The model cannot submit an
// executable, cwd, environment, URL to open, or an approval/auto-confirm flag.
async function operation(pi: ExtensionAPI, command: "clone-repository" | "open-resource", cwd: string,
  request: unknown, signal: AbortSignal | undefined, plan?: string): Promise<unknown> {
  checkSignal(signal);
  const binary = process.env.RADAR_BINARY?.trim() || "radar";
  const result = await pi.exec(binary, [command, "--workspace", resolve(cwd), "--request", JSON.stringify(request),
    "--json", ...(plan === undefined ? ["--preview"] : ["--plan", plan])],
    { signal, timeout: command === "clone-repository" && plan !== undefined ? 610_000 : 30_000 });
  checkSignal(signal);
  if (result.code !== 0) throw new Error(`radar_${command.replaceAll("-", "_")} failed: ${result.stderr.trim() || "host operation refused"}`);
  let value: unknown;
  try { value = JSON.parse(result.stdout); } catch { throw new Error(`${command} returned invalid JSON; no automatic retry`); }
  return value;
}

export function registerHostResourceTools(pi: ExtensionAPI) {
  pi.registerTool({
    name: "radar_clone_repository",
    label: "Clone Radar Source Repository",
    description: "Preview and clone a missing GitHub source repository on the host under a configured repository_dirs root. Initial cloning always requires explicit approval, regardless of workspace.auto_confirm. Matching existing source checkouts are reused without fetching or changing local work. No worktree attachment or project setup.",
    promptSnippet: "Clone a missing source repository on the host with mandatory approval",
    promptGuidelines: [
      "Inspect radar_workspace_context repository_dirs and repositories before cloning; pass a direct-child destination under an advertised canonical host root.",
      "Use this tool rather than cloning an unmanaged sandbox checkout or asking the user to run git clone. The tool obtains clone approval itself; do not ask for a separate approval first.",
      "After cloning, refresh radar_workspace_context, then use radar_repository_refs and radar_reconcile_workspace for branch selection and attachment.",
      "If approval is unavailable or refused, stop; do not bypass the tool through a shell or another host capability.",
    ],
    parameters: Type.Object({
      repository: Type.String({ description: "GitHub HTTPS remote or git@github.com:owner/repo.git", maxLength: 512 }),
      destination: Type.String({ description: "Absolute direct-child checkout path under repository_dirs from radar_workspace_context", maxLength: 4096 }),
    }, { additionalProperties: false }),
    executionMode: "sequential",
    async execute(_id, params, signal, _update, ctx) {
      const request = params as { repository: string; destination: string };
      const raw = await operation(pi, "clone-repository", ctx.cwd, request, signal);
      const plan = raw as ClonePlan | null;
      if (!plan || !validText(plan.plan_id) || !validText(plan.remote) || !validText(plan.repository_dir) ||
          !validText(plan.destination) || !validText(plan.repository) ||
          !((plan.status === "clone" && plan.requires_confirmation === true) ||
            (plan.status === "reused" && plan.requires_confirmation === false))) {
        throw new Error("radar_clone_repository returned an invalid preview; nothing applied");
      }
      if (plan.status === "reused") return textResult({ ok: true, status: "reused", repository: plan.repository, remote: plan.remote }, plan);
      requireConfirmation(ctx, "radar_clone_repository");
      checkSignal(signal);
      const approved = await ctx.ui.confirm("Clone repository on the host?",
        `Remote: ${plan.remote}\nRepository root: ${plan.repository_dir}\nDestination: ${plan.destination}\n\nThis approval is required independently of workspace.auto_confirm. No setup scripts or submodules will run.`);
      checkSignal(signal);
      if (approved !== true) return cancelled();
      const result = await operation(pi, "clone-repository", ctx.cwd, request, signal, plan.plan_id) as Record<string, unknown> | null;
      if (!result || typeof result.ok !== "boolean") throw new Error("radar_clone_repository returned an invalid apply result; inspect the destination before retrying");
      return textResult(result, plan);
    },
  });

  pi.registerTool({
    name: "radar_open_resource",
    label: "Open Radar Resources",
    description: "Open advertised current-task resources in the host default browser. Select IDs from radar_workspace_context.openable_resources, including GitHub PR Changes targets. Requires one explicit confirmation per batch, regardless of workspace.auto_confirm. No arbitrary URLs, files, HTTP fetching, browser inspection, or page content.",
    promptSnippet: "Open provider-advertised resources in the host browser with batch confirmation",
    promptGuidelines: [
      "Select resource_ids only from the current radar_workspace_context.openable_resources; never invent IDs or pass arbitrary URLs.",
      "The tool obtains one explicit confirmation for all exact destinations in the batch. Do not ask for another confirmation before calling it.",
      "Host browser traffic is outside sandbox network policy. Launch success does not establish that the page loaded.",
      "If resources are unavailable, refresh Radar task associations normally; do not bypass missing targets or refused/unavailable approval with shell commands, browser automation, or arbitrary URL opening.",
    ],
    parameters: Type.Object({
      resource_ids: Type.Array(Type.String({ maxLength: 512, minLength: 1 }), { minItems: 1, maxItems: 16, description: "Advertised opening target IDs from the current workspace context; one confirmation for the whole batch" }),
    }, { additionalProperties: false }),
    executionMode: "sequential",
    async execute(_id, params, signal, _update, ctx) {
      const request = params as { resource_ids: string[] };
      const raw = await operation(pi, "open-resource", ctx.cwd, request, signal);
      const plan = raw as OpenPlan | null;
      const selected = new Set(request.resource_ids);
      if (!plan || !validText(plan.plan_id) || plan.requires_confirmation !== true ||
          !Array.isArray(plan.resources) || plan.resources.length === 0 || plan.resources.length !== selected.size ||
          !plan.resources.every(item => item && validText(item.id) && selected.has(item.id) && validText(item.label) &&
            validText(item.url) && item.url.startsWith("https://")) ||
          new Set(plan.resources.map(item => item.id)).size !== plan.resources.length) {
        throw new Error("radar_open_resource returned an invalid preview; no browser launches were made");
      }
      requireConfirmation(ctx, "radar_open_resource");
      checkSignal(signal);
      const approved = await ctx.ui.confirm("Open this resource batch in the host browser?",
        plan.resources.map(item => `${item.label}\n${item.url}`).join("\n\n") +
        "\n\nBrowser traffic, redirects and authenticated sessions are outside sandbox network policy. This confirmation covers this batch only.");
      checkSignal(signal);
      if (approved !== true) return cancelled();
      const result = await operation(pi, "open-resource", ctx.cwd, request, signal, plan.plan_id) as Record<string, unknown> | null;
      if (!result || typeof result.ok !== "boolean" || !Array.isArray(result.results)) throw new Error("radar_open_resource returned an invalid result; do not retry successful launches blindly");
      return textResult(result, plan);
    },
  });
}
