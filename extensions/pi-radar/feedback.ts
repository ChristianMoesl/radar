import { resolve } from "node:path";
import { Type } from "typebox";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { isLinearTool, linearAvailable } from "./linear.ts";
import { radarPackageVersion } from "./version.ts";

export const feedbackLinearWorkspace = "christian-moesl";
export const feedbackLinearProject = "radar-f46332010943";

export function registerFeedbackSession(pi: ExtensionAPI) {
  let feedback = false;
  let origin: string | undefined;
  let registered = false;

  const available = () => feedback && linearAvailable(pi);
  function instructions(): string | undefined {
    if (!feedback) return undefined;
    if (!available()) return "Radar feedback is disabled because the MCP server named linear is unavailable in this Pi session. Explain that the user must enable/authenticate it in Pi and retry. Do not use a direct Linear API, install a provider CLI, copy credentials, or claim to submit an issue. Existing notes and conversation remain available.";
    return [
      "Radar feedback workspace — Improve Radar:",
      "- Your job is collaborative brainstorming, bug investigation and feature shaping, not implementation. Do not change Radar code, repair the host or change configuration unless separately requested.",
      "- Start from what the user experienced or wants. Do not require an upfront bug/feature form. Ask concise questions and distinguish facts, hypotheses, decisions and open questions.",
      "- Consult radar_documentation for the installed version. Use radar_diagnostics for scoped host inspection; keep source searches, notes and experiments inside the normal workspace execution boundary. Never mount live Radar state, sockets or secret directories as a shortcut.",
      `- Loaded pi-radar version: ${radarPackageVersion}. A source checkout may differ from the installed CLI or extension; label evidence accordingly.`,
      origin ? `- The originating workspace being investigated is ${JSON.stringify(origin)}, not this feedback workspace. radar_diagnostics inspects that registered origin when available.` : "- No originating workspace was captured; diagnose Radar generally without inventing a target.",
      "- Read notes.md first and preserve user content. Keep the evolving problem, evidence, decisions, alternatives, open questions and issue draft there so the conversation can be resumed. Do not add Radar task metadata to Markdown.",
      "- For bugs, shape reproduction steps, expected/actual behavior, impact and supporting evidence. A root-cause hypothesis is not a confirmed cause and is not required to file.",
      "- For features, shape the user problem, desired workflow, scope/non-goals, acceptance criteria and a grounded plan. Prefer the smallest viable change; do not start coding automatically.",
      `- The fixed issue destination is Linear workspace ${feedbackLinearWorkspace}, project ${feedbackLinearProject}. Discover search/read/create tools from the MCP server named exactly linear. Resolve the project/team through that MCP; use the project slug as the create request's project. Do not use another account or project.`,
      "- Search for relevant existing issues before creating one. Prepare the exact title, body and destination for review. The MCP write confirmation is the explicit approval step; do not ask for an additional confirmation beforehand. Publish the distilled issue, not transcripts, raw logs, private configuration or unrelated task data.",
      "- After confirmed MCP success, append the returned issue identifier/link to notes.md. If the result is ambiguous, inspect through the MCP before retrying; never blindly repeat a create. Filing an issue does not automatically complete or delete this workspace.",
    ].join("\n");
  }

  pi.on("tool_call", async (event, ctx) => {
    if (!feedback || !isLinearTool(event.toolName)) return;
    if (!available()) return { block: true, reason: "Radar feedback requires the linear MCP in this Pi session" };
    const tool = pi.getAllTools().find(tool => tool.name === event.toolName) as { annotations?: { readOnlyHint?: boolean } } | undefined;
    const createsIssue = /(?:__create_issue|__save_issue)$/.test(event.toolName);
    if (!createsIssue && tool?.annotations?.readOnlyHint === true) return;
    // Current Linear MCP creation tools accept the project's name/slug. Pin
    // creation to the destination above, including nested codemode calls.
    if (createsIssue && (event.input as Record<string, unknown>).project !== feedbackLinearProject) {
      return { block: true, reason: `Radar feedback issues must use project ${feedbackLinearProject}` };
    }
    if (!ctx.hasUI) return { block: true, reason: "Linear changes from Radar feedback require an interactive confirmation" };
    ctx.signal?.throwIfAborted();
    const approved = await ctx.ui.confirm("Allow this Linear change?",
      `Workspace: ${feedbackLinearWorkspace}\nProject: ${feedbackLinearProject}\nTool: ${event.toolName}\n\nExact MCP request:\n${JSON.stringify(event.input, null, 2)}\n\nPublish only the reviewed issue and approved evidence, not raw diagnostics or transcripts.`);
    ctx.signal?.throwIfAborted();
    if (!available()) return { block: true, reason: "The linear MCP became unavailable" };
    return approved === true ? undefined : { block: true, reason: "Linear change was not approved" };
  });

  function registerDiagnostics() {
    if (registered) return;
    registered = true;
    pi.registerTool({
      name: "radar_diagnostics",
      label: "Inspect Radar Internals",
      description: "Read scoped diagnostics from the host Radar instance in a feedback workspace. Overview includes version, cached daemon/provider health, allowlisted settings and originating-workspace resources. Logs require fresh confirmation and return at most 100 lines / 32 KiB; they may contain private values. No arbitrary paths, commands, SQL, state exports, credentials, refreshes or repairs.",
      promptSnippet: "Read scoped host Radar diagnostics; log access requires approval",
      promptGuidelines: [
        "Use radar_diagnostics rather than host shell commands or mounting live Radar state to investigate Radar itself.",
        "radar_diagnostics logs are private evidence, not automatic Linear attachments; redact and review anything proposed for publication.",
      ],
      parameters: Type.Object({
        section: Type.Optional(Type.Unsafe<"overview" | "logs">({ type: "string", enum: ["overview", "logs"], description: "Read the overview (default) or a bounded private daemon log tail" })),
      }, { additionalProperties: false }),
      executionMode: "sequential",
      async execute(_id, params, signal, _update, ctx) {
        if (!available()) throw new Error("Radar feedback diagnostics require the linear MCP in a feedback workspace");
        const section = (params as { section?: "overview" | "logs" }).section ?? "overview";
        signal?.throwIfAborted();
        if (section === "logs") {
          if (!ctx.hasUI) throw new Error("Reading private host Radar logs requires an interactive confirmation");
          const approved = await ctx.ui.confirm("Read private Radar daemon logs?",
            "Read at most the last 100 lines / 32 KiB of the configured host Radar log. Logs may contain private values from any workspace. This permits local inspection only, not publication to Linear.");
          signal?.throwIfAborted();
          if (approved !== true) return { content: [{ type: "text" as const, text: "Log access cancelled" }], details: { cancelled: true } };
        }
        if (!available()) throw new Error("The linear MCP became unavailable; no diagnostics read");
        const binary = process.env.RADAR_BINARY?.trim() || "radar";
        const result = await pi.exec(binary, ["feedback-diagnostics", "--workspace", resolve(ctx.cwd), "--section", section, "--json"], { signal, timeout: 15_000 });
        signal?.throwIfAborted();
        if (result.code !== 0) throw new Error("Radar diagnostics unavailable; update the host CLI or inspect the workspace registration. No host changes made.");
        const data = JSON.parse(result.stdout) as Record<string, unknown>;
        if (!data || data.section !== section) throw new Error("Invalid Radar diagnostics response");
        data.loaded_pi_radar_version = radarPackageVersion;
        return { content: [{ type: "text" as const, text: JSON.stringify(data) }], details: data };
      },
    });
  }

  return {
    instructions,
    update(context: { feedback?: boolean; feedback_origin?: string }, ctx: ExtensionContext) {
      feedback = context.feedback === true;
      origin = context.feedback_origin;
      if (available()) {
        registerDiagnostics();
        if (!pi.getActiveTools().includes("radar_diagnostics")) pi.setActiveTools([...pi.getActiveTools(), "radar_diagnostics"]);
      } else if (registered) {
        pi.setActiveTools(pi.getActiveTools().filter(name => name !== "radar_diagnostics"));
      }
      if (feedback && ctx.hasUI) ctx.ui.setStatus("radar-feedback", available() ? "Improve Radar" : "Improve Radar disabled · linear MCP unavailable");
    },
  };
}
