import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, type TestContext } from "node:test";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { registerOnboarding } from "../../extensions/pi-radar/onboarding.ts";

async function harness(t: TestContext) {
  const root = await mkdtemp(join(tmpdir(), "radar-welcome-"));
  const env = { ...process.env };
  process.env.PI_CODING_AGENT_DIR = root;
  process.env.TMUX = "fixture";
  process.env.TMUX_PANE = "%1";
  t.after(async () => {
    for (const key of Object.keys(process.env)) if (!(key in env)) delete process.env[key];
    Object.assign(process.env, env);
    await rm(root, { recursive: true, force: true });
  });
  const hooks = new Map<string, Function>();
  const commands = new Map<string, any>();
  const notices: string[] = [];
  let prompts = 0;
  const state = { choice: "Yes" as string | undefined, prefix: "C-b", binding: "bind-key -T prefix r display-popup -E radar", fail: false };
  const pi = {
    on: (event: string, handler: Function) => hooks.set(event, handler),
    registerCommand: (name: string, command: any) => commands.set(name, command),
    exec: async (name: string, args: string[]) => {
      assert.equal(name, "tmux");
      if (state.fail) throw new Error("missing tmux");
      return { code: 0, stdout: args[0] === "show-options" ? state.prefix : state.binding };
    },
  } as unknown as ExtensionAPI;
  const ctx = { hasUI: true, mode: "tui", isIdle: () => true, waitForIdle: async () => {}, ui: {
    select: async () => { prompts++; return state.choice; },
    notify: (message: string) => notices.push(message),
  } };
  const load = () => registerOnboarding(pi);
  load();
  return { root, marker: join(root, "radar", "onboarding-seen"), state, ctx, notices, load, prompts: () => prompts,
    start: () => hooks.get("resources_discover")!({}, ctx),
    manual: () => commands.get("radar-onboarding").handler("", ctx),
  };
}

test("first welcome is concise, then shared across new workspaces and reloads", async t => {
  const h = await harness(t);
  await h.start();
  assert.equal(h.prompts(), 1);
  assert.match(h.notices[0], /Ctrl\+B, release, then R/);
  assert.match(h.notices[0], /find tasks, create workspaces/);
  assert.equal(h.notices[0].split("\n").length, 3);
  assert.equal(await readFile(h.marker, "utf8"), "completed\n");
  assert.equal((await stat(h.marker)).mode & 0o777, 0o600);
  h.load();
  await h.start();
  assert.equal(h.prompts(), 1);
  await h.manual();
  assert.equal(h.notices.length, 2);
});

test("No permanently dismisses automatic offers, manual introduction still works", async t => {
  const h = await harness(t);
  h.state.choice = "No — don't ask again";
  await h.start();
  assert.equal(h.notices.length, 0);
  assert.equal(await readFile(h.marker, "utf8"), "dismissed\n");
  h.load(); await h.start();
  assert.equal(h.prompts(), 1);
  await h.manual();
  assert.equal(h.notices.length, 1);
});

test("Escape does not consume the automatic offer", async t => {
  const h = await harness(t);
  h.state.choice = undefined;
  await h.start();
  await assert.rejects(stat(h.marker), { code: "ENOENT" });
  h.load(); h.state.choice = "Yes"; await h.start();
  assert.equal(h.prompts(), 2);
});

test("concurrent instances only offer once and headless/busy sessions stay quiet", async t => {
  const h = await harness(t);
  h.ctx.mode = "rpc";
  await h.start();
  h.ctx.mode = "tui";
  h.ctx.isIdle = () => false;
  await h.start();
  assert.equal(h.prompts(), 0);
  h.ctx.isIdle = () => true;
  const first = h.start();
  h.load();
  await Promise.all([first, h.start()]);
  assert.equal(h.prompts(), 1);
});

test("uses existing custom prefix and does not invent a missing popup", async t => {
  const h = await harness(t);
  h.state.prefix = "C-a";
  await h.manual();
  assert.match(h.notices.at(-1)!, /Ctrl\+A/);
  h.state.prefix = "C-Space";
  await h.manual();
  assert.match(h.notices.at(-1)!, /Ctrl\+Space/);
  h.state.prefix = "none";
  await h.manual();
  assert.match(h.notices.at(-1)!, /another terminal/);
  h.state.prefix = "C-a";
  h.state.binding = "bind-key -T prefix r source-file ~/.tmux.conf";
  await h.manual();
  assert.match(h.notices.at(-1)!, /another terminal/);
  h.state.fail = true;
  await h.manual();
  assert.match(h.notices.at(-1)!, /another terminal/);
});

test("unavailable state does not block startup; a different Pi profile gets its own offer", async t => {
  const h = await harness(t);
  await writeFile(join(h.root, "radar"), "not a directory");
  await h.start();
  assert.equal(h.prompts(), 0);
  const other = join(h.root, "other");
  await mkdir(other);
  process.env.PI_CODING_AGENT_DIR = other;
  h.load(); await h.start();
  assert.equal(h.prompts(), 1);
});
