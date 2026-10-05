import assert from "node:assert/strict";
import { spawn, execFile } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const exec = promisify(execFile);
const repository = fileURLToPath(new URL("../../", import.meta.url));
const cli = join(dirname(fileURLToPath(import.meta.resolve("@earendil-works/pi-coding-agent"))), "bundle", "cli.js");

test("installed package activates on startup/reload/restart and disappears on a non-workspace session switch", { timeout: 60000 }, async (t) => {
  const root = await mkdtemp(join(tmpdir(), "pi-radar-installed-"));
  const stops: (() => Promise<void>)[] = [];
  t.after(async () => {
    for (const stop of stops) await stop();
    await rm(root, { recursive: true, force: true });
  });
  const home = join(root, "home");
  const agent = join(home, ".pi", "agent");
  const workspace = join(root, "workspace");
  const member = join(workspace, "member");
  const outside = join(root, "outside");
  const shared = join(root, "shared");
  const bin = join(root, "bin");
  const skill = join(member, ".agents", "skills", "fixture");
  for (const path of [agent, outside, shared, bin, skill]) await mkdir(path, { recursive: true });
  await writeFile(join(skill, "SKILL.md"), "---\nname: fixture\ndescription: Fixture only\n---\nFixture");
  await writeFile(join(agent, "settings.json"), JSON.stringify({ defaultProjectTrust: "always" }));
  await writeFile(join(bin, "radar"), `#!/usr/bin/env node
import { appendFileSync } from 'node:fs';
import { relative } from 'node:path';
const args = process.argv.slice(2);
appendFileSync(process.env.FIXTURE_LOG, JSON.stringify(args) + '\\n');
if (args[0] === 'activity') process.exit(0);
if (args[0] !== 'workspace-context') process.exit(1);
const cwd = args[args.indexOf('--workspace') + 1];
const path = relative(${JSON.stringify(workspace)}, cwd);
const registered = path === '' || (!path.startsWith('..') && !path.startsWith('/'));
if (args.includes('--registration-only')) {
  console.log(JSON.stringify({registered}));
} else {
  if (!registered) throw Error('full inspection outside a workspace');
  console.log(JSON.stringify({registered, workspace_path: ${JSON.stringify(workspace)}, members: [{skill_paths: [${JSON.stringify(join(member, ".agents", "skills"))}]}], sandbox: {shared_directory: ${JSON.stringify(shared)}, shared_directory_ready: true}}));
}
`, { mode: 0o755 });
  // No LLM prompts are sent. This fixture-only command observes the actual Pi
  // runtime; the package under test is installed normally, never through -e.
  const probe = join(root, "probe.ts");
  const snapshot = join(root, "snapshot.json");
  await writeFile(probe, `import { writeFileSync } from 'node:fs';
export default function(pi) {
  pi.registerCommand('fixture-snapshot', {description:'Fixture inspection', handler: async (_args, ctx) => {
    writeFileSync(${JSON.stringify(snapshot)}, JSON.stringify({
      cwd: ctx.cwd, tools: pi.getAllTools().map(t => t.name), active: pi.getActiveTools(),
      skills: ctx.getSystemPromptOptions().skills?.map(s => s.name), tmpdir: process.env.TMPDIR,
      commands: pi.getCommands().map(c => c.name), sessionId: ctx.sessionManager.getSessionId()
    }));
  }});
}
`);
  // Also exercise the launcher's notice-only helper from outside the package,
  // as it would be loaded from Radar's cache. RPC must not consume its notice.
  const hint = join(root, "install-hint.ts");
  await writeFile(hint, await readFile(join(repository, "internal/pi/install-hint.ts")));
  const env = {
    PATH: `${bin}:${dirname(process.execPath)}:/usr/bin:/bin`, HOME: home,
    PI_CODING_AGENT_DIR: agent, PI_OFFLINE: "1", PI_TELEMETRY: "0", TERM: "dumb",
    XDG_CONFIG_HOME: join(root, "config"), XDG_DATA_HOME: join(root, "data"), XDG_STATE_HOME: join(root, "state"),
    TMPDIR: root, FIXTURE_LOG: join(root, "radar.jsonl"),
  };
  // Exercise the actual publish artifact, not a hand-maintained copy of source
  // files. No development node_modules may be present beside the extension:
  // Pi must supply its normal runtime imports, just as for an npm installation.
  const tarball = join(root, "pi-radar.tgz");
  await exec("pnpm", ["pack", "--out", tarball], { cwd: repository, timeout: 15000 });
  const { stdout: listing } = await exec("tar", ["-tzf", tarball]);
  assert.deepEqual(listing.trim().split("\n").sort(), [
    "package/LICENSE", "package/README.md", "package/extensions/pi-radar/index.ts", "package/package.json",
  ]);
  await exec("tar", ["-xzf", tarball, "-C", root]);
  const distribution = join(root, "package");
  const manifest = JSON.parse(await readFile(join(distribution, "package.json"), "utf8"));
  assert.equal(manifest.name, "@christianmoesl/pi-radar");
  assert.equal(manifest.license, "MIT");
  const license = await readFile(join(distribution, "LICENSE"), "utf8");
  assert.equal(license, await readFile(join(repository, "LICENSE"), "utf8"));
  assert.match(license, /^MIT License\n/);
  assert.match(license, /Permission is hereby granted, free of charge/);
  assert.equal(manifest.publishConfig.access, "public");
  assert.equal(manifest.publishConfig.registry, "https://registry.npmjs.org");
  assert.ok(manifest.keywords.includes("pi-package"));
  assert.deepEqual(manifest.pi.extensions, ["./extensions/pi-radar/index.ts"]);
  assert.equal(manifest.dependencies, undefined);
  assert.ok(manifest.peerDependencies["@earendil-works/pi-coding-agent"]);
  assert.ok(manifest.peerDependencies.typebox);
  assert.equal(await readFile(join(distribution, "extensions/pi-radar/index.ts"), "utf8"),
    await readFile(join(repository, "extensions/pi-radar/index.ts"), "utf8"));
  await exec(process.execPath, [cli, "install", distribution], { cwd: outside, env, timeout: 15000 });
  const settingsPath = join(agent, "settings.json");
  const settings = JSON.parse(await readFile(settingsPath, "utf8"));
  assert.equal(settings.packages.length, 1);
  settings.extensions = [probe];
  await writeFile(settingsPath, JSON.stringify(settings));

  function launch(cwd: string) {
    const child = spawn(process.execPath, [cli, "--offline", "--mode", "rpc", "--extension", hint], { cwd, env, stdio: ["pipe", "pipe", "pipe"] });
    let buffer = "";
    let stderr = "";
    let counter = 0;
    const pending = new Map<string, { resolve: (value: any) => void; reject: (error: Error) => void }>();
    child.stderr.on("data", chunk => { stderr += chunk.toString(); });
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", chunk => {
      buffer += chunk;
      let end: number;
      while ((end = buffer.indexOf("\n")) >= 0) {
        const line = buffer.slice(0, end); buffer = buffer.slice(end + 1);
        let event;
        try { event = JSON.parse(line); } catch { continue; }
        if (event.type === "response") pending.get(event.id)?.resolve(event);
      }
    });
    const closed = new Promise<void>((resolve) => child.on("close", () => {
      for (const request of pending.values()) request.reject(Error(`Pi exited: ${stderr}`));
      resolve();
    }));
    child.on("error", error => { for (const request of pending.values()) request.reject(error); });
    async function request(type: string, args: Record<string, unknown> = {}) {
      const id = String(++counter);
      let timer: ReturnType<typeof setTimeout>;
      try {
        const result = await new Promise<any>((resolve, reject) => {
          pending.set(id, { resolve, reject });
          timer = setTimeout(() => reject(Error(`Pi RPC ${type} timed out: ${stderr}`)), 10000);
          child.stdin.write(`${JSON.stringify({ id, type, ...args })}\n`);
        });
        assert.equal(result.success, true, JSON.stringify(result));
        return result;
      } finally {
        clearTimeout(timer!);
        pending.delete(id);
      }
    }
    async function stop() {
      if (child.exitCode !== null || child.signalCode !== null) return;
      child.kill("SIGTERM");
      const timer = setTimeout(() => child.kill("SIGKILL"), 2000);
      try { await closed; } finally { clearTimeout(timer); }
    }
    stops.push(stop);
    return {
      request, stop,
      snapshot: async () => {
        await request("prompt", { message: "/fixture-snapshot" });
        return JSON.parse(await readFile(snapshot, "utf8"));
      },
    };
  }
  function assertActive(value: any) {
    assert.equal(value.tools.filter((name: string) => name.startsWith("radar_")).length, 3);
    assert.equal(value.active.filter((name: string) => name.startsWith("radar_")).length, 3);
    assert.ok(value.skills.includes("fixture"));
    assert.equal(value.tmpdir, shared);
    assert.ok(value.commands.includes("radar-reload-workspace-resources"));
  }
  const first = launch(workspace);
  const original = await first.snapshot();
  assertActive(original);
  await first.request("prompt", { message: "/radar-reload-workspace-resources" });
  assertActive(await first.snapshot());
  await first.request("new_session");
  const fresh = await first.snapshot();
  assertActive(fresh);
  assert.notEqual(original.sessionId, fresh.sessionId);
  // Replacing the runtime must drop tools, commands, member skills and TMPDIR.
  const outsideSession = join(root, "outside.jsonl");
  await writeFile(outsideSession, JSON.stringify({ type: "session", version: 3, id: "outside-fixture", timestamp: new Date().toISOString(), cwd: outside }) + "\n");
  await first.request("switch_session", { sessionPath: outsideSession });
  const inactive = await first.snapshot();
  assert.equal(inactive.cwd, outside);
  assert.equal(inactive.tools.some((name: string) => name.startsWith("radar_")), false);
  assert.equal(inactive.commands.includes("radar-reload-workspace-resources"), false);
  assert.equal(inactive.skills.includes("fixture"), false);
  assert.equal(inactive.tmpdir, root);
  const workspaceSession = join(root, "workspace.jsonl");
  await writeFile(workspaceSession, JSON.stringify({ type: "session", version: 3, id: "workspace-fixture", timestamp: new Date().toISOString(), cwd: workspace }) + "\n");
  await first.request("switch_session", { sessionPath: workspaceSession });
  assertActive(await first.snapshot());
  await first.stop();

  const restarted = launch(member);
  assertActive(await restarted.snapshot());
  await restarted.stop();
  const outsidePi = launch(outside);
  const plain = await outsidePi.snapshot();
  assert.equal(plain.tools.some((name: string) => name.startsWith("radar_")), false);
  await outsidePi.stop();
  await assert.rejects(readFile(join(agent, "radar", "install-hint-seen")), { code: "ENOENT" });
  const calls = (await readFile(env.FIXTURE_LOG, "utf8")).trim().split("\n").map(line => JSON.parse(line));
  assert.ok(calls.some(args => args.includes("--registration-only") && args.includes(member)));
  assert.ok(!calls.some(args => args[0] === "workspace-context" && args.includes(outside) && !args.includes("--registration-only")));
});
