import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, type TestContext } from "node:test";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import radar from "../../extensions/pi-radar/index.ts";

type Handler = (event: any, ctx: any) => any;

async function harness(t: TestContext, registered: unknown = true) {
  const root = await mkdtemp(join(tmpdir(), "pi-radar-test-"));
  const environment = { ...process.env };
  t.after(async () => {
    for (const key of Object.keys(process.env)) if (!(key in environment)) delete process.env[key];
    Object.assign(process.env, environment);
    await rm(root, { recursive: true, force: true });
  });
  process.env.TMPDIR = root;
  delete process.env.RADAR_HOST_TMPDIR;
  delete process.env.RADAR_BINARY;
  process.env.XDG_CONFIG_HOME = join(root, "config");
  await mkdir(join(root, "config", "radar"), { recursive: true });
  await writeFile(join(root, "config", "radar", "AGENTS.md"), "Radar fixture instructions");
  const hooks = new Map<string, Handler[]>();
  const tools = new Map<string, any>();
  const commands = new Map<string, any>();
  const calls: { binary: string; args: string[]; options: any }[] = [];
  const notices: string[] = [];
  const confirmations: string[] = [];
  const messages: string[] = [];
  const entries: any[] = [];
  const responses: any[] = [];
  const approvals: boolean[] = [];
  let context: any = { registered: true, workspace_path: root, members: [] };
  let inspectionFails = false;
  let registrationFails = false;
  let registrationCode = 0;
  let registrationOutput = JSON.stringify({ registered });
  let reloads = 0;
  const ctx = {
    cwd: root,
    signal: undefined,
    hasUI: true,
    isProjectTrusted: () => true,
    sessionManager: { getEntries: () => entries },
    ui: {
      notify: (message: string) => notices.push(message),
      confirm: async (title: string, message: string) => {
        confirmations.push(`${title}\n${message}`);
        return approvals.shift() ?? false;
      },
    },
    reload: async () => { reloads++; },
  };
  const pi = {
    on: (event: string, handler: Handler) => hooks.set(event, [...(hooks.get(event) ?? []), handler]),
    registerTool: (tool: any) => tools.set(tool.name, tool),
    registerCommand: (name: string, command: any) => commands.set(name, command),
    appendEntry: (customType: string, data: any) => entries.push({ type: "custom", customType, data }),
    sendUserMessage: (message: string) => messages.push(message),
    exec: async (binary: string, args: string[], options: any) => {
      calls.push({ binary, args, options });
      if (args.includes("--registration-only")) {
        if (registrationFails) throw Error("Radar unavailable");
        return { code: registrationCode, stdout: registrationOutput, stderr: "" };
      }
      if (args[0] === "workspace-context") {
        if (inspectionFails) throw Error("Host resource inspection failed");
        return { code: 0, stdout: JSON.stringify(context), stderr: "" };
      }
      if (args[0] === "reconcile-workspace") {
        assert.ok(responses.length, "unexpected reconciliation operation");
        return { code: 0, stdout: JSON.stringify(responses.shift()), stderr: "" };
      }
      return { code: 0, stdout: "{}", stderr: "" };
    },
  };
  radar(pi as unknown as ExtensionAPI);
  async function emit(event: string, data: any = {}) {
    let result;
    for (const handler of [...(hooks.get(event) ?? [])]) result = await handler(data, ctx);
    return result;
  }
  return {
    root, hooks, tools, commands, calls, notices, confirmations, messages, entries, responses, approvals, ctx,
    emit,
    start: () => emit("session_start", { reason: "startup" }),
    resources: (reason = "startup") => emit("resources_discover", { cwd: ctx.cwd, reason }),
    prompt: () => emit("before_agent_start", { systemPrompt: "Base prompt" }),
    setContext: (value: any) => { context = value; },
    failInspection: () => { inspectionFails = true; },
    failRegistration: () => { registrationFails = true; },
    registrationExitCode: (value: number) => { registrationCode = value; },
    registrationOutput: (value: string) => { registrationOutput = value; },
    reloads: () => reloads,
    reconcile: () => tools.get("radar_reconcile_workspace").execute("call", {
      revision: "revision", desired: { note: null, worktrees: [], sandbox: null },
    }, undefined, undefined, ctx),
  };
}

for (const registered of [false, undefined, "true"]) {
  test(`does not activate without affirmative registration (${registered})`, async (t) => {
    const h = await harness(t, registered);
    if (registered === undefined) h.registrationOutput("{}");
    await h.start();
    await h.resources();
    assert.equal(await h.prompt(), undefined);
    await h.emit("agent_start");
    await h.emit("agent_settled");
    await h.emit("session_shutdown");
    assert.equal(h.tools.size, 0);
    assert.equal(h.commands.size, 0);
    assert.equal(h.calls.length, 1);
    assert.deepEqual(h.entries, []);
    assert.deepEqual(h.notices, []);
    assert.equal(process.env.TMPDIR, h.root);
  });
}

for (const failure of ["unavailable", "malformed", "nonzero"]) {
  test(`stays inactive when registration is ${failure}`, async (t) => {
    const h = await harness(t);
    if (failure === "unavailable") h.failRegistration();
    else if (failure === "nonzero") h.registrationExitCode(1);
    else h.registrationOutput("not JSON");
    await h.start();
    assert.equal(h.tools.size, 0);
    assert.equal(h.commands.size, 0);
    assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0].options.timeout, 5000);
  });
}

test("activates from the session cwd, not the process cwd, exactly once", async (t) => {
  const h = await harness(t);
  h.ctx.cwd = join(h.root, "member", "src");
  await h.start();
  await h.start();
  assert.deepEqual([...h.tools.keys()], ["radar_workspace_context", "radar_repository_refs", "radar_reconcile_workspace"]);
  assert.deepEqual([...h.commands.keys()], ["radar-reload-workspace-resources"]);
  assert.deepEqual(h.calls[0].args, ["workspace-context", "--registration-only", "--workspace", h.ctx.cwd]);
  assert.equal(h.calls[0].binary, "radar");
  assert.deepEqual(h.calls[1].args, ["activity", "idle"]);
  assert.equal(h.calls.length, 2);
  await h.emit("agent_start");
  await h.emit("agent_settled");
  await h.emit("session_shutdown");
  assert.deepEqual(h.calls.slice(2).map(call => call.args), [["activity", "busy"], ["activity", "idle"], ["activity", "idle"]]);
});

test("keeps repair tools available when full workspace inspection fails", async (t) => {
  const h = await harness(t);
  h.failInspection();
  await h.start();
  assert.equal(h.tools.size, 3);
  assert.deepEqual(await h.resources(), { skillPaths: [] });
  assert.ok((await h.prompt()).systemPrompt.includes("Radar fixture instructions"));
});

test("loads scoped instructions and trusted skills; excludes duplicate skill names", async (t) => {
  const h = await harness(t);
  const skills = join(h.root, "member", ".agents", "skills");
  const instruction = join(h.root, "member", "AGENTS.md");
  for (const [directory, name] of [["one", "unique"], ["two", "duplicate"], ["three", "duplicate"]]) {
    await mkdir(join(skills, directory), { recursive: true });
    await writeFile(join(skills, directory, "SKILL.md"), `---\nname: ${name}\ndescription: Fixture skill\n---\nFixture`);
  }
  await writeFile(instruction, "Repository fixture instructions");
  h.setContext({ registered: true, members: [{ instruction_files: [instruction], skill_paths: [skills] }] });
  await h.start();
  const resources = await h.resources();
  assert.deepEqual(resources.skillPaths, [join(skills, "one", "SKILL.md")]);
  assert.ok(h.notices.some(notice => notice.includes("duplicate skill duplicate")));
  const prompt = (await h.prompt()).systemPrompt;
  assert.ok(prompt.includes("Radar fixture instructions"));
  assert.ok(prompt.includes(`<project_instructions path="${instruction}">`));
  assert.ok(prompt.includes("Repository fixture instructions"));
  assert.ok(prompt.includes("repository context file apply only to files under that repository"));
  h.ctx.isProjectTrusted = () => false;
  assert.deepEqual((await h.resources("reload")).skillPaths, []);
  assert.ok(h.notices.some(notice => notice.includes("workspace is not trusted")));
});

test("restores shared TMPDIR before Pi replaces the session", async (t) => {
  const h = await harness(t);
  const shared = join(h.root, "shared");
  await mkdir(shared);
  h.setContext({ members: [], sandbox: { shared_directory: shared, shared_directory_ready: true } });
  await h.start();
  await h.resources();
  assert.equal(process.env.TMPDIR, shared);
  assert.equal(process.env.RADAR_HOST_TMPDIR, h.root);
  await h.emit("session_shutdown");
  assert.equal(process.env.TMPDIR, h.root);
});

test("reload command uses Pi's resource reload without replacing the conversation", async (t) => {
  const h = await harness(t);
  await h.start();
  await h.commands.get("radar-reload-workspace-resources").handler("", h.ctx);
  assert.equal(h.reloads(), 1);
});

const plan = { workspace_name: "fixture", revision: "revision", next_revision: "next", plan_id: "plan-one", changes: [{ action: "add", resource: "worktree", summary: "Add fixture member" }] };

test("denied reconciliation never applies", async (t) => {
  const h = await harness(t);
  await h.start();
  h.responses.push(plan);
  const result = await h.reconcile();
  assert.equal(JSON.parse(result.content[0].text).cancelled, true);
  assert.equal(h.confirmations.length, 1);
  assert.equal(h.calls.filter(call => call.args[0] === "reconcile-workspace").length, 1);
});

test("headless reconciliation fails closed unless auto-confirm is configured", async (t) => {
  const h = await harness(t);
  await h.start();
  h.ctx.hasUI = false;
  h.responses.push(plan);
  await assert.rejects(h.reconcile(), /requires an interactive confirmation channel/);
  h.responses.push({ ...plan, auto_confirm: true }, { ok: true });
  await h.reconcile();
  assert.equal(h.confirmations.length, 0);
});

test("changed plans require renewed confirmation and use the new plan ID", async (t) => {
  const h = await harness(t);
  await h.start();
  const changed = { ...plan, plan_id: "plan-two", warnings: ["Changed observation"] };
  h.responses.push(plan, { reconfirm_required: true, reason: "plan_changed", plan: changed }, { ok: true, worktrees_added: 1 });
  h.approvals.push(true, true);
  await h.reconcile();
  assert.equal(h.confirmations.length, 2);
  assert.ok(h.confirmations[1].includes("Changed observation"));
  assert.ok(h.calls.at(-1)?.args.includes("plan-two"));
  assert.deepEqual(h.messages, ["/radar-reload-workspace-resources"]);
});

test("partial reconciliation retains progress and queues a resource reload", async (t) => {
  const h = await harness(t);
  await h.start();
  h.responses.push({ ...plan, auto_confirm: true }, { ok: false, retryable: true, worktrees_added: 1, sandbox_reconciled: false });
  const result = await h.reconcile();
  assert.ok(result.content[0].text.includes("1 worktree added"));
  assert.ok(result.content[0].text.includes("Re-inspect and retry"));
  assert.deepEqual(h.messages, ["/radar-reload-workspace-resources"]);
});
