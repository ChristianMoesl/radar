import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, type TestContext } from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { showWorkspaceAttached } from "../../extensions/pi-radar/workspace-notice.ts";

async function harness(t: TestContext) {
  const root = await mkdtemp(join(tmpdir(), "radar-notice-"));
  const env = { ...process.env };
  process.env.PI_CODING_AGENT_DIR = root;
  process.env.TMUX = "fixture";
  process.env.TMUX_PANE = "%1";
  t.after(async () => {
    for (const key of Object.keys(process.env)) if (!(key in env)) delete process.env[key];
    Object.assign(process.env, env);
    await rm(root, { recursive: true, force: true });
  });
  const notices: string[] = [];
  const calls: string[][] = [];
  const state = { prefix: "C-b", binding: "bind-key -T prefix r display-popup -E radar", fail: false, prefixCode: 0, bindingCode: 0 };
  const pi = {
    exec: async (name: string, args: string[], options: { timeout: number }) => {
      assert.equal(name, "tmux");
      assert.equal(options.timeout, 2000);
      calls.push(args);
      if (state.fail) throw new Error("missing tmux");
      const prefix = args[0] === "show-options";
      return { code: prefix ? state.prefixCode : state.bindingCode, stdout: prefix ? state.prefix : state.binding };
    },
  } as unknown as ExtensionAPI;
  const ctx = { hasUI: true, mode: "tui", ui: {
    notify: (message: string, level: string) => {
      assert.equal(level, "info");
      notices.push(message);
    },
  } };
  return { root, state, ctx, notices, calls,
    show: () => showWorkspaceAttached(pi, ctx as unknown as ExtensionContext),
  };
}

test("attachment is a single line using the active pane's real tmux prefix", async t => {
  const h = await harness(t);
  await h.show();
  assert.deepEqual(h.notices, ["Radar workspace attached. Press Ctrl+B, then r to open the Radar task dashboard."]);
  assert.deepEqual(h.calls, [
    ["show-options", "-v", "-t", "%1", "prefix"],
    ["list-keys", "-T", "prefix", "r"],
  ]);
  assert.deepEqual(await readdir(h.root), []);
});

test("each new session gets the notice without reading or changing old dismissal state", async t => {
  const h = await harness(t);
  const marker = join(h.root, "radar", "onboarding-seen");
  await mkdir(join(h.root, "radar"));
  await writeFile(marker, "dismissed\n");
  await h.show();
  await h.show();
  assert.equal(h.notices.length, 2);
  assert.equal(await readFile(marker, "utf8"), "dismissed\n");
  assert.deepEqual(await readdir(join(h.root, "radar")), ["onboarding-seen"]);
});

test("custom prefixes are rendered without assuming Ctrl+B", async t => {
  const h = await harness(t);
  for (const [prefix, displayed] of [["C-a", "Ctrl+A"], ["C-Space", "Ctrl+Space"], ["M-b", "Alt+B"], ["C-M-b", "Ctrl+Alt+B"], ["F12", "F12"], ["C-]", "Ctrl+]"]]) {
    h.state.prefix = prefix;
    await h.show();
    assert.equal(h.notices.at(-1), `Radar workspace attached. Press ${displayed}, then r to open the Radar task dashboard.`);
  }
  delete process.env.TMUX_PANE;
  await h.show();
  assert.deepEqual(h.calls.at(-2), ["show-options", "-v", "prefix"]);
});

test("missing or unusable tmux shortcuts never produce invented instructions", async t => {
  const h = await harness(t);
  const fallback = "Radar workspace attached. Run `radar` in another terminal to open the task dashboard.";
  for (const prefix of ["none", "", "C-b\nC-a", "\x1b[31mC-b"]) {
    h.state.prefix = prefix;
    await h.show();
    assert.equal(h.notices.at(-1), fallback);
  }
  h.state.prefix = "C-a";
  h.state.binding = "bind-key -T prefix r source-file ~/.tmux.conf";
  await h.show();
  assert.equal(h.notices.at(-1), fallback);
  h.state.binding = "bind-key -T prefix r display-popup -E radar";
  h.state.bindingCode = 1;
  await h.show();
  assert.equal(h.notices.at(-1), fallback);
  h.state.bindingCode = 0;
  h.state.prefixCode = 1;
  await h.show();
  assert.equal(h.notices.at(-1), fallback);
  h.state.fail = true;
  await h.show();
  assert.equal(h.notices.at(-1), fallback);
  delete process.env.TMUX;
  h.calls.length = 0;
  await h.show();
  assert.equal(h.notices.at(-1), fallback);
  assert.deepEqual(h.calls, []);
});

test("noninteractive sessions stay quiet and do not query tmux", async t => {
  const h = await harness(t);
  for (const mode of ["rpc", "print", "json"]) {
    h.ctx.mode = mode;
    await h.show();
  }
  h.ctx.mode = "tui";
  h.ctx.hasUI = false;
  await h.show();
  assert.deepEqual(h.notices, []);
  assert.deepEqual(h.calls, []);
  assert.deepEqual(await readdir(h.root), []);
});
