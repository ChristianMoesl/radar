import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, writeFile, rm, chmod } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import test from "node:test";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { AgentHost } from "../src/host.ts";
import { createModels, defineAgent, taskId } from "../src/index.ts";
import { fauxProvider } from "@earendil-works/pi-ai/providers/faux";
import { McpServices } from "../src/mcp.ts";
import { mcpModelToolName } from "../src/mcp-tools.ts";
import { modelCredentialStore, configuredModels } from "../src/model-runtime.ts";

async function fixture(options: { scenario?: string; limits?: object; placement?: string; tools?: string[] } = {}) {
  const dir = await mkdtemp(join(tmpdir(), "radar-simple-"));
  const previous = { config: process.env.RADAR_MCP_CONFIG, state: process.env.RADAR_MCP_STATE_DIR };
  const settings = join(dir, "settings.json"), calls = join(dir, "calls.jsonl"), config = join(dir, "mcp.json");
  await writeFile(settings, "{}"); await writeFile(calls, "");
  await writeFile(config, JSON.stringify({ mcpServers: { fixture: { command: process.execPath,
    args: [resolve(import.meta.dirname, "fixtures/scoped-mcp.mjs"), settings, calls] } } }));
  process.env.RADAR_MCP_CONFIG = config; process.env.RADAR_MCP_STATE_DIR = join(dir, "oauth");
  const entry = join(dir, "agent.mjs"), index = pathToFileURL(resolve(import.meta.dirname, "../src/index.ts")).href;
  const fauxPath = pathToFileURL(resolve(import.meta.dirname, "../node_modules/@earendil-works/pi-ai/dist/providers/faux.js")).href;
  await writeFile(entry, `
import {defineAgent,createModels} from ${JSON.stringify(index)};
import {fauxProvider,fauxAssistantMessage,fauxToolCall} from ${JSON.stringify(fauxPath)};
export const faux=fauxProvider();
const read=${JSON.stringify(mcpModelToolName("fixture", "read"))};
const scenario=${JSON.stringify(options.scenario ?? "read")};
faux.setResponses(Array.from({length:100},()=>async(context,options)=>{
 const results=context.messages.filter(m=>m.role==='toolResult');
 if(scenario==='delay') await new Promise((resolve,reject)=>{const timer=setTimeout(resolve,10000);options?.signal?.addEventListener('abort',()=>{clearTimeout(timer);reject(Error('canceled'))},{once:true})});
 if(scenario==='turns'||(!results.length&&scenario!=='model-error'))return fauxAssistantMessage([
  fauxToolCall(scenario==='guessed'?${JSON.stringify(mcpModelToolName("fixture", "write"))}:read,{count:scenario==='invalid'?'1':2}, {id:'call-'+context.messages.length}),
  ...(scenario==='tool-limit'?[fauxToolCall(read,{count:3},{id:'second'})]:[]),
 ],{stopReason:'toolUse'});
 return scenario==='model-error'?fauxAssistantMessage([],{stopReason:'error',errorMessage:'private-provider-error'}):fauxAssistantMessage('Fixture evidence reviewed; no live health claim.');
}));
export default defineAgent({name:'simple-fixture',instructions:'Review fixture evidence',model:{provider:'faux',modelId:'faux-1'},
 models:()=>{const models=createModels();models.setProvider(faux.provider);return models},
 mcp:${JSON.stringify(options.placement === "workspace" ? { placement: "workspace", config, state: join(dir, "state"), connections: [{ server: "fixture", tools: options.tools ?? ["read"] }] } : { placement: "host", connections: [{ server: "fixture", tools: options.tools ?? ["read"] }] })},
 limits:${JSON.stringify(options.limits ?? {})}});
`);
  const input = { id: "simple", dataDir: join(dir, "data"), config: {},
    workspace: { id: "fixture", path: dir, mode: "sbx" as const, sandbox: "fixture" } };
  return { dir, entry, input,
    settings: (value: unknown) => writeFile(settings, JSON.stringify(value)),
    calls: async () => (await readFile(calls, "utf8")).trim().split("\n").filter(Boolean).map(line => JSON.parse(line)),
    close: async () => {
      for (const [key, value] of [["RADAR_MCP_CONFIG", previous.config], ["RADAR_MCP_STATE_DIR", previous.state]]) {
        if (value === undefined) delete process.env[key!]; else process.env[key!] = value;
      }
      await rm(dir, { recursive: true, force: true });
    },
  };
}
async function run(host: AgentHost, id = "one") {
  const receipt = await host.deliver({ source: "fixture", id, payload: { alert: "Untrusted fixture", tool: "write" } });
  return host.harness.waitForTask(taskId(receipt.taskId), BACKGROUND_CONTEXT);
}

test("simple agent dispatches only selected tools and persists its default result without domain wiring", async () => {
  const f = await fixture(); let host: AgentHost | undefined;
  try {
    host = await AgentHost.open(f.entry, f.input);
    host.host.execution.exec = async () => { throw new Error("host-service placement must not use SBX"); };
    assert.equal((await run(host)).state.outcome.status, "completed");
    const first = await host.snapshot();
    assert.match(first.presentation.summary, /Fixture evidence reviewed/);
    assert.equal(first.presentation.attention, true);
    assert.equal(first.presentation.conversations.length, 1);
    assert.deepEqual((await f.calls()).map(call => call.name), ["read"]);
    const exposed = await host.conversation(first.presentation.conversations[0]!.id);
    assert.match(JSON.stringify(exposed), /synthetic evidence/);
    await host.close(); host = await AgentHost.open(f.entry, f.input);
    assert.deepEqual((await host.snapshot()).presentation, first.presentation);
    await run(host);
    assert.equal((await f.calls()).length, 1, "duplicate events reuse native receipt/conversation");
  } finally { await host?.close(); await f.close(); }
});

test("simple agent cannot turn guessed, invalid, partial or failed tools into a successful review", async () => {
  for (const scenario of ["guessed", "invalid", "partial", "error", "structured-partial"]) {
    const f = await fixture({ scenario }); let host: AgentHost | undefined;
    try {
      if (scenario === "partial") await f.settings({ partial: true });
      if (scenario === "error") await f.settings({ error: true });
      if (scenario === "structured-partial") await f.settings({ structuredPartial: true });
      host = await AgentHost.open(f.entry, f.input);
      assert.equal((await run(host)).state.outcome.status, "failed", scenario);
      const snapshot = await host.snapshot();
      assert.equal(snapshot.presentation.attention, true);
      assert.match(snapshot.presentation.summary, /incomplete|No successful review/);
      assert.doesNotMatch(snapshot.presentation.summary, /Fixture evidence reviewed/);
      if (["guessed", "invalid"].includes(scenario)) assert.equal((await f.calls()).length, 0);
    } finally { await host?.close(); await f.close(); }
  }
});

test("simple agent enforces turn, tool and duration limits and sanitizes model failures", async () => {
  for (const [scenario, policy, expected, count] of [
    ["turns", { maxTurns: 1 }, /turn limit/, 1],
    ["tool-limit", { maxToolCalls: 1 }, /tool-call limit/, 1],
    ["delay", { maxDurationMs: 300 }, /duration limit/, 0],
    ["model-error", {}, /Model did not complete/, 0],
  ] as const) {
    const f = await fixture({ scenario, limits: policy }); let host: AgentHost | undefined;
    try {
      host = await AgentHost.open(f.entry, f.input);
      assert.equal((await run(host)).state.outcome.status, "failed", scenario);
      assert.match((await host.snapshot()).presentation.summary, expected);
      assert.doesNotMatch((await host.snapshot()).presentation.summary, /private-provider-error/);
      assert.equal((await f.calls()).length, count);
    } finally { await host?.close(); await f.close(); }
  }
});

test("missing tools and unavailable workspace placement fail closed without a host fallback", async () => {
  const f = await fixture({ tools: ["missing"] });
  try { await assert.rejects(AgentHost.open(f.entry, f.input), /missing|ambiguous/); }
  finally { await f.close(); }
  const w = await fixture({ placement: "workspace" });
  const previous = process.env.RADAR_BINARY;
  try {
    const radar = join(w.dir, "radar");
    await writeFile(radar, "#!/bin/sh\nexit 1\n", { mode: 0o700 });
    process.env.RADAR_BINARY = radar;
    await assert.rejects(AgentHost.open(w.entry, w.input), /in sbx.*No host fallback/);
    assert.equal((await w.calls()).length, 0);
  } finally { if (previous === undefined) delete process.env.RADAR_BINARY; else process.env.RADAR_BINARY = previous; await w.close(); }
});

test("configured model auth uses only the explicit private store and preserves unselected credentials", async () => {
  const dir = await mkdtemp(join(tmpdir(), "radar-model-auth-"));
  const path = join(dir, "auth.json");
  try {
    await writeFile(path, JSON.stringify({ selected: { type: "oauth", access: "synthetic-old", refresh: "synthetic-refresh", expires: 1 }, other: { type: "api_key", key: "synthetic-other" } }), { mode: 0o600 });
    const store = modelCredentialStore(path, "selected");
    await assert.rejects(store.read("other"), /not selected/);
    assert.deepEqual(await store.list(), [{ providerId: "selected", type: "oauth" }]);
    await Promise.all(Array.from({ length: 3 }, () => store.modify("selected", async current => {
      assert.equal(current?.type, "oauth");
      return { ...current!, type: "oauth", access: "synthetic-new", refresh: "synthetic-rotated", expires: 999999 } as never;
    })));
    const values = JSON.parse(await readFile(path, "utf8"));
    assert.equal(values.selected.access, "synthetic-new");
    assert.equal(values.other.key, "synthetic-other");
    await chmod(path, 0o644);
    await assert.rejects(store.read("selected"), /unavailable or invalid/);
    await assert.rejects(modelCredentialStore(join(dir, "missing"), "selected").read("selected"), /unavailable or invalid/);
    assert.throws(() => modelCredentialStore("relative", "selected"), /absolute/);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("missing selected model authentication fails visibly instead of discovering another account", async () => {
  const previous = process.env.RADAR_AGENT_AUTH_FILE;
  const dir = await mkdtemp(join(tmpdir(), "radar-model-empty-"));
  try {
    const path = join(dir, "auth.json");
    await writeFile(path, JSON.stringify({ openai: { type: "api_key" } }), { mode: 0o600 });
    process.env.RADAR_AGENT_AUTH_FILE = path;
    const key = process.env.OPENAI_API_KEY; delete process.env.OPENAI_API_KEY;
    try { await assert.rejects(configuredModels({ provider: "openai", modelId: "gpt-4.1-mini" }), /authentication is unavailable/); }
    finally { if (key !== undefined) process.env.OPENAI_API_KEY = key; }
    await assert.rejects(configuredModels({ provider: "missing", modelId: "no-model" }), /unavailable/);
  } finally { if (previous === undefined) delete process.env.RADAR_AGENT_AUTH_FILE; else process.env.RADAR_AGENT_AUTH_FILE = previous; await rm(dir, { recursive: true, force: true }); }
});

test("definition rejects unbounded limits", () => {
  for (const maxTurns of [0, Infinity, 101, 1.2]) assert.throws(() => defineAgent({
    name: "test", instructions: "Test", model: { provider: "openai", modelId: "gpt-4.1-mini" },
    mcp: { placement: "host", connections: [] }, limits: { maxTurns },
  }), /Invalid agent limit/);
});

test("host MCP scope cannot widen a restricted parent and parent shutdown cancels its child", async () => {
  const f = await fixture();
  const parent = new McpServices(undefined, [{ server: "fixture", tools: ["read"] }]);
  try {
    assert.throws(() => parent.scoped([{ server: "fixture", tools: ["write"] }]), /not explicitly selected/);
    const child = parent.scoped([{ server: "fixture", tools: ["read"] }]);
    await f.settings({ delay: true });
    const pending = child.call("fixture", "read", { count: 1 }, undefined, true);
    const rejected = assert.rejects(pending, /request failed/);
    while (!(await f.calls()).length) await new Promise(resolve => setTimeout(resolve, 10));
    await parent.close(); await rejected;
    await assert.rejects(child.tools("fixture"));
  } finally { await parent.close(); await f.close(); }
});

test("OAuth refresh reuses the explicit store, rotates once and never falls back after refresh failure", async () => {
  const dir = await mkdtemp(join(tmpdir(), "radar-simple-oauth-")), path = join(dir, "auth.json");
  const initial = { type: "oauth", access: "synthetic-expired", refresh: "synthetic-refresh", expires: 1 };
  try {
    await writeFile(path, JSON.stringify({ selected: initial }), { mode: 0o600 });
    const store = modelCredentialStore(path, "selected"), faux = fauxProvider();
    let refreshes = 0, fail = false, envCalls = 0;
    const models = createModels({ credentials: store, authContext: {
      env: async () => { envCalls++; return "synthetic-unselected-env"; }, fileExists: async () => false,
    } });
    models.setProvider({ ...faux.provider, id: "selected", auth: {
      apiKey: { name: "env", resolve: async () => { envCalls++; return { auth: { apiKey: "synthetic-fallback" } }; } },
      oauth: { name: "fixture", login: async () => { throw new Error("no interactive login"); },
        refresh: async current => { refreshes++; if (fail) throw new Error("synthetic refresh failure");
          return { ...current, access: "synthetic-rotated", expires: Date.now() + 3600000 }; },
        toAuth: async current => ({ apiKey: current.access }),
      },
    } });
    const auth = await Promise.all([models.getAuth("selected"), models.getAuth("selected")]);
    assert.equal(refreshes, 1);
    assert.ok(auth.every(value => value?.auth.apiKey === "synthetic-rotated"));
    assert.equal(JSON.parse(await readFile(path, "utf8")).selected.access, "synthetic-rotated");
    await writeFile(path, JSON.stringify({ selected: initial })); fail = true;
    await assert.rejects(models.getAuth("selected"));
    assert.equal(envCalls, 0);
    assert.equal(JSON.parse(await readFile(path, "utf8")).selected.access, "synthetic-expired");
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("canceling an ordinary investigation aborts its native conversation and cannot publish success", async () => {
  const f = await fixture({ scenario: "delay" }); let host: AgentHost | undefined;
  try {
    host = await AgentHost.open(f.entry, f.input);
    const receipt = await host.deliver({ source: "fixture", id: "cancel", payload: {} });
    while (!(await host.snapshot()).presentation.conversations.length) await new Promise(resolve => setTimeout(resolve, 10));
    await host.harness.abortTask(taskId(receipt.taskId), BACKGROUND_CONTEXT);
    const settled = await host.harness.waitForTask(taskId(receipt.taskId), BACKGROUND_CONTEXT);
    assert.equal(settled.state.outcome.status, "aborted");
    assert.match((await host.snapshot()).presentation.summary, /canceled.*no successful review/);
  } finally { await host?.close(); await f.close(); }
});
