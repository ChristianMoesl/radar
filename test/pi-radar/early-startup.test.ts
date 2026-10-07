import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, realpath, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { performance } from "node:perf_hooks";
import { test, type TestContext } from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

const repository = fileURLToPath(new URL("../../", import.meta.url));
const cli = join(dirname(fileURLToPath(import.meta.resolve("@earendil-works/pi-coding-agent"))), "bundle", "cli.js");
const sbxPackage = dirname(fileURLToPath(import.meta.resolve("@christianmoesl/pi-sbx/package.json")));
const routedTools = ["bash", "read", "write", "edit", "grep", "find", "ls"];
// Deadlock watchdogs, not startup performance requirements. SBX gates are released
// by assertions, never by a sleep or a time-to-conversation threshold.
const watchdogMs = 15000;

type RecordValue = Record<string, any>;

async function jsonLines(path: string): Promise<RecordValue[]> {
  try {
    return (await readFile(path, "utf8")).split("\n").filter(Boolean).map(line => JSON.parse(line));
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return [];
    throw error;
  }
}

async function eventually<T>(description: string, inspect: () => Promise<T | undefined>, diagnostics: () => string): Promise<T> {
  const deadline = performance.now() + watchdogMs;
  do {
    const result = await inspect();
    if (result !== undefined) return result;
    await delay(25);
  } while (performance.now() < deadline);
  throw new Error(`${description} timed out: ${diagnostics()}`);
}

function rpc(cwd: string, env: NodeJS.ProcessEnv, guard: string, preload: string) {
  const launchedAt = performance.now();
  const child = spawn(process.execPath, [
    "--require", preload, cli, "--offline", "--mode", "rpc", "--no-session",
    "--no-context-files", "--no-skills", "--no-prompt-templates", "--no-themes",
    "--extension", guard, "--provider", "fixture", "--model", "fixture-model", "--thinking", "off",
  ], { cwd, env, stdio: ["pipe", "pipe", "pipe"] });
  const events: RecordValue[] = [];
  let stderr = "";
  let buffer = "";
  let counter = 0;
  let failure: Error | undefined;
  const pending = new Map<string, { resolve: (value: RecordValue) => void; reject: (error: Error) => void }>();
  const listeners = new Set<() => void>();
  const diagnostics = () => `${failure ?? ""}\n${stderr}\n${JSON.stringify(events.slice(-8))}`;
  const fail = (error: Error) => {
    failure = error;
    for (const request of pending.values()) request.reject(error);
    for (const listener of listeners) listener();
  };
  child.stdin.on("error", fail);
  child.stderr.setEncoding("utf8").on("data", text => { stderr += text; });
  child.stdout.setEncoding("utf8").on("data", text => {
    buffer += text;
    let end: number;
    while ((end = buffer.indexOf("\n")) >= 0) {
      const line = buffer.slice(0, end); buffer = buffer.slice(end + 1);
      if (!line.trim()) continue;
      let event: RecordValue;
      try { event = JSON.parse(line); } catch { fail(Error(`Non-JSON Pi RPC output: ${line}`)); continue; }
      events.push(event);
      if (event.type === "response") pending.get(event.id)?.resolve(event);
      for (const listener of listeners) listener();
    }
  });
  const closed = new Promise<void>(resolve => child.on("close", (code, signal) => {
    fail(Error(`Pi exited (${code ?? signal}): ${stderr}`));
    resolve();
  }));
  child.on("error", fail);

  async function request(type: string, args: RecordValue = {}) {
    if (failure) throw failure;
    const id = String(++counter);
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      const response = await new Promise<RecordValue>((resolve, reject) => {
        pending.set(id, { resolve, reject });
        timer = setTimeout(() => reject(Error(`Pi RPC ${type} timed out: ${diagnostics()}`)), watchdogMs);
        child.stdin.write(`${JSON.stringify({ id, type, ...args })}\n`);
      });
      assert.equal(response.success, true, JSON.stringify(response));
      return response.data;
    } finally {
      clearTimeout(timer);
      pending.delete(id);
    }
  }

  function waitEvent(predicate: (event: RecordValue) => boolean, since = 0): Promise<RecordValue> {
    return new Promise((resolve, reject) => {
      let timer: ReturnType<typeof setTimeout> | undefined;
      const check = () => {
        const found = events.slice(since).find(predicate);
        if (!found && !failure) return;
        clearTimeout(timer);
        listeners.delete(check);
        if (found) resolve(found);
        else reject(failure!);
      };
      listeners.add(check);
      timer = setTimeout(() => {
        listeners.delete(check);
        reject(Error(`Pi event timed out: ${diagnostics()}`));
      }, watchdogMs);
      check();
    });
  }

  async function turn(message: string) {
    const start = events.length;
    const promptedAt = performance.now();
    await request("prompt", { message });
    // A prompt acceptance response alone is NOT a completed conversation.
    await waitEvent(event => event.type === "agent_settled", start);
    const run = events.slice(start);
    assert.ok(run.some(event => event.type === "agent_start"), diagnostics());
    assert.ok(run.some(event => event.type === "message_update" && event.assistantMessageEvent?.type === "text_delta"), diagnostics());
    const assistant = run.filter(event => event.type === "message_end" && event.message?.role === "assistant").at(-1)?.message;
    assert.equal(assistant?.provider, "fixture", diagnostics());
    assert.equal(assistant?.model, "fixture-model", diagnostics());
    assert.equal(assistant?.stopReason, "stop", JSON.stringify(assistant));
    return { events: run, assistant, completedAt: performance.now(), elapsedMs: performance.now() - promptedAt };
  }

  async function stop() {
    if (child.exitCode !== null || child.signalCode !== null) return;
    child.kill("SIGTERM");
    const timer = setTimeout(() => child.kill("SIGKILL"), 2000);
    try { await closed; } finally { clearTimeout(timer); }
  }
  return { request, turn, waitEvent, stop, events, launchedAt, diagnostics };
}

async function fixture(t: TestContext, initiallyPresent = false) {
  // Pi process.cwd() is canonical on macOS (/private/var vs /var).
  const root = await realpath(await mkdtemp(join(tmpdir(), "pi-radar-early-startup-")));
  let session: ReturnType<typeof rpc> | undefined;
  t.after(async () => {
    await session?.stop();
    await rm(root, { recursive: true, force: true });
  });
  const home = join(root, "home");
  const agent = join(home, ".pi", "agent");
  const workspace = join(root, "workspace");
  const shared = join(root, "shared");
  const bin = join(root, "bin");
  for (const directory of [agent, workspace, shared, bin]) await mkdir(directory, { recursive: true });
  const discoveryGate = join(root, "discovery-released");
  const appearanceGate = join(root, "sandbox-appeared");
  const readinessGate = join(root, "readiness-released");
  const sbxLog = join(root, "sbx.jsonl");
  const radarLog = join(root, "radar.jsonl");
  const providerLog = join(root, "provider.jsonl");
  const networkLog = join(root, "forbidden-network.jsonl");
  const snapshot = join(root, "snapshot.json");
  const sandboxName = "fixture-workspace";
  const commands = Object.fromEntries(["absent", "initializing", "ready", "host"].map(phase => [
    `tool:${phase}`, `printf 'host:${phase}' > '${join(root, `host-${phase}`)}'`,
  ]));
  if (initiallyPresent) {
    await writeFile(discoveryGate, "released");
    await writeFile(appearanceGate, "present");
  }

  // This executable replaces ONLY the SBX CLI boundary. The released extension's
  // discovery, polling, tool overrides, connection and transport run unchanged.
  // Never eval the real worker script or execute the requested command on host.
  const sbx = join(bin, "sbx");
  await writeFile(sbx, `#!${process.execPath}
const { appendFileSync, existsSync } = require('node:fs');
const { createInterface } = require('node:readline');
const args = process.argv.slice(2);
const log = value => appendFileSync(${JSON.stringify(sbxLog)}, JSON.stringify({...value, at: Date.now()}) + '\\n');
const emit = value => process.stdout.write(JSON.stringify(value) + '\\n');
if (args[0] === 'ls' && args[1] === '--json' && args.length === 2) {
  log({kind: 'discovery-start'});
  const timer = setInterval(() => {
    if (!existsSync(${JSON.stringify(discoveryGate)})) return;
    clearInterval(timer);
    const present = existsSync(${JSON.stringify(appearanceGate)});
    log({kind: 'discovery-result', present});
    emit({sandboxes: present ? [{name: ${JSON.stringify(sandboxName)}, status: 'running', workspaces: [${JSON.stringify(workspace)}]}] : []});
  }, 10);
} else if (args[0] === 'exec') {
  if (args[1] !== '-i' || args[2] !== '--workdir' || args[3] !== ${JSON.stringify(workspace)} || args[4] !== ${JSON.stringify(sandboxName)} || args[5] !== 'node' || args[6] !== '-e' || !args[7]?.includes('SBX_STARTUP_DIR')) {
    throw Error('Unexpected worker invocation: ' + JSON.stringify(args));
  }
  log({kind: 'worker-start', args});
  emit({type: 'initializing'});
  let ready = false;
  const timer = setInterval(() => {
    if (!existsSync(${JSON.stringify(readinessGate)})) return;
    clearInterval(timer);
    ready = true;
    log({kind: 'worker-ready'});
    emit({type: 'ready'});
  }, 10);
  const input = createInterface({input: process.stdin, crlfDelay: Infinity});
  input.on('line', line => {
    const request = JSON.parse(line);
    log({kind: 'worker-request', request, ready});
    if (!ready) { emit({type: 'error', id: request.id, message: 'Fixture worker not ready'}); return; }
    if (request.type !== 'exec' || request.command[0] !== 'bash' || request.command[1] !== '-lc') {
      emit({type: 'error', id: request.id, message: 'Unsupported fixture request'}); return;
    }
    emit({type: 'stdout', id: request.id, data: Buffer.from('fake sandbox worker output\\n').toString('base64')});
    emit({type: 'result', id: request.id, exitCode: 0});
  });
  input.on('close', () => { clearInterval(timer); });
} else throw Error('Unexpected SBX command: ' + JSON.stringify(args));
`, { mode: 0o755 });

  const radar = join(bin, "radar");
  await writeFile(radar, `#!${process.execPath}
const { appendFileSync, existsSync } = require('node:fs');
const args = process.argv.slice(2);
appendFileSync(${JSON.stringify(radarLog)}, JSON.stringify({args}) + '\\n');
if (args[0] === 'activity') process.exit(0);
if (!args.includes('--json')) throw Error('Radar JSON output must be requested');
if (args[0] !== 'workspace-context' || args[args.indexOf('--workspace') + 1] !== ${JSON.stringify(workspace)}) throw Error('Unexpected Radar call');
console.log(JSON.stringify(args.includes('--registration-only') ? {registered: true} : {
  registered: true, workspace_path: ${JSON.stringify(workspace)}, revision: 'fixture-revision', members: [],
  sandbox: {shared_directory: ${JSON.stringify(shared)}, shared_directory_ready: existsSync(${JSON.stringify(readinessGate)})}
}));
`, { mode: 0o755 });

  // --offline only prohibits startup network operations, not model requests.
  // Fail closed at socket/fetch/HTTP boundaries before Pi loads, even if model
  // selection or the provider implementation regresses to a live provider.
  const preload = join(root, "no-network.cjs");
  await writeFile(preload, `const { appendFileSync } = require('node:fs');
const forbidden = () => {
  appendFileSync(${JSON.stringify(networkLog)}, JSON.stringify({attempt: 'network'}) + '\\n');
  throw Error('Early-startup fixture forbids all network access');
};
globalThis.fetch = forbidden;
require('node:net').Socket.prototype.connect = forbidden;
require('node:http').request = require('node:http').get = forbidden;
require('node:https').request = require('node:https').get = forbidden;
require('node:module').syncBuiltinESMExports();
`);

  // Supported registerProvider/streamSimple API from Pi's custom-provider docs.
  // This is a model, not an input handler pretending a turn completed: actual
  // assistant streaming and tool calls flow through Pi's normal agent loop.
  const probe = join(root, "offline-model.ts");
  await writeFile(probe, `import { appendFileSync, writeFileSync } from 'node:fs';
import { createAssistantMessageEventStream } from '@earendil-works/pi-ai';
export default function(pi) {
  let counter = 0;
  const commands = ${JSON.stringify(commands)};
  pi.registerProvider('fixture', {
    name: 'Offline fixture', baseUrl: 'https://fixture.invalid', apiKey: 'not-a-real-key', api: 'fixture-offline',
    models: [{id: 'fixture-model', name: 'Offline fixture model', reasoning: false, input: ['text'],
      cost: {input: 0, output: 0, cacheRead: 0, cacheWrite: 0}, contextWindow: 128000, maxTokens: 1024}],
    streamSimple(model, context) {
      appendFileSync(${JSON.stringify(providerLog)}, JSON.stringify({systemPrompt: context.systemPrompt, messages: context.messages}) + '\\n');
      const stream = createAssistantMessageEventStream();
      const last = context.messages.at(-1);
      const prompt = typeof last.content === 'string' ? last.content : last.content.filter(c => c.type === 'text').map(c => c.text).join('');
      const command = last.role === 'user' ? commands[prompt] : undefined;
      const output = {role: 'assistant', content: [], api: model.api, provider: model.provider, model: model.id,
        usage: {input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2, cost: {input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0}},
        stopReason: command ? 'toolUse' : 'stop', timestamp: Date.now()};
      stream.push({type: 'start', partial: output});
      if (command) {
        const toolCall = {type: 'toolCall', id: 'fixture-call-' + (++counter), name: 'bash', arguments: {command}};
        output.content.push(toolCall);
        stream.push({type: 'toolcall_start', contentIndex: 0, partial: output});
        stream.push({type: 'toolcall_delta', contentIndex: 0, delta: JSON.stringify(toolCall.arguments), partial: output});
        stream.push({type: 'toolcall_end', contentIndex: 0, toolCall, partial: output});
      } else {
        const block = {type: 'text', text: ''};
        output.content.push(block);
        stream.push({type: 'text_start', contentIndex: 0, partial: output});
        block.text = last.role === 'toolResult' ? 'offline tool result observed' : 'offline reply: ' + prompt;
        stream.push({type: 'text_delta', contentIndex: 0, delta: block.text, partial: output});
        stream.push({type: 'text_end', contentIndex: 0, content: block.text, partial: output});
      }
      stream.push({type: 'done', reason: output.stopReason, message: output});
      stream.end();
      return stream;
    }
  });
  pi.registerCommand('fixture-snapshot', {description: 'Fixture runtime metadata', handler: async (_args, ctx) => {
    writeFileSync(${JSON.stringify(snapshot)}, JSON.stringify({tools: pi.getAllTools(), active: pi.getActiveTools(), commands: pi.getCommands(), sessionId: ctx.sessionManager.getSessionId(), tmpdir: process.env.TMPDIR}));
  }});
}
`);

  const manifest = JSON.parse(await readFile(join(sbxPackage, "package.json"), "utf8"));
  assert.equal(manifest.name, "@christianmoesl/pi-sbx");
  assert.equal(manifest.version, "0.6.0", "must exercise the pinned release, not a fake provider package");
  await writeFile(join(agent, "settings.json"), JSON.stringify({
    defaultProjectTrust: "always", quietStartup: true, enableInstallTelemetry: false,
    packages: [sbxPackage, repository], extensions: [probe],
    compaction: { enabled: false }, retry: { enabled: false },
  }));
  // Do not inherit credentials, proxies, NODE_OPTIONS, SBX integration settings,
  // user configuration or a host PATH containing a real radar/sbx executable.
  const env = {
    PATH: `${bin}:${dirname(process.execPath)}:/usr/bin:/bin`, HOME: home, TMPDIR: root,
    PI_CODING_AGENT_DIR: agent, PI_OFFLINE: "1", PI_TELEMETRY: "0", TERM: "dumb",
    XDG_CONFIG_HOME: join(root, "config"), XDG_DATA_HOME: join(root, "data"), XDG_STATE_HOME: join(root, "state"),
    PI_SBX_EXECUTABLE: sbx, RADAR_BINARY: radar,
  };
  session = rpc(workspace, env, join(repository, "internal/pi/require-sandbox.ts"), preload);
  const client = session;
  const records = () => jsonLines(sbxLog);
  const waitSbx = (kind: string) => eventually(kind, async () => (await records()).find(record => record.kind === kind), client.diagnostics);
  const waitStatus = (text: string) => client.waitEvent(event => event.type === "extension_ui_request" && event.method === "setStatus" && event.statusKey === "pi-sbx" && String(event.statusText).includes(text));
  async function metadata() {
    await client.request("prompt", { message: "/fixture-snapshot" });
    return JSON.parse(await readFile(snapshot, "utf8"));
  }
  async function assertProvenance() {
    const value = await metadata();
    const source = await realpath(join(sbxPackage, manifest.pi.extensions[0]));
    const command = value.commands.filter((command: RecordValue) => command.name === "sbx");
    assert.equal(command.length, 1);
    assert.equal(command[0].source, "extension");
    assert.equal(command[0].sourceInfo.origin, "package");
    assert.equal(command[0].sourceInfo.scope, "user");
    assert.equal(await realpath(command[0].sourceInfo.path), source);
    for (const name of routedTools) {
      const tools = value.tools.filter((tool: RecordValue) => tool.name === name);
      assert.equal(tools.length, 1, name);
      assert.equal(await realpath(tools[0].sourceInfo.path), source, name);
      assert.equal(tools[0].sourceInfo.origin, "package", name);
      assert.ok(value.active.includes(name), name);
    }
    assert.deepEqual(value.tools.filter((tool: RecordValue) => tool.name.startsWith("radar_")).map((tool: RecordValue) => tool.name).sort(), [
      "radar_reconcile_workspace", "radar_repository_refs", "radar_workspace_context",
    ]);
    assert.ok(value.commands.some((command: RecordValue) => command.name === "radar-reload-workspace-resources"));
    return value;
  }
  async function noHostExecution(phase: string) {
    await assert.rejects(readFile(join(root, `host-${phase}`)), { code: "ENOENT" });
  }
  async function assertClean() {
    await assert.rejects(readFile(networkLog), { code: "ENOENT" });
    assert.equal(client.events.some(event => event.type === "extension_error"), false, client.diagnostics());
    assert.doesNotMatch(client.diagnostics(), /launch refused|Error loading extension/);
  }
  return { root, shared, client, metadata, assertProvenance, noHostExecution, assertClean, records, waitSbx, waitStatus, commands,
    providerLog, radarLog, discoveryGate, appearanceGate, readinessGate };
}

function toolResult(turn: Awaited<ReturnType<ReturnType<typeof rpc>["turn"]>>, isError: boolean) {
  const results = turn.events.filter(event => event.type === "tool_execution_end");
  assert.equal(results.length, 1, JSON.stringify(turn.events));
  assert.equal(results[0].toolName, "bash");
  assert.equal(results[0].isError, isError, JSON.stringify(results[0]));
  return results[0].result.content.map((block: RecordValue) => block.text ?? "").join("\n");
}

test("real offline Pi talks before discovery, fails closed while absent/initializing, and adopts SBX without resetting the session", { timeout: 60000 }, async t => {
  const h = await fixture(t);
  await h.waitSbx("discovery-start");
  assert.equal((await h.records()).some(record => record.kind === "discovery-result"), false);
  const first = await h.client.turn("chat:pre-discovery");
  assert.deepEqual(first.assistant.content, [{ type: "text", text: "offline reply: chat:pre-discovery" }]);
  assert.equal((await h.records()).some(record => record.kind === "discovery-result"), false,
    "the first actual model turn must finish while SBX discovery is deliberately held");
  const original = await h.assertProvenance(); // Also proves the real release passes the launch guard.
  assert.equal(original.tmpdir, h.root);

  await writeFile(h.discoveryGate, "released");
  const absentDiscovery = await h.waitSbx("discovery-result");
  assert.equal(absentDiscovery.present, false);
  const absent = await h.client.turn("chat:absent");
  assert.deepEqual(absent.assistant.content, [{ type: "text", text: "offline reply: chat:absent" }]);
  const absentTool = await h.client.turn("tool:absent");
  assert.match(toolResult(absentTool, true), /Sandbox not ready \(waiting\).*not executed/);
  await h.noHostExecution("absent");
  assert.equal((await h.records()).some(record => record.kind === "worker-start"), false);

  // Appearance is observed by the release's normal background polling. No /sbx
  // retry, /reload, new_session, or other synthetic lifecycle trigger is used.
  await writeFile(h.appearanceGate, "present");
  await h.waitSbx("worker-start");
  await h.waitStatus("initializing");
  const pending = await h.client.turn("chat:initializing");
  assert.deepEqual(pending.assistant.content, [{ type: "text", text: "offline reply: chat:initializing" }]);
  const pendingTool = await h.client.turn("tool:initializing");
  assert.match(toolResult(pendingTool, true), /Sandbox not ready \(initializing\).*not executed/);
  await h.noHostExecution("initializing");
  assert.equal((await h.records()).some(record => record.kind === "worker-request"), false,
    "expired tool waits must not enqueue execution for later readiness");
  assert.equal((await h.records()).some(record => record.kind === "worker-ready"), false,
    "tool calls must return without waiting for the held initialization to finish");

  const readinessReleasedAt = performance.now();
  await writeFile(h.readinessGate, "released");
  await h.waitStatus("sbx: fixture-workspace");
  const ready = await h.client.turn("tool:ready");
  assert.equal(toolResult(ready, false), "fake sandbox worker output\n");
  await h.noHostExecution("ready");
  const workerRequests = (await h.records()).filter(record => record.kind === "worker-request");
  assert.equal(workerRequests.length, 1, "unavailable calls must never be replayed");
  assert.equal(workerRequests[0].ready, true);
  assert.deepEqual(workerRequests[0].request.command, ["bash", "-lc", h.commands["tool:ready"]]);
  const afterReady = await h.metadata();
  assert.equal(afterReady.sessionId, original.sessionId);
  assert.equal(afterReady.tmpdir, h.shared);
  const state = await h.client.request("get_state");
  assert.equal(state.sessionId, original.sessionId);
  const messages = (await h.client.request("get_messages")).messages;
  assert.deepEqual(messages.filter((message: RecordValue) => message.role === "user").map((message: RecordValue) => message.content), [
    "chat:pre-discovery", "chat:absent", "tool:absent", "chat:initializing", "tool:initializing", "tool:ready",
  ].map(text => [{ type: "text", text }]));
  const providerCalls = await jsonLines(h.providerLog);
  assert.ok(providerCalls.some(call => /sandboxing enabled, waiting/.test(call.systemPrompt)));
  assert.ok(providerCalls.some(call => /sandboxing enabled, initializing/.test(call.systemPrompt)));
  assert.ok(providerCalls.some(call => /sbx sandbox fixture-workspace/.test(call.systemPrompt)));
  assert.ok(providerCalls.every(call => /Radar workspace instructions:/.test(call.systemPrompt)), "pi-radar must contribute to actual model context");
  assert.ok((await jsonLines(h.radarLog)).some(call => call.args.includes("--registration-only")));

  // /sbx off is the real provider's explicit consent path, not a guard bypass or
  // forged session entry. Only after this command may this disposable marker be
  // created by the normal host bash tool, without approval UI or session reset.
  await h.client.request("prompt", { message: "/sbx off" });
  await h.waitStatus("host (disabled)");
  const host = await h.client.turn("tool:host");
  assert.equal(toolResult(host, false), "(no output)");
  assert.equal(await readFile(join(h.root, "host-host"), "utf8"), "host:host");
  assert.equal((await h.records()).filter(record => record.kind === "worker-request").length, 1);
  assert.equal((await h.client.request("get_state")).sessionId, original.sessionId);
  assert.equal(h.client.events.some(event => event.type === "extension_ui_request" && event.method === "confirm"), false);
  await h.assertClean();
  t.diagnostic(`fixture launch-to-first-completed-conversation: ${(first.completedAt - h.client.launchedAt).toFixed(0)}ms; SBX readiness deliberately withheld for ${(readinessReleasedAt - h.client.launchedAt).toFixed(0)}ms; absent/initializing tool waits: ${absentTool.elapsedMs.toFixed(0)}/${pendingTool.elapsedMs.toFixed(0)}ms (not a performance threshold)`);
});

test("real offline Pi's first conversational turn also completes when SBX is already initializing at startup", { timeout: 30000 }, async t => {
  const h = await fixture(t, true);
  await h.waitStatus("initializing");
  const first = await h.client.turn("chat:startup-initializing");
  assert.deepEqual(first.assistant.content, [{ type: "text", text: "offline reply: chat:startup-initializing" }]);
  await h.assertProvenance();
  const records = await h.records();
  assert.ok(records.some(record => record.kind === "discovery-result" && record.present === true));
  assert.ok(records.some(record => record.kind === "worker-start"));
  assert.equal(records.some(record => record.kind === "worker-ready" || record.kind === "worker-request"), false);
  const calls = await jsonLines(h.providerLog);
  assert.equal(calls.length, 1);
  assert.match(calls[0].systemPrompt, /sandboxing enabled, initializing/);
  assert.match(calls[0].systemPrompt, /shared screenshot directory is not ready/);
  await h.assertClean();
  t.diagnostic(`fixture launch-to-first-completed-conversation during held initialization: ${(first.completedAt - h.client.launchedAt).toFixed(0)}ms; SBX was not released`);
});
