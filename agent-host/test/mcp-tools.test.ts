import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, writeFile, symlink, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";
import { BACKGROUND_CONTEXT, withAbortSignal } from "@earendil-works/chord/context";
import { Harness, createRegistry } from "@earendil-works/pi-durable";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { createModels } from "@earendil-works/pi-ai/models";
import { fauxProvider, fauxAssistantMessage, fauxToolCall } from "@earendil-works/pi-ai/providers/faux";
import { WorkspaceExecution } from "../src/environment.ts";
import { createMcpTools, mcpModelToolName } from "../src/mcp-tools.ts";
import { mcpInputValidator } from "../src/mcp-schema.ts";
import { AgentHost } from "../src/host.ts";
import { taskId } from "../src/index.ts";

async function fixture() {
  const dir = await mkdtemp(join(tmpdir(), "radar-scoped-mcp-"));
  const previous = process.env.RADAR_BINARY;
  const config = join(dir, "mcp.json"), state = join(dir, "state");
  const settings = join(dir, "settings.json"), calls = join(dir, "calls.jsonl"), executions = join(dir, "executions.jsonl");
  await writeFile(settings, "{}"); await writeFile(calls, "");
  await writeFile(config, JSON.stringify({ mcpServers: { fixture: {
    command: process.execPath,
    args: [resolve(import.meta.dirname, "fixtures/scoped-mcp.mjs"), settings, calls],
  } } }));
  const pkg = join(dir, "node_modules/@christianmoesl/radar-agent-host");
  await mkdir(pkg, { recursive: true });
  await writeFile(join(pkg, "package.json"), JSON.stringify({ type: "module", exports: { "./mcp": "./mcp.ts" } }));
  await symlink(resolve(import.meta.dirname, "../src/mcp.ts"), join(pkg, "mcp.ts"));
  const radar = join(dir, "radar.mjs");
  await writeFile(radar, `#!${process.execPath}
import {appendFileSync} from 'node:fs';
import {execFile} from 'node:child_process';
const args=process.argv.slice(2),binding=JSON.parse(args[args.indexOf('--binding')+1]);
appendFileSync(${JSON.stringify(executions)},JSON.stringify({args,binding})+'\\n');
if(binding.mode==='sbx'){process.stderr.write('unavailable sandbox; private credential must not escape');process.exit(1)}
const argv=args.slice(args.indexOf('--')+1);
const child=execFile(argv[0],argv.slice(1),{cwd:binding.path,maxBuffer:2*1024*1024},(error,stdout)=>{
 if(error){process.stderr.write('private process error');process.exitCode=1}else process.stdout.write(JSON.stringify({output:stdout}));
});
process.once('SIGTERM',()=>{child.kill('SIGTERM');process.exitCode=1});
`, { mode: 0o700 });
  process.env.RADAR_BINARY = radar;
  return {
    dir,
    config: { config, state, connections: [{ server: "fixture", tools: ["read"] }] },
    execution: new WorkspaceExecution({ id: "fixture", path: dir, mode: "local" }),
    settings: (value: unknown) => writeFile(settings, JSON.stringify(value)),
    calls: async () => (await readFile(calls, "utf8")).trim().split("\n").filter(Boolean).map(line => JSON.parse(line)),
    executions: async () => (await readFile(executions, "utf8")).trim().split("\n").filter(Boolean).map(line => JSON.parse(line)),
    close: async () => {
      if (previous === undefined) delete process.env.RADAR_BINARY; else process.env.RADAR_BINARY = previous;
      await rm(dir, { recursive: true, force: true });
    },
  };
}

test("MCP schemas strictly validate without coercion, defaults or remote resolution", () => {
  for (const dialect of [undefined, "http://json-schema.org/draft-07/schema#"]) {
    const check = mcpInputValidator({ ...(dialect ? { $schema: dialect } : {}), type: "object",
      properties: { count: { type: "integer", minimum: 1, default: 2 } }, required: ["count"], additionalProperties: false });
    check({ count: 1 });
    for (const args of [{ count: "1" }, { count: 0 }, { count: 1, extra: true }, {}, null, []])
      assert.throws(() => check(args), /arguments do not match/);
  }
  for (const schema of [{ type: "bad" }, { type: "object", properties: { x: { type: "bad" } } },
    { type: "object", properties: { x: { $ref: "https://example.test/schema" } } },
    { type: "object", unsupportedValidation: true }, { type: "object", $async: true }])
    assert.throws(() => mcpInputValidator(schema), /invalid or unsupported/);
});

test("workspace MCP installs only exact native tools and preserves selection across reconnect", async () => {
  const f = await fixture();
  try {
    const selected = await createMcpTools(f.execution, f.config);
    const tool = selected.extension.tools![0]!;
    assert.equal(selected.extension.tools!.length, 1);
    assert.equal(tool.name, mcpModelToolName("fixture", "read"));
    assert.equal(tool.replay, "safe");
    assert.deepEqual(selected.inspection.execution, f.execution.binding);
    const attempts = (await f.executions()).length;
    await assert.rejects(selected.read("fixture", "write", { count: 1 }), /not explicitly selected/);
    await assert.rejects(selected.read("guessed", "read", { count: 1 }), /not explicitly selected/);
    assert.equal((await f.executions()).length, attempts);
    f.config.connections[0]!.tools.push("write");
    for (const args of [{ count: "1" }, { count: 8 }, {}, { count: 1, extra: true }]) {
      assert.throws(() => tool.prepareArguments!(args), /arguments do not match/);
      await assert.rejects(tool.execute(args, {} as never, BACKGROUND_CONTEXT), /arguments do not match/);
    }
    assert.equal((await f.calls()).length, 0);
    const result = await tool.execute({ count: 2 }, {} as never, BACKGROUND_CONTEXT);
    assert.match(JSON.stringify(result.output), /synthetic evidence/);
    await f.settings({ extra: true });
    await tool.execute({ count: 3 }, {} as never, BACKGROUND_CONTEXT);
    const restarted = await createMcpTools(f.execution, { ...f.config, connections: [{ server: "fixture", tools: ["read"] }] });
    assert.deepEqual(restarted.extension.tools!.map(t => t.name), [tool.name]);
    assert.deepEqual((await f.calls()).map(call => call.name), ["read", "read"]);
    for (const settings of [{ missing: true }, { duplicate: true }, { invalidSchema: true }]) {
      await f.settings(settings);
      await assert.rejects(tool.execute({ count: 1 }, {} as never, BACKGROUND_CONTEXT), /Workspace MCP fixture failed/);
    }
    assert.equal((await f.calls()).length, 2, "changed/missing schemas must fail before dispatch");
  } finally { await f.close(); }
});

test("workspace MCP rejects unavailable SBX without using host credentials or execution", async () => {
  const f = await fixture();
  try {
    const execution = new WorkspaceExecution({ id: "fixture", path: f.dir, mode: "sbx", sandbox: "selected" });
    await assert.rejects(createMcpTools(execution, f.config), error => {
      assert.match(String(error), /in sbx.*No host fallback/);
      assert.doesNotMatch(String(error), /private credential/);
      return true;
    });
    assert.equal((await f.calls()).length, 0);
    const attempts = await f.executions();
    assert.equal(attempts.length, 1);
    assert.equal(attempts[0].binding.mode, "sbx");
    assert.equal(attempts[0].binding.sandbox, "selected");
    assert.ok(!attempts[0].args.some((arg: string) => /private credential/.test(arg)));
  } finally { await f.close(); }
});

test("workspace MCP failures, cancellation and oversized evidence never become all-clear", async () => {
  const f = await fixture();
  try {
    const { extension } = await createMcpTools(f.execution, f.config);
    const tool = extension.tools![0]!;
    for (const settings of [{ partial: true }, { error: true }, { oversized: true }]) {
      await f.settings(settings);
      await assert.rejects(tool.execute({ count: 1 }, {} as never, BACKGROUND_CONTEXT), /incomplete|Workspace MCP fixture failed/);
    }
    const before = (await f.executions()).length;
    await assert.rejects(tool.execute({ count: 1 }, {} as never,
      withAbortSignal(AbortSignal.abort(), BACKGROUND_CONTEXT)), /Workspace MCP fixture failed/);
    assert.equal((await f.executions()).length, before);
    await f.settings({ delay: true });
    await assert.rejects(tool.execute({ count: 1 }, {} as never,
      withAbortSignal(AbortSignal.timeout(200), BACKGROUND_CONTEXT)), /Workspace MCP fixture failed/);
  } finally { await f.close(); }
});

test("native durable registry offers only selected schemas and persists reads while rejecting guessed tools", async () => {
  const f = await fixture();
  let harness: Harness | undefined;
  try {
    const { extension } = await createMcpTools(f.execution, f.config);
    const name = extension.tools![0]!.name;
    const models = createModels(), faux = fauxProvider();
    models.setProvider(faux.provider);
    faux.setResponses([
      context => {
        assert.deepEqual(context.messages.flatMap(message => message.role === "system" ? message.toolsAdded ?? [] : []).map(tool => tool.name), [name]);
        return fauxAssistantMessage([
          fauxToolCall(name, { count: 2 }),
          fauxToolCall(mcpModelToolName("fixture", "write"), { count: 1 }),
          fauxToolCall(name, { count: "1" }),
        ], { stopReason: "toolUse" });
      },
      fauxAssistantMessage("Fixture check complete; synthetic evidence only."),
    ]);
    const registry = createRegistry(); registry.install(extension);
    const db = join(f.dir, "durable.sqlite");
    harness = await Harness.open(await openNodeSqliteStorage(db), { models, registry }, BACKGROUND_CONTEXT);
    const root = await harness.root(BACKGROUND_CONTEXT);
    await root.configure({ model: { provider: "faux", modelId: "faux-1" } }, BACKGROUND_CONTEXT);
    harness.resume();
    const input = await root.submit({ type: "input", content: "Read fixture evidence" }, BACKGROUND_CONTEXT);
    await input.wait(BACKGROUND_CONTEXT);
    const messages = (await root.context(BACKGROUND_CONTEXT)).messages;
    const results = messages.filter(message => message.role === "toolResult");
    assert.equal(results.length, 3);
    assert.equal(results.filter(result => result.isError).length, 2);
    assert.match(JSON.stringify(results), /synthetic evidence/);
    assert.equal((await f.calls()).length, 1, "only the selected valid native call may reach the server");
    await harness.close(BACKGROUND_CONTEXT); harness = undefined;
    const reopened = await createMcpTools(f.execution, f.config);
    registry.install(reopened.extension);
    harness = await Harness.open(await openNodeSqliteStorage(db), { models, registry }, BACKGROUND_CONTEXT);
    const restored = await harness.root(BACKGROUND_CONTEXT);
    assert.match(JSON.stringify((await restored.context(BACKGROUND_CONTEXT)).messages), /synthetic evidence/);
    assert.equal((await restored.agent(BACKGROUND_CONTEXT)).tools.length, 1);
  } finally { await harness?.close(BACKGROUND_CONTEXT); await f.close(); }
});

test("calendar event example persists fixed MCP evidence in Radar and flags incomplete checks", async () => {
  const f = await fixture();
  let host: AgentHost | undefined;
  try {
    const input = {
      id: "scoped-check", dataDir: join(f.dir, "agent"), workspace: f.execution.binding,
      config: { mcp: f.config, reads: [{ server: "fixture", tool: "read", arguments: { count: 2 } }] },
    };
    const entry = resolve(import.meta.dirname, "../examples/mcp-check.ts");
    host = await AgentHost.open(entry, input);
    const event = {
      source: "radar.schedule", id: "schedule-one:2026-11-02T08:00:00Z",
      payload: { schedule: { id: "schedule-one", scheduledAt: "2026-11-02T08:00:00Z" },
        data: { tool: "write", arguments: { count: 5 } } },
    };
    const receipt = await host.deliver(event);
    const settled = await host.harness.waitForTask(taskId(receipt.taskId), BACKGROUND_CONTEXT);
    assert.equal(settled.state.outcome.status, "completed");
    const first = await host.snapshot();
    assert.equal(first.presentation.attention, false);
    assert.match(first.presentation.summary, /synthetic evidence/);
    assert.match(first.presentation.summary, /2026-11-02T08:00:00Z/);
    assert.deepEqual((await f.calls()).map(call => [call.name, call.arguments]), [["read", { count: 2 }]]);
    await host.close(); host = undefined;
    host = await AgentHost.open(entry, input);
    assert.equal((await host.snapshot()).presentation.summary, first.presentation.summary);
    assert.equal((await host.deliver(event)).taskId, receipt.taskId);
    assert.equal((await f.calls()).length, 1);
    await f.settings({ partial: true });
    const partial = await host.deliver({ ...event, id: "schedule-one:2026-11-03T08:00:00Z" });
    await host.harness.waitForTask(taskId(partial.taskId), BACKGROUND_CONTEXT);
    const after = await host.snapshot();
    assert.equal(after.presentation.attention, true);
    assert.match(after.presentation.summary, /Incomplete MCP check/);
    assert.doesNotMatch(after.presentation.summary, /not a health assessment/);
  } finally { await host?.close(); await f.close(); }
});
