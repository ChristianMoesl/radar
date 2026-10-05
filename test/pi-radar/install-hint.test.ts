import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, type TestContext } from "node:test";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import installHint, { radarConfigured } from "../../internal/pi/install-hint.ts";

async function harness(t: TestContext) {
  const root = await mkdtemp(join(tmpdir(), "radar-install-hint-"));
  const environment = { ...process.env };
  const argv = process.argv;
  t.after(async () => {
    process.argv = argv;
    for (const key of Object.keys(process.env)) if (!(key in environment)) delete process.env[key];
    Object.assign(process.env, environment);
    await rm(root, { recursive: true, force: true });
  });
  const agent = join(root, "custom-agent");
  const cwd = join(root, "workspace");
  await mkdir(agent);
  await mkdir(join(cwd, ".pi"), { recursive: true });
  process.env.PI_CODING_AGENT_DIR = agent;
  process.argv = ["node", "pi"];
  const hooks = new Map<string, (event: any, ctx: any) => any>();
  const commands = new Map<string, any>();
  const tools: { name: string }[] = [];
  const displayed: string[][] = [];
  let widget: string[] | undefined;
  const ctx = {
    cwd, hasUI: true, mode: "tui",
    ui: { setWidget: (_key: string, value: string[] | undefined) => {
      widget = value;
      if (value) displayed.push(value);
    } },
  };
  const pi = {
    on: (name: string, handler: (event: any, ctx: any) => any) => hooks.set(name, handler),
    getAllTools: () => tools,
    registerCommand: (name: string, command: any) => commands.set(name, command),
    registerTool: () => assert.fail("advice must not register model tools"),
    exec: () => assert.fail("advice must not execute or install anything"),
  };
  const load = () => installHint(pi as unknown as ExtensionAPI);
  load();
  return {
    root, agent, cwd, commands, tools, ctx, displayed, load,
    marker: join(agent, "radar", "install-hint-seen"),
    widget: () => widget,
    discover: async () => hooks.get("resources_discover")!({ reason: "startup", cwd }, ctx),
  };
}

test("missing integration gets optional, dismissible advice once per actual Pi profile", async t => {
  const h = await harness(t);
  const original = '{"theme":"dark"}\n';
  await writeFile(join(h.agent, "settings.json"), original);
  await h.discover();
  assert.equal(h.displayed.length, 1);
  assert.match(h.widget()!.join("\n"), /pi install npm:@christianmoesl\/pi-radar/);
  assert.match(h.widget()!.join("\n"), /Optional/);
  assert.ok(h.widget()!.join("\n").includes(`PI_CODING_AGENT_DIR='${h.agent}' pi install`));
  assert.equal(await readFile(h.marker, "utf8"), "shown\n");
  assert.equal((await stat(h.marker)).mode & 0o777, 0o600);
  await h.commands.get("radar-dismiss-install-hint").handler("", h.ctx);
  assert.equal(h.widget(), undefined);
  h.load(); // reload / new process / new session, same profile
  await h.discover();
  assert.equal(h.displayed.length, 1);
  assert.equal(await readFile(join(h.agent, "settings.json"), "utf8"), original);

  const other = join(h.root, "other's profile");
  process.env.PI_CODING_AGENT_DIR = other;
  await h.discover();
  assert.equal(h.displayed.length, 2);
  assert.ok(h.widget()!.join("\n").includes(`PI_CODING_AGENT_DIR='${other.replaceAll("'", "'\"'\"'")}' pi install`));
});

test("active integration is detected even when its tools are not selected", async t => {
  const h = await harness(t);
  h.tools.push({ name: "radar_workspace_context" });
  await h.discover();
  assert.equal(h.displayed.length, 0);
  await assert.rejects(stat(h.marker), { code: "ENOENT" });
});

for (const source of [
  "npm:@christianmoesl/pi-radar", "npm:@christianmoesl/pi-radar@0.1.0",
  "git:github.com/ChristianMoesl/radar", "git:github.com/ChristianMoesl/radar@v0.1.0",
  "https://github.com/ChristianMoesl/radar.git", "git@github.com:ChristianMoesl/radar.git",
]) {
  test(`recognizes configured source ${source}, including deliberately disabled resources`, async t => {
    const h = await harness(t);
    for (const base of [h.agent, join(h.cwd, ".pi")]) {
      await writeFile(join(base, "settings.json"), JSON.stringify({ packages: [{ source, extensions: [] }] }));
      assert.equal(await radarConfigured(h.agent, h.cwd), true);
      await h.discover();
      await rm(join(base, "settings.json"));
    }
    assert.equal(h.displayed.length, 0);
  });
}

test("local package identity and explicitly excluded extension paths suppress advice", async t => {
  const h = await harness(t);
  const local = join(h.agent, "arbitrary-name");
  await mkdir(local);
  await writeFile(join(local, "package.json"), JSON.stringify({ name: "@christianmoesl/pi-radar" }));
  await writeFile(join(h.agent, "settings.json"), JSON.stringify({ packages: [{ source: "./arbitrary-name", extensions: [] }] }));
  await h.discover();
  await writeFile(join(h.agent, "settings.json"), JSON.stringify({ extensions: ["-/src/extensions/pi-radar/index.ts"] }));
  await h.discover();
  assert.equal(h.displayed.length, 0);
});

test("unrelated packages and ordinary extension files do not hide the recommendation", async t => {
  const h = await harness(t);
  const extension = join(h.agent, "unrelated.ts");
  await writeFile(extension, "export default function() {}");
  await writeFile(join(h.agent, "settings.json"), JSON.stringify({
    packages: ["npm:@christianmoesl/pi-radar-other", "https://github.com/elsewhere/radar", "npm:unrelated"],
    extensions: [extension, "builtin:mcp"],
  }));
  await h.discover();
  assert.equal(h.displayed.length, 1);
});

test("explicit extension disabling, RPC and headless sessions remain quiet without consuming the notice", async t => {
  const h = await harness(t);
  for (const flag of ["-ne", "--no-extensions"]) {
    process.argv = ["node", "pi", flag];
    await h.discover();
  }
  process.argv = ["node", "pi"];
  h.ctx.hasUI = false;
  await h.discover();
  h.ctx.hasUI = true;
  h.ctx.mode = "rpc";
  await h.discover();
  assert.equal(h.displayed.length, 0);
  await assert.rejects(stat(h.marker), { code: "ENOENT" });
  h.ctx.mode = "tui";
  await h.discover();
  assert.equal(h.displayed.length, 1);
});

test("invalid settings and unavailable notice storage never interrupt startup", async t => {
  const h = await harness(t);
  const settings = join(h.agent, "settings.json");
  await writeFile(settings, "broken json");
  await h.discover();
  await rm(settings);
  await writeFile(join(h.agent, "radar"), "not a directory");
  await h.discover();
  assert.equal(h.displayed.length, 0);
});

test("concurrent launches claim only one notice", async t => {
  const h = await harness(t);
  await Promise.all([h.discover(), h.discover(), h.discover()]);
  assert.equal(h.displayed.length, 1);
});
