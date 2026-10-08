import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, type TestContext } from "node:test";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import installHint, { radarConfigured, sbxConfigured } from "../../internal/pi/install-hint.ts";

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
    shutdown: () => assert.fail("advice must not enforce package requirements"),
  };
  const workspace = { registered: true, capabilities: { sandbox: true }, sandbox: {} as object | null };
  let inspectionFails = false;
  const pi = {
    on: (name: string, handler: (event: any, ctx: any) => any) => hooks.set(name, handler),
    getAllTools: () => tools,
    registerCommand: (name: string, command: any) => commands.set(name, command),
    registerTool: () => assert.fail("advice must not register model tools"),
    exec: async (name: string, args: string[]) => {
      if (name === "sbx") {
        assert.deepEqual(args, ["--help"]);
        if (inspectionFails) throw new Error("SBX CLI is unavailable");
        return { code: 0, stdout: "SBX help" };
      }
      assert.equal(name, process.env.RADAR_BINARY?.trim() || "radar");
      assert.deepEqual(args, ["workspace-context", "--workspace", cwd, "--json"]);
      return { code: 0, stdout: JSON.stringify(workspace) };
    },
  };
  const load = () => installHint(pi as unknown as ExtensionAPI);
  load();
  return {
    root, agent, cwd, commands, tools, ctx, displayed, load, workspace, failInspection: () => { inspectionFails = true; },
    marker: join(agent, "radar", "install-hint-seen"),
    widget: () => widget,
    text: () => widget!.join("\n"),
    discover: async () => hooks.get("resources_discover")!({ reason: "startup", cwd }, ctx),
  };
}

test("missing packages get combined, dismissible advice once per actual Pi profile", async t => {
  const h = await harness(t);
  const original = '{"theme":"dark"}\n';
  await writeFile(join(h.agent, "settings.json"), original);
  await h.discover();
  assert.equal(h.displayed.length, 1);
  assert.match(h.text(), /pi install npm:@christianmoesl\/pi-radar/);
  assert.match(h.text(), /pi install npm:@christianmoesl\/pi-sbx/);
  assert.match(h.text(), />=0\.6\.0 is required for early sandboxed launch/);
  assert.match(h.text(), /Advice only; no automatic installs or Pi settings changes/);
  for (const name of ["pi-radar", "pi-sbx"]) {
    assert.ok(h.text().includes(`PI_CODING_AGENT_DIR='${h.agent}' pi install npm:@christianmoesl/${name}`));
  }
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
  for (const name of ["pi-radar", "pi-sbx"]) {
    assert.ok(h.text().includes(`PI_CODING_AGENT_DIR='${other.replaceAll("'", "'\"'\"'")}' pi install npm:@christianmoesl/${name}`));
  }
});

test("active pi-radar does not suppress missing pi-sbx advice even when its tools are not selected", async t => {
  const h = await harness(t);
  h.tools.push({ name: "radar_workspace_context" });
  await h.discover();
  assert.equal(h.displayed.length, 1);
  assert.match(h.text(), /pi install npm:@christianmoesl\/pi-sbx/);
  assert.doesNotMatch(h.text(), /pi-radar/);
});

test("both packages configured stay quiet without consuming the notice or enforcing versions", async t => {
  const h = await harness(t);
  const settings = JSON.stringify({ packages: [
    { source: "npm:@christianmoesl/pi-radar", extensions: [] },
    { source: "npm:@christianmoesl/pi-sbx@0.5.0", extensions: [] },
  ] });
  await writeFile(join(h.agent, "settings.json"), settings);
  await h.discover();
  assert.equal(h.displayed.length, 0);
  await assert.rejects(stat(h.marker), { code: "ENOENT" });
  assert.equal(await readFile(join(h.agent, "settings.json"), "utf8"), settings);
});

for (const { name, repository, configured, other } of [
  { name: "pi-radar", repository: "radar", configured: radarConfigured, other: "pi-sbx" },
  { name: "pi-sbx", repository: "pi-sbx", configured: sbxConfigured, other: "pi-radar" },
]) {
  for (const source of [
    `npm:@christianmoesl/${name}`, `npm:@christianmoesl/${name}@0.1.0`,
    `git:github.com/ChristianMoesl/${repository}`, `git:github.com/ChristianMoesl/${repository}@v0.1.0`,
    `https://github.com/ChristianMoesl/${repository}.git`, `git:git@github.com:ChristianMoesl/${repository}.git`,
    `ssh://git@github.com/ChristianMoesl/${repository}.git`,
  ]) {
    test(`recognizes configured ${source}, including deliberately disabled resources, independently`, async t => {
      const h = await harness(t);
      for (const base of [h.agent, join(h.cwd, ".pi")]) {
        const settings = JSON.stringify({ packages: [{ source, extensions: [] }] });
        await writeFile(join(base, "settings.json"), settings);
        assert.equal(await configured(h.agent, h.cwd), true);
        await h.discover();
        assert.doesNotMatch(h.text(), new RegExp(`pi install npm:@christianmoesl/${name}`));
        assert.ok(h.text().includes(`pi install npm:@christianmoesl/${other}`));
        assert.equal(await readFile(join(base, "settings.json"), "utf8"), settings);
        await rm(join(base, "settings.json"));
        await rm(h.marker);
      }
      assert.equal(h.displayed.length, 2);
    });
  }

  test(`${name} local package identity and explicitly excluded extension paths suppress only its advice`, async t => {
    const h = await harness(t);
    for (const base of [h.agent, join(h.cwd, ".pi")]) {
      const local = join(base, "arbitrary-name");
      await mkdir(local);
      await writeFile(join(local, "package.json"), JSON.stringify({ name: `@christianmoesl/${name}` }));
      for (const declaration of [
        { packages: [{ source: "./arbitrary-name", extensions: [] }] },
        { extensions: [`-/src/extensions/${name}/index.ts`] },
        { extensions: [`!./extensions/${name}/index.js`] },
      ]) {
        await writeFile(join(base, "settings.json"), JSON.stringify(declaration));
        await h.discover();
        assert.doesNotMatch(h.text(), new RegExp(`pi install npm:@christianmoesl/${name}`));
        assert.ok(h.text().includes(`pi install npm:@christianmoesl/${other}`));
        await rm(h.marker);
      }
      await rm(join(base, "settings.json"));
    }
  });
}

test("user pi-radar and project-disabled pi-sbx declarations suppress both recommendations", async t => {
  const h = await harness(t);
  await writeFile(join(h.agent, "settings.json"), JSON.stringify({ packages: ["npm:@christianmoesl/pi-radar"] }));
  await writeFile(join(h.cwd, ".pi", "settings.json"), JSON.stringify({ packages: [
    { source: "npm:@christianmoesl/pi-sbx", autoload: false, extensions: [] },
  ] }));
  await h.discover();
  assert.equal(h.displayed.length, 0);
  await assert.rejects(stat(h.marker), { code: "ENOENT" });
});

test("an existing per-profile notice marker is preserved, not reset for pi-sbx advice", async t => {
  const h = await harness(t);
  await mkdir(join(h.agent, "radar"));
  await writeFile(h.marker, "shown\n");
  h.tools.push({ name: "radar_workspace_context" });
  await h.discover();
  assert.equal(h.displayed.length, 0);
  assert.equal(await readFile(h.marker, "utf8"), "shown\n");
});

test("unrelated packages and ordinary extension files do not hide either recommendation", async t => {
  const h = await harness(t);
  const extension = join(h.agent, "unrelated.ts");
  await writeFile(extension, "export default function() {}");
  await writeFile(join(h.agent, "settings.json"), JSON.stringify({
    packages: [
      "npm:@christianmoesl/pi-radar-other", "https://github.com/elsewhere/radar",
      "npm:@christianmoesl/pi-sbx-other", "https://github.com/elsewhere/pi-sbx", "npm:unrelated",
    ],
    extensions: [extension, "builtin:mcp"],
  }));
  await h.discover();
  assert.equal(h.displayed.length, 1);
  assert.match(h.text(), /pi install npm:@christianmoesl\/pi-radar/);
  assert.match(h.text(), /pi install npm:@christianmoesl\/pi-sbx/);
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
  for (const mode of ["rpc", "print", "json"]) {
    h.ctx.mode = mode;
    await h.discover();
  }
  assert.equal(h.displayed.length, 0);
  await assert.rejects(stat(h.marker), { code: "ENOENT" });
  h.ctx.mode = "tui";
  process.argv = ["node", "pi", "--", "--no-extensions"];
  await h.discover();
  assert.equal(h.displayed.length, 1);
});

test("invalid settings and unavailable notice storage never interrupt startup", async t => {
  const h = await harness(t);
  for (const base of [h.agent, join(h.cwd, ".pi")]) {
    const settings = join(base, "settings.json");
    await writeFile(settings, "broken json");
    await h.discover();
    await rm(settings);
  }
  await writeFile(join(h.agent, "radar"), "not a directory");
  await h.discover();
  assert.equal(h.displayed.length, 0);
});

test("concurrent launches claim only one combined notice", async t => {
  const h = await harness(t);
  await Promise.all([h.discover(), h.discover(), h.discover()]);
  assert.equal(h.displayed.length, 1);
  assert.match(h.text(), /pi install npm:@christianmoesl\/pi-radar/);
  assert.match(h.text(), /pi install npm:@christianmoesl\/pi-sbx/);
});

for (const condition of ["disabled", "absent CLI", "non-sandbox workspace", "unregistered"]) {
  test(`no pi-sbx advice when ${condition}`, async t => {
    const h = await harness(t);
    if (condition === "absent CLI") h.failInspection();
    else if (condition === "unregistered") h.workspace.registered = false;
    else { h.workspace.capabilities.sandbox = false; h.workspace.sandbox = null; }
    h.tools.push({ name: "radar_workspace_context" });
    await h.discover();
    assert.equal(h.displayed.length, 0);
    await assert.rejects(stat(h.marker), { code: "ENOENT" });
    h.tools.length = 0;
    await h.discover();
    assert.match(h.text(), /pi-radar/);
    assert.doesNotMatch(h.text(), /pi-sbx/);
  });
}
