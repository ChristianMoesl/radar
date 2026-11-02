import assert from "node:assert/strict";
import { test } from "node:test";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { registerFeedbackSession, feedbackLinearProject, feedbackLinearWorkspace } from "../../extensions/pi-radar/feedback.ts";
import { linearAvailable } from "../../extensions/pi-radar/linear.ts";

function harness() {
  const tools = new Map<string, any>();
  tools.set("mcp__linear__save_issue", { name: "mcp__linear__save_issue", exposure: "codemode" });
  tools.set("mcp__linear__get_project", { name: "mcp__linear__get_project", exposure: "codemode", annotations: { readOnlyHint: true } });
  let active = ["read", "codemode"];
  const hooks = new Map<string, any>();
  const calls: any[] = [];
  const confirmations: string[] = [];
  const approvals: unknown[] = [];
  const statuses: string[] = [];
  const ctx: any = { cwd: "/work/feedback", hasUI: true, ui: {
    setStatus: (_key: string, value: string) => statuses.push(value),
    confirm: async (title: string, body: string) => { confirmations.push(`${title}\n${body}`); return approvals.shift() ?? false; },
  } };
  const pi = {
    getAllTools: () => [...tools.values()],
    getActiveTools: () => active,
    setActiveTools: (names: string[]) => { active = names; },
    registerTool: (tool: any) => { tools.set(tool.name, tool); active.push(tool.name); },
    on: (event: string, handler: any) => hooks.set(event, handler),
    exec: async (binary: string, args: string[], options: any) => {
      calls.push({ binary, args, options });
      return { code: 0, stdout: JSON.stringify({ section: args[args.indexOf("--section") + 1], version: "v1.2.3" }) };
    },
  } as unknown as ExtensionAPI;
  const session = registerFeedbackSession(pi);
  return { pi, session, tools, ctx, hooks, calls, confirmations, approvals, statuses,
    activate: () => session.update({ feedback: true, feedback_origin: "/work/origin" }, ctx),
    setActive: (names: string[]) => { active = names; },
    diagnostic: (section = "overview", signal?: AbortSignal) => tools.get("radar_diagnostics").execute("call", { section }, signal, undefined, ctx),
    linear: (toolName = "mcp__linear__save_issue", input: any = { title: "Example", description: "Reviewed body", project: feedbackLinearProject }) => hooks.get("tool_call")({ toolName, input, parentToolCallId: "codemode-call" }, ctx),
  };
}

test("only the exact reachable linear MCP namespace enables feedback", () => {
  const h = harness();
  assert.equal(linearAvailable(h.pi), true);
  h.tools.delete("mcp__linear__save_issue");
  assert.equal(linearAvailable(h.pi), false); // A read-only connection cannot file issues.
  h.tools.clear();
  for (const name of ["linear", "linear_create_issue", "mcp__not_linear__create_issue", "mcp__linear_other__create_issue"]) h.tools.set(name, { name, exposure: "codemode" });
  assert.equal(linearAvailable(h.pi), false);
  h.tools.set("mcp__linear__save_issue", { name: "mcp__linear__save_issue", exposure: "hidden" });
  assert.equal(linearAvailable(h.pi), false);
  h.tools.set("mcp__linear__save_issue", { name: "mcp__linear__save_issue", exposure: "direct" });
  assert.equal(linearAvailable(h.pi), false);
  h.setActive(["mcp__linear__save_issue"]);
  assert.equal(linearAvailable(h.pi), true);
  h.tools.set("mcp__linear__save_issue", { name: "mcp__linear__save_issue", exposure: "deferred" });
  h.setActive(["tool_search"]);
  assert.equal(linearAvailable(h.pi), true);
  h.setActive(["read"]);
  assert.equal(linearAvailable(h.pi), false);
});

test("ordinary workspaces gain no diagnostic tool, instructions or Linear gate", async () => {
  const h = harness();
  h.session.update({}, h.ctx);
  assert.equal(h.session.instructions(), undefined);
  assert.equal(h.tools.has("radar_diagnostics"), false);
  assert.equal(await h.linear(), undefined);
  assert.deepEqual(h.confirmations, []);
});

test("feedback restores its purpose, origin, fixed destination and shaping instructions", () => {
  const h = harness();
  h.activate();
  const instructions = h.session.instructions()!;
  for (const text of ["christian-moesl", feedbackLinearProject, "/work/origin", "notes.md", "not implementation", "Search for relevant existing issues", "After confirmed MCP success"]) assert.ok(instructions.includes(text), text);
  assert.ok(h.tools.has("radar_diagnostics"));
  assert.equal(h.statuses.at(-1), "Improve Radar");
});

test("feedback proactively guides classification, targeted rounds, readiness and a resumable handoff", () => {
  const h = harness();
  h.activate();
  const instructions = h.session.instructions()!;
  for (const text of [
    "After the initial description, suggest bug report or feature request",
    "confirm or correct that classification",
    "Reuse an already-confirmed classification",
    "one to three questions per round, only about missing information",
    "Reuse information already provided in the conversation and notes.md",
    "do not repeat a fixed checklist",
    "proactively announce that the report is ready to file",
    "Do not wait for the user",
    "only if it materially changes the report",
    "label unresolved assumptions separately from confirmed decisions",
    "Present the exact proposed title, body and destination together",
    "lead directly into the existing MCP write confirmation",
    "do not ask for an additional confirmation beforehand",
    "never submit without approval",
    "return the issue identifier/link to the user and append it to notes.md",
    "Do not claim creation or save an issue identifier/link before confirmed success",
    "never blindly repeat a create",
    "does not automatically implement the request or close, complete or delete this workspace",
  ]) assert.ok(instructions.includes(text), text);
  assert.ok(instructions.indexOf("Search for relevant existing issues") < instructions.indexOf("Present the exact proposed title"));
  assert.ok(instructions.indexOf("Present the exact proposed title") < instructions.indexOf("After confirmed MCP success"));
  h.activate(); // Guidance survives resume/reload without adding a new wizard/state.
  assert.equal(h.session.instructions(), instructions);
  assert.deepEqual(h.confirmations, []);
  assert.deepEqual(h.calls, []);
});

for (const scenario of [
  {
    kind: "bug report",
    guidance: ["For bugs", "reproduction steps", "expected/actual behavior", "impact", "supporting evidence", "not required to file"],
    title: "Task list does not refresh after adding a port",
    description: "## Reproduction\nAdd a configured forward and refresh the task list.\n\n## Expected versus actual\nThe new host port should appear but is absent.\n\n## Impact and evidence\nThe local development link is unavailable; a screenshot is available.\n\n## Assumptions\nRoot cause remains unconfirmed; it is not required to file.",
  },
  {
    kind: "feature request",
    guidance: ["For features", "user problem", "desired workflow", "scope/non-goals", "acceptance criteria", "grounded plan", "do not start coding automatically"],
    title: "Make local development links easier to discover",
    description: "## User problem and desired workflow\nOpen configured development links from the task row.\n\n## Scope and non-goals\nUse registered host ports; do not scan services.\n\n## Acceptance criteria\nList each configured host URL and omit empty entries.\n\n## Plan\nReuse the existing link picker.\n\n## Assumptions\nNo HTTP availability is inferred.",
  },
]) {
  test(`${scenario.kind} guidance leads to reviewed submission without bypassing the existing MCP approval`, async () => {
    const h = harness();
    h.activate();
    const instructions = h.session.instructions()!;
    for (const text of scenario.guidance) assert.ok(instructions.includes(text), text);
    h.tools.set("mcp__linear__list_issues", { name: "mcp__linear__list_issues", annotations: { readOnlyHint: true } });
    assert.equal(await h.linear("mcp__linear__get_project", { query: feedbackLinearProject }), undefined);
    assert.equal(await h.linear("mcp__linear__list_issues", { project: feedbackLinearProject, query: scenario.title }), undefined);
    assert.equal(h.confirmations.length, 0); // Readiness and related-issue reads are not extra approval dialogs.
    const draft = { title: scenario.title, description: scenario.description, project: feedbackLinearProject };
    assert.equal((await h.linear("mcp__linear__save_issue", draft)).block, true);
    assert.equal(h.confirmations.length, 1);
    assert.ok(h.confirmations[0].includes(`Workspace: ${feedbackLinearWorkspace}`));
    assert.ok(h.confirmations[0].includes(`Project: ${feedbackLinearProject}`));
    assert.ok(h.confirmations[0].includes(JSON.stringify(draft, null, 2)));
    h.approvals.push(true);
    assert.equal(await h.linear("mcp__linear__save_issue", draft), undefined);
    assert.equal(h.confirmations.length, 2); // One exact-request confirmation per write attempt.
    assert.deepEqual(h.calls, []); // No host command or direct API/CLI submission fallback.
  });
}

test("unavailable MCP disables feedback; connecting later enables it without configuration reads", () => {
  const h = harness();
  const tools = [...h.tools];
  h.tools.clear();
  h.activate();
  assert.equal(h.tools.has("radar_diagnostics"), false);
  assert.ok(h.session.instructions()!.includes("disabled"));
  for (const [name, tool] of tools) h.tools.set(name, tool);
  h.activate();
  assert.ok(h.tools.has("radar_diagnostics"));
  assert.deepEqual(h.calls, []);
});

test("diagnostics use fixed host argv, bounded timeout and no arbitrary host access", async () => {
  const h = harness();
  h.activate();
  const result = await h.diagnostic();
  assert.equal(JSON.parse(result.content[0].text).section, "overview");
  assert.ok(JSON.parse(result.content[0].text).loaded_pi_radar_version);
  assert.deepEqual(h.calls[0].args, ["feedback-diagnostics", "--workspace", "/work/feedback", "--section", "overview", "--json"]);
  assert.equal(h.calls[0].options.timeout, 15_000);
  assert.deepEqual(h.confirmations, []);
});

for (const condition of ["headless", "denied", "cancelled", "approved"]) {
  test(`private logs require a fresh confirmation: ${condition}`, async () => {
    const h = harness();
    h.activate();
    h.ctx.hasUI = condition !== "headless";
    h.approvals.push(condition === "approved" ? true : condition === "cancelled" ? undefined : false);
    if (condition === "headless") await assert.rejects(h.diagnostic("logs"), /interactive confirmation/);
    else await h.diagnostic("logs");
    assert.equal(h.calls.length, condition === "approved" ? 1 : 0);
    if (condition !== "headless") assert.ok(h.confirmations[0].includes("not publication to Linear"));
    if (condition === "approved") {
      await h.diagnostic("logs");
      assert.equal(h.confirmations.length, 2);
      assert.equal(h.calls.length, 1);
    }
  });
}

test("aborted diagnostic/log approval never executes a host command", async () => {
  const h = harness();
  h.activate();
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(h.diagnostic("logs", controller.signal), /abort/i);
  assert.deepEqual(h.confirmations, []);
  assert.deepEqual(h.calls, []);
});

test("MCP loss withdraws diagnostics and denies existing tool references", async () => {
  const h = harness();
  h.activate();
  h.tools.delete("mcp__linear__save_issue");
  h.tools.delete("mcp__linear__get_project");
  h.activate();
  assert.ok(!h.pi.getActiveTools().includes("radar_diagnostics"));
  await assert.rejects(h.diagnostic(), /require the linear MCP/);
  assert.equal((await h.linear()).block, true);
  assert.deepEqual(h.calls, []);
});

test("Linear read-only calls pass; every write including a nested MCP call needs exact-request approval", async () => {
  const h = harness();
  h.activate();
  assert.equal(await h.linear("mcp__linear__get_project", { query: feedbackLinearProject }), undefined);
  assert.equal(h.confirmations.length, 0);
  assert.equal((await h.linear()).block, true);
  assert.ok(h.confirmations[0].includes('"title": "Example"'));
  assert.ok(h.confirmations[0].includes('"description": "Reviewed body"'));
  h.approvals.push(true);
  assert.equal(await h.linear(), undefined);
  assert.equal(h.confirmations.length, 2);
  assert.equal((await h.linear(undefined, { project: "another-project" })).block, true);
  assert.equal(h.confirmations.length, 2);
  h.ctx.hasUI = false;
  assert.equal((await h.linear()).block, true);
  assert.equal(h.confirmations.length, 2);
  assert.deepEqual(h.calls, []); // MCP calls remain Pi-owned; no API/CLI fallback.
});


test("issue creation cannot bypass destination/approval through advisory read-only annotations", async () => {
  const h = harness();
  h.tools.get("mcp__linear__save_issue").annotations = { readOnlyHint: true };
  h.activate();
  assert.equal((await h.linear()).block, true);
  assert.equal(h.confirmations.length, 1);
  assert.equal((await h.linear(undefined, { project: "another-project" })).block, true);
  assert.equal(h.confirmations.length, 1);
});
