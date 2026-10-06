import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import fs from "node:fs";
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { syncBuiltinESMExports } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test, type TestContext } from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import type { ExtensionAPI, SourceInfo } from "@earendil-works/pi-coding-agent";
import requireSandbox from "../../internal/pi/require-sandbox.ts";

const names = ["bash", "read", "write", "edit", "grep", "find", "ls"];
const exec = promisify(execFile);
const repository = fileURLToPath(new URL("../../", import.meta.url));
const cli = join(dirname(fileURLToPath(import.meta.resolve("@earendil-works/pi-coding-agent"))), "bundle", "cli.js");
const helper = join(repository, "internal/pi/require-sandbox.ts");

async function harness(t: TestContext) {
  const root = await mkdtemp(join(tmpdir(), "radar-require-sandbox-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const hooks = new Map<string, (event: any, ctx: any) => any>();
  const commands: any[] = [];
  const tools: any[] = [];
  const notices: string[] = [];
  const errors: string[] = [];
  const exits: number[] = [];
  const originalExit = process.exit;
  const originalWriteSync = fs.writeSync;
  process.exit = ((code: number) => { exits.push(code); }) as typeof originalExit;
  fs.writeSync = ((fd: number, text: string) => {
    assert.equal(fd, 2);
    errors.push(text);
    return Buffer.byteLength(text);
  }) as typeof originalWriteSync;
  syncBuiltinESMExports();
  t.after(() => {
    process.exit = originalExit;
    fs.writeSync = originalWriteSync;
    syncBuiltinESMExports();
  });
  let shutdowns = 0;
  let inspections = 0;
  const ctx = {
    cwd: process.cwd(), hasUI: true, mode: "tui",
    ui: { notify: (text: string, level: string) => {
      assert.equal(level, "error");
      notices.push(text);
    } },
    shutdown: () => { shutdowns++; },
    // Session records are deliberately untrusted, including persisted /sbx off.
    sessionManager: { getEntries: () => assert.fail("must not trust session records") },
  };
  const pi = {
    on: (event: string, handler: (event: any, ctx: any) => any) => hooks.set(event, handler),
    getCommands: () => { inspections++; return commands; },
    getAllTools: () => tools,
    getActiveTools: () => assert.fail("tool selection is not provider ownership"),
    registerTool: () => assert.fail("guard must not own routing"),
    registerCommand: () => assert.fail("guard must not add a command"),
    exec: () => assert.fail("guard must not install or inspect by subprocess"),
    events: { on: () => assert.fail("no pi-sbx inter-extension protocol") },
  };
  const load = () => requireSandbox(pi as unknown as ExtensionAPI);
  load();
  const emit = (event: string, details: any = {}) => hooks.get(event)!({ type: event, ...details }, ctx);
  const manifest = join(root, "arbitrary-package", "package.json");
  const source = join(dirname(manifest), "src", "extensions", "index.ts");
  async function provider(version: unknown = "0.6.0", name: unknown = "@christianmoesl/pi-sbx") {
    await mkdir(dirname(source), { recursive: true });
    await writeFile(source, "export default function() {}\n");
    await writeFile(manifest, JSON.stringify({ name, version }));
    const sourceInfo: SourceInfo = { path: source, source: "local", scope: "user", origin: "package", baseDir: root };
    commands.splice(0, commands.length, { name: "sbx", source: "extension", sourceInfo });
    tools.splice(0, tools.length, ...names.map(name => ({ name, sourceInfo })));
  }
  return {
    root, hooks, ctx, pi, commands, tools, notices, errors, exits, emit, load, provider, source, manifest,
    shutdowns: () => shutdowns, inspections: () => inspections,
    tool: (name = "bash") => emit("tool_call", { toolName: name, input: {} }),
    start: (reason = "startup") => emit("session_start", { reason }),
    discover: (reason = "startup") => emit("resources_discover", { reason, cwd: ctx.cwd }),
    shell: () => emit("user_bash", { command: "printf unsafe", cwd: ctx.cwd, excludeFromContext: false }),
  };
}

function refused(h: Awaited<ReturnType<typeof harness>>, reason: RegExp) {
  assert.ok(h.shutdowns() > 0);
  assert.ok(h.exits.length > 0);
  assert.ok(h.exits.every(code => code === 1));
  assert.match(h.errors.at(-1)!, reason);
  assert.match(h.notices.at(-1)!, reason);
  assert.match(h.notices.at(-1)!, /stable|>=0\.6\.0/);
  assert.match(h.notices.at(-1)!, /host terminal.*install\/update/);
  assert.match(h.notices.at(-1)!, /pi install npm:@christianmoesl\/pi-sbx/);
  assert.match(h.notices.at(-1)!, /pi config.*restart/);
  assert.equal(h.tool().block, true);
}

test("factory blocks every tool and claims user shell until lifecycle verification", async t => {
  const h = await harness(t);
  await h.provider();
  assert.equal(h.inspections(), 0, "public metadata is not bound in the factory");
  for (const tool of [...names, "other_tool"]) {
    assert.equal(h.tool(tool).block, true);
    assert.equal(h.tool(tool).terminate, true);
  }
  const blocked = h.shell();
  assert.ok(blocked.operations);
  await assert.rejects(blocked.operations.exec("printf unsafe", h.ctx.cwd, {}), /not yet been verified/);
  assert.equal(h.emit("input", { text: "model prompt" }).action, "handled");
  assert.equal(h.shutdowns(), 1);
  h.start();
  h.discover();
  assert.equal(h.tool(), undefined);
  assert.equal(h.shell(), undefined);
  assert.equal(h.emit("input", { text: "model prompt" }), undefined);
});

test("missing, disabled or failed providers cannot be established by package/config/session records", async t => {
  const h = await harness(t);
  await h.provider();
  await mkdir(join(h.root, ".pi"));
  const declaration = JSON.stringify({ packages: [{ source: "npm:@christianmoesl/pi-sbx@0.6.0", extensions: [], autoload: false }], extensions: [`-${h.source}`] });
  await writeFile(join(h.root, ".pi", "settings.json"), declaration);
  h.commands.length = 0;
  h.tools.length = 0;
  h.start();
  h.discover();
  refused(h, /active \/sbx command is missing/);
  assert.equal(h.notices.length, 1, "don't repeat the same startup diagnostic");
  assert.equal(await readFile(join(h.root, ".pi", "settings.json"), "utf8"), declaration);
  await assert.rejects(h.shell().operations.exec(), /launch refused/);
});

for (const version of ["0.5.9", "0.0.6", "0.6.0-rc.1", "0.7.0-beta", "1.0.0-preview", "v0.6.0", "0.6", "00.6.0", "0.06.0", "0.6.00", "0.6.0+", "0.6.0\n", null, 6]) {
  test(`rejects unverifiable/old/prerelease version ${JSON.stringify(version)}`, async t => {
    const h = await harness(t);
    await h.provider(version);
    h.start();
    refused(h, /version .* incompatible/);
  });
}

for (const version of ["0.6.0", "0.6.1", "0.7.0", "1.0.0", "2.0.0+local.1"]) {
  test(`accepts active stable pi-sbx ${version} without requiring tools be selected`, async t => {
    const h = await harness(t);
    await h.provider(version);
    h.start();
    h.discover();
    assert.equal(h.shutdowns(), 0);
    assert.deepEqual(h.notices, []);
    assert.equal(h.tool(), undefined);
    assert.equal(h.shell(), undefined);
  });
}

for (const name of names) {
  test(`rejects missing and overridden ${name} even if /sbx is valid`, async t => {
    const h = await harness(t);
    await h.provider();
    h.tools.splice(h.tools.findIndex(tool => tool.name === name), 1);
    h.start();
    refused(h, new RegExp(`${name} tool is missing or overridden`));
    await h.provider();
    const override = join(h.root, "override.ts");
    await writeFile(override, "export default function() {}\n");
    const tool = h.tools.find(tool => tool.name === name);
    tool.sourceInfo = { ...tool.sourceInfo, path: override };
    h.discover("reload");
    refused(h, new RegExp(`${name} tool is missing or overridden`));
  });
}

test("rejects duplicate, suffixed and non-extension /sbx commands", async t => {
  const h = await harness(t);
  for (const replacement of [
    [{ name: "sbx", source: "prompt" }],
    [{ name: "sbx:1", source: "extension" }, { name: "sbx:2", source: "extension" }],
    [{ name: "sbx", source: "extension" }, { name: "sbx", source: "extension" }],
  ]) {
    await h.provider();
    const sourceInfo = h.commands[0].sourceInfo;
    h.commands.splice(0, h.commands.length, ...replacement.map(command => ({ ...command, sourceInfo })));
    h.start();
    refused(h, /active \/sbx command is missing or ambiguous/);
  }
});

test("only matching real extension paths establish ownership; package identity alone does not", async t => {
  const h = await harness(t);
  await h.provider();
  const otherEntry = join(dirname(h.source), "other.ts");
  await writeFile(otherEntry, "export default function() {}\n");
  h.tools[0].sourceInfo = { ...h.tools[0].sourceInfo, path: otherEntry };
  h.start();
  refused(h, /bash tool is missing or overridden/);
});

test("local symlinks use canonical extension ownership and the real closest manifest", async t => {
  const h = await harness(t);
  await h.provider("0.6.0+local");
  const link = join(h.root, "custom-install");
  await symlink(dirname(h.manifest), link, "dir");
  h.commands[0].sourceInfo = { ...h.commands[0].sourceInfo, path: join(link, "src", "extensions", "index.ts") };
  h.start();
  h.discover();
  assert.equal(h.shutdowns(), 0);
  assert.equal(h.tool(), undefined);
  await writeFile(join(dirname(h.source), "package.json"), JSON.stringify({ name: "unrelated", version: "9.0.0" }));
  h.start("reload");
  refused(h, /closest package.json.*not @christianmoesl\/pi-sbx/);
});

test("rejects missing/relative/pseudo/inaccessible metadata rather than guessing global install paths", async t => {
  const h = await harness(t);
  for (const sourceInfo of [undefined, {}, { path: "relative/index.ts" }, { path: "<builtin:bash>" }, { path: join(h.root, "missing.ts") }]) {
    await h.provider();
    h.commands[0].sourceInfo = sourceInfo;
    h.start();
    refused(h, /verifiable extension sourceInfo.path|ENOENT/);
  }
  await h.provider();
  delete h.tools[0].sourceInfo;
  h.start();
  refused(h, /verifiable extension sourceInfo.path/);
});

test("rejects wrong, malformed and absent manifests at the nearest boundary", async t => {
  const h = await harness(t);
  await h.provider("0.6.0", "unrelated");
  h.start();
  refused(h, /closest package.json.*not @christianmoesl\/pi-sbx/);
  await writeFile(h.manifest, "broken JSON");
  h.start("reload");
  refused(h, /JSON/);
  await rm(h.manifest);
  h.start("reload");
  refused(h, /No package.json owns/);
});

test("unavailable runtime metadata fails closed instead of a caught throw-only startup error", async t => {
  const h = await harness(t);
  h.pi.getCommands = () => { throw new Error("public metadata unavailable"); };
  assert.doesNotThrow(() => h.start());
  refused(h, /public metadata unavailable/);
});

test("/sbx off is permitted by verified provider identity, not by persisted enabled/disabled records", async t => {
  const h = await harness(t);
  await h.provider();
  h.commands[0].handler = () => {}; // The guard never replaces or intercepts /sbx.
  h.start();
  h.emit("input", { text: "/sbx off" });
  h.ctx.sessionManager.getEntries = () => assert.fail("off consent belongs to pi-sbx");
  assert.equal(h.shell(), undefined);
  assert.equal(h.tool(), undefined);
  assert.equal(h.shutdowns(), 0);
});

test("UIless enforcement remains active and reports to stderr; UI notification failure still shuts down", async t => {
  const h = await harness(t);
  for (const mode of ["print", "json", "rpc", "tui"]) {
    h.load();
    h.ctx.mode = mode;
    h.ctx.hasUI = false;
    h.start();
    assert.equal(h.tool().block, true);
    assert.equal(h.emit("input", { text: "startup prompt" }).action, "handled");
    await assert.rejects(h.shell().operations.exec(), /launch refused/);
  }
  assert.equal(h.errors.length, 4);
  assert.ok(h.exits.length > 0);
  assert.ok(h.exits.every(code => code === 1));
  assert.deepEqual(h.notices, []);
  h.load();
  h.ctx.hasUI = true;
  h.ctx.ui.notify = () => { throw new Error("notification failure"); };
  const before = h.shutdowns();
  assert.throws(() => h.start(), /notification failure/);
  assert.equal(h.shutdowns(), before + 1);
  assert.equal(h.tool().block, true);
});

test("unresolvable session cwd cannot throw through user_bash into a host fallback", async t => {
  const h = await harness(t);
  await h.provider();
  h.start();
  h.ctx.cwd = join(h.root, "missing-cwd");
  const claimed = h.shell();
  await assert.rejects(claimed.operations.exec(), /Cannot verify the session cwd/);
  assert.equal(h.shutdowns(), 1);
  assert.deepEqual(h.exits, [1]);
});

test("resources discovery catches late startup overrides; execution catches dynamically overridden tools", async t => {
  const h = await harness(t);
  await h.provider();
  h.start();
  h.tools[0].sourceInfo = { path: "<builtin:bash>" };
  h.discover();
  refused(h, /verifiable extension sourceInfo.path/);
  await h.provider();
  h.start("reload");
  h.tools[0].sourceInfo = { path: "<sdk:bash>" };
  assert.equal(h.tool().block, true);
  refused(h, /verifiable extension sourceInfo.path/);
});

test("reload and return revalidate; outside the originating workspace tools/input/user shell are unaffected", async t => {
  const h = await harness(t);
  await h.provider();
  h.start();
  h.ctx.cwd = join(h.root, "outside");
  await mkdir(h.ctx.cwd);
  h.commands.length = 0;
  h.tools.length = 0;
  h.start("resume");
  h.discover();
  h.load(); // Pi rebuilds the extension factory when replacing sessions.
  h.start("resume");
  assert.equal(h.tool(), undefined);
  assert.equal(h.shell(), undefined);
  assert.equal(h.emit("input", { text: "outside prompt" }), undefined);
  assert.equal(h.shutdowns(), 0);
  h.ctx.cwd = process.cwd();
  assert.equal(h.tool().block, true, "outside lifecycle must not grant workspace execution");
  h.start("resume");
  refused(h, /active \/sbx command is missing/);
  await h.provider();
  h.load();
  assert.equal(h.tool().block, true, "new factory is closed until verified again");
  h.start("reload");
  assert.equal(h.tool(), undefined);
  await writeFile(h.manifest, JSON.stringify({ name: "@christianmoesl/pi-sbx", version: "0.5.0" }));
  h.discover("reload");
  refused(h, /version "0.5.0" is incompatible/);
});

test("scope contains nested members and canonical symlinks, but not symlink escapes or prefix siblings", async t => {
  const h = await harness(t);
  const member = await mkdtemp(join(process.cwd(), ".guard-scope-"));
  const sibling = await mkdtemp(`${process.cwd()}-sibling-`);
  t.after(async () => {
    await rm(member, { recursive: true, force: true });
    await rm(sibling, { recursive: true, force: true });
  });
  const alias = join(h.root, "workspace-alias");
  const escape = join(member, "escape");
  await symlink(process.cwd(), alias, "dir");
  await symlink(h.root, escape, "dir");
  await h.provider();
  h.ctx.cwd = member;
  h.start("resume");
  assert.equal(h.tool(), undefined);
  await h.provider("0.5.0");
  h.start("reload");
  refused(h, /version "0.5.0" is incompatible/);
  for (const cwd of [escape, sibling]) {
    h.ctx.cwd = cwd;
    const shutdowns = h.shutdowns();
    h.start("resume");
    h.discover();
    assert.equal(h.tool(), undefined);
    assert.equal(h.shell(), undefined);
    assert.equal(h.emit("input", { text: "outside" }), undefined);
    assert.equal(h.shutdowns(), shutdowns);
  }
  h.ctx.cwd = alias;
  h.load();
  assert.equal(h.tool().block, true);
  h.start("resume");
  refused(h, /version "0.5.0" is incompatible/);
});

async function realFixture(t: TestContext, version: string) {
  const root = await mkdtemp(join(tmpdir(), "radar-prerequisite-real-pi-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const workspace = join(root, "workspace");
  const agent = join(root, "agent");
  const distribution = join(root, "arbitrary-local-package");
  await mkdir(workspace);
  await mkdir(agent);
  await mkdir(distribution);
  await writeFile(join(agent, "settings.json"), JSON.stringify({ quietStartup: true, enableInstallTelemetry: false }));
  await writeFile(join(distribution, "package.json"), JSON.stringify({ name: "@christianmoesl/pi-sbx", version }));
  const source = join(distribution, "index.ts");
  const shellMarker = join(root, "user-shell-called");
  const modelMarker = join(root, "model-called");
  const snapshot = join(root, "snapshot.json");
  const inputMarker = join(root, "startup-input-accepted");
  const shutdownMarker = join(root, "shutdown-completed");
  const hostMarker = join(root, "unsafe-host-execution");
  const probe = join(root, "probe.ts");
  await writeFile(probe, `import { writeFileSync } from 'node:fs';
export default function(pi) {
  pi.on('session_start', (_event, ctx) => {
    writeFileSync(${JSON.stringify(snapshot)}, JSON.stringify({commands: pi.getCommands(), tools: pi.getAllTools(), cwd: ctx.cwd}));
  });
  // Loaded after the guard: observing input proves the guard allowed it through.
  // Claim it so even a regression cannot make a model/network call.
  pi.on('input', () => { writeFileSync(${JSON.stringify(inputMarker)}, 'accepted'); return {action: 'handled'}; });
  pi.on('session_shutdown', () => { writeFileSync(${JSON.stringify(shutdownMarker)}, 'completed'); });
  pi.registerCommand('fixture-reload', {description: 'offline reload', handler: async (_args, ctx) => { await ctx.reload(); }});
}
`);
  await writeFile(source, `import { writeFileSync } from 'node:fs';
import { Type } from 'typebox';
export default function(pi) {
  pi.registerCommand('sbx', {description: 'fixture provider', handler: async () => {}});
  for (const name of ${JSON.stringify(names)}) pi.registerTool({
    name, label: name, description: 'fixture routed tool', parameters: Type.Object({}),
    execute: async () => ({content: [{type: 'text', text: 'fixture'}], details: {}})
  });
  pi.on('session_start', () => { pi.setActiveTools(['read']); });
  // Mimic older pi-sbx first-claim-wins user_bash, loaded BEFORE the guard.
  pi.on('user_bash', () => {
    writeFileSync(${JSON.stringify(shellMarker)}, 'unsafe earlier handler called');
    return {result: {output: '', exitCode: 0, cancelled: false, truncated: false}};
  });
  // No model or network call is ever made, even if the guard regresses.
  pi.on('before_agent_start', () => { writeFileSync(${JSON.stringify(modelMarker)}, 'model execution reached'); throw Error('offline fixture prohibits model calls'); });
  pi.registerCommand('fixture-snapshot', {description: 'offline metadata inspection', handler: async (_args, ctx) => {
    writeFileSync(${JSON.stringify(snapshot)}, JSON.stringify({commands: pi.getCommands(), tools: pi.getAllTools(), cwd: ctx.cwd}));
  }});
}
`);
  const env = {
    PATH: `${dirname(process.execPath)}:/usr/local/bin:/usr/bin:/bin`, HOME: root,
    PI_CODING_AGENT_DIR: agent, PI_OFFLINE: "1", PI_TELEMETRY: "0", TERM: "xterm-256color", TMPDIR: root,
    XDG_CONFIG_HOME: join(root, "config"), XDG_DATA_HOME: join(root, "data"), XDG_STATE_HOME: join(root, "state"),
  };
  const args = [cli, "--offline", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files", "--extension", source, "--extension", helper, "--extension", probe];
  const withoutProvider = args.filter((_arg, index) => index !== args.indexOf(source) && index !== args.indexOf(source) - 1);
  return { root, workspace, env, args, withoutProvider, source, shellMarker, modelMarker, snapshot, inputMarker, shutdownMarker, hostMarker };
}

test("offline real Pi: stable local provider exposes effective SourceInfo even with only read selected", { timeout: 20000 }, async t => {
  const h = await realFixture(t, "0.6.0");
  const run = exec(process.execPath, [...h.args, "-p", "/fixture-reload", "/fixture-snapshot"], { cwd: h.workspace, env: h.env, timeout: 15000 });
  run.child.stdin!.end(); // Print mode reads piped stdin before startup.
  const { stderr } = await run;
  assert.doesNotMatch(stderr, /launch refused|Extension error/);
  const snapshot = JSON.parse(await readFile(h.snapshot, "utf8"));
  assert.equal(snapshot.commands.find((command: any) => command.name === "sbx").sourceInfo.path, h.source);
  for (const name of names) assert.equal(snapshot.tools.find((tool: any) => tool.name === name).sourceInfo.path, h.source);
  await assert.rejects(readFile(h.modelMarker), { code: "ENOENT" });
});

for (const missing of [false, true]) {
  test(`offline real Pi: ${missing ? "missing" : "old"} provider stops print startup input without throw-only failure`, { timeout: 20000 }, async t => {
    const h = await realFixture(t, "0.5.0");
    const run = exec(process.execPath, [...(missing ? h.withoutProvider : h.args), "-p", "startup prompt"], { cwd: h.workspace, env: h.env, timeout: 15000 });
    run.child.stdin!.end();
    await assert.rejects(run, (error: any) => {
      assert.equal(error.code, 1, "non-TUI launch must fail before processing prompts");
      assert.match(error.stderr, /Radar sandbox launch refused/);
      assert.match(error.stderr, missing ? /active \/sbx command is missing/ : /version "0.5.0" is incompatible/);
      assert.doesNotMatch(error.stderr + error.stdout, /Extension error|No model selected|offline fixture prohibits model/);
      return true;
    });
    await assert.rejects(readFile(h.inputMarker), { code: "ENOENT" });
    await assert.rejects(readFile(h.modelMarker), { code: "ENOENT" });
  });
}

for (const missing of [false, true]) {
  test(`offline real Pi RPC: ${missing ? "missing" : "older first-loaded"} provider exits before queued bash can execute`, { timeout: 20000 }, async t => {
    const h = await realFixture(t, "0.5.0");
    const child = spawn(process.execPath, [...(missing ? h.withoutProvider : h.args), "--mode", "rpc"], { cwd: h.workspace, env: h.env, stdio: ["pipe", "pipe", "pipe"] });
    t.after(() => { child.kill("SIGKILL"); });
    let output = "";
    let errors = "";
    child.stdout.setEncoding("utf8").on("data", text => { output += text; });
    child.stderr.setEncoding("utf8").on("data", text => { errors += text; });
    const exit = new Promise<number | null>((done, reject) => {
      child.on("error", reject);
      child.on("close", code => done(code));
    });
    // Send before runtime startup, not after observing the failure. Older Pi
    // defers ctx.shutdown until AFTER handling its next RPC command, so without
    // the hard exit the earlier provider would claim this shell immediately.
    child.stdin.write(JSON.stringify({ id: "shell", type: "bash", command: `printf unsafe > '${h.hostMarker}'` }) + "\n"
      + JSON.stringify({ id: "prompt", type: "prompt", message: "startup prompt" }) + "\n");
    assert.equal(await exit, 1);
    assert.match(output + errors, /launch refused/);
    assert.doesNotMatch(output, /"command":"bash"|"event":"user_bash"|"command":"prompt"/);
    await assert.rejects(readFile(h.shellMarker), { code: "ENOENT" });
    await assert.rejects(readFile(h.hostMarker), { code: "ENOENT" });
    await assert.rejects(readFile(h.inputMarker), { code: "ENOENT" });
    await assert.rejects(readFile(h.modelMarker), { code: "ENOENT" });
  });
}

for (const missing of [false, true]) {
  test(`offline real Pi TUI: ${missing ? "missing" : "older first-loaded"} provider shuts down before startup input or shell executes`, { timeout: 25000 }, async t => {
    const h = await realFixture(t, "0.5.0");
    // Standard-library PTY, no dependency, model or network. Buffer !! input both
    // before startup and when refusal appears. Incompatible TUI exits nonzero,
    // with synchronous stderr retained, without entering its user input loop.
    const python = `import errno, os, pty, select, subprocess, sys, time
master, slave = pty.openpty()
p = subprocess.Popen(sys.argv[1:], stdin=slave, stdout=slave, stderr=slave)
os.close(slave)
# Buffer shell input BEFORE startup as well as during refusal, so the old
# provider's earlier user_bash handler cannot be reached in either window.
os.write(master, ${JSON.stringify(`!!printf unsafe > '${h.hostMarker}'\r`)}.encode())
output = b''
sent = False
deadline = time.monotonic() + 18
try:
    while time.monotonic() < deadline:
        if select.select([master], [], [], 0.05)[0]:
            try: chunk = os.read(master, 65536)
            except OSError as error:
                if error.errno == errno.EIO: break
                raise
            if not chunk: break
            output += chunk
            if not sent and b'Radar sandbox launch refused' in output:
                os.write(master, ${JSON.stringify(`!!printf unsafe > '${h.hostMarker}'\r`)}.encode())
                sent = True
        if p.poll() is not None: break
    # PTY EOF/EIO can precede waitpid visibility by a scheduling tick on macOS.
    # Reap the child before treating closure as a hung shutdown.
    try: p.wait(timeout=2)
    except subprocess.TimeoutExpired:
        p.kill()
        raise RuntimeError('Pi did not shut down after prerequisite refusal')
    if not sent: raise RuntimeError('No startup refusal was displayed')
    if p.returncode != 1: raise RuntimeError('Expected prerequisite failure exit 1, got: ' + str(p.returncode))
finally:
    if p.poll() is None: p.kill()
    p.wait()
    os.close(master)
sys.stdout.buffer.write(output)
  `;
    const { stdout } = await exec("python3", ["-c", python, process.execPath, ...(missing ? h.withoutProvider : h.args), "startup prompt"], { cwd: h.workspace, env: h.env, timeout: 22000, maxBuffer: 1024 * 1024 });
    assert.match(stdout, /Radar sandbox launch refused/);
    assert.doesNotMatch(stdout, /Extension error|No model selected/);
    await assert.rejects(readFile(h.shellMarker), { code: "ENOENT" });
    await assert.rejects(readFile(h.hostMarker), { code: "ENOENT" });
    await assert.rejects(readFile(h.inputMarker), { code: "ENOENT" });
    await assert.rejects(readFile(h.modelMarker), { code: "ENOENT" });
  });
}
