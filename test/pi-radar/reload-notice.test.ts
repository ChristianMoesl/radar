import assert from "node:assert/strict";
import { mkdtemp, mkdir, realpath, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, type TestContext } from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { installedPackageFile, isNewerVersion, showReloadNotice } from "../../extensions/pi-radar/reload-notice.ts";
import { radarPackageVersion } from "../../extensions/pi-radar/version.ts";

for (const [installed, loaded, newer] of [
  ["0.4.1", "0.4.0", true], ["0.10.0", "0.9.0", true], ["1.0.0", "0.99.99", true],
  ["0.4.0", "0.4.0", false], ["0.3.9", "0.4.0", false], ["0.4.0+new", "0.4.0+old", false],
  ["1.0.0", "1.0.0-rc.1", true], ["1.0.0-rc.2", "1.0.0", false],
  ["1.0.0-rc.10", "1.0.0-rc.2", true], ["1.0.0-beta", "1.0.0-alpha", true],
  ["1.0.0-alpha.1", "1.0.0-alpha", true], ["1.0.0-alpha", "1.0.0-alpha.1", false],
  ["1.0.0-alpha.beta", "1.0.0-alpha.2", true], ["1.0.0-alpha.2", "1.0.0-alpha.beta", false],
  ["01.0.0", "0.4.0", false], ["1.0.0-01", "0.4.0", false], ["1.0", "0.4.0", false],
  [null, "0.4.0", false], [{}, "0.4.0", false], ["1.0.0", "dev", false],
] as const) {
  test(`newer version ${JSON.stringify(installed)} vs ${loaded}`, () => {
    assert.equal(isNewerVersion(installed, loaded), newer);
  });
}

test("npm source uses the stable install path; custom sources use their own package", () => {
  const profile = "/profile";
  for (const module of [
    "/profile/npm/node_modules/@christianmoesl/pi-radar/extensions/pi-radar/reload-notice.ts",
    "/profile/npm/node_modules/.pnpm/@christianmoesl+pi-radar@0.3.0/node_modules/@christianmoesl/pi-radar/extensions/pi-radar/reload-notice.ts",
  ]) assert.equal(installedPackageFile(profile, module), "/profile/npm/node_modules/@christianmoesl/pi-radar/package.json");
  assert.equal(installedPackageFile(profile, "/dev/radar/extensions/pi-radar/reload-notice.ts"), "/dev/radar/package.json");
  assert.equal(installedPackageFile(profile, "/profile/npm-other/extensions/pi-radar/reload-notice.ts"), "/profile/npm-other/package.json");
});

async function harness(t: TestContext, mode = "tui", hasUI = true) {
  const root = await mkdtemp(join(tmpdir(), "radar-reload-notice-"));
  const file = join(root, "package.json");
  const statuses: [string, string | undefined][] = [];
  const shutdowns: (() => Promise<void>)[] = [];
  const nativeInterval = globalThis.setInterval;
  let poll: (() => Promise<void>) | undefined;
  let timer: ReturnType<typeof setInterval> | undefined;
  t.mock.method(globalThis, "setInterval", (callback: () => Promise<void>, delay: number) => {
    assert.equal(delay, 30_000);
    poll = callback;
    timer = nativeInterval(() => {}, delay);
    return timer;
  });
  const clear = t.mock.method(globalThis, "clearInterval");
  t.after(async () => {
    for (const stop of shutdowns) await stop();
    await rm(root, { recursive: true, force: true });
  });
  const ctx = {
    mode, hasUI,
    ui: {
      setStatus: (key: string, value: string | undefined) => statuses.push([key, value]),
      theme: { fg: (color: string, text: string) => { assert.equal(color, "error"); return `red:${text}`; } },
    },
    isIdle: () => { throw Error("must not inspect idleness"); },
    reload: () => { throw Error("must not reload"); },
  } as unknown as ExtensionContext;
  const pi = {
    on: (event: string, fn: () => Promise<void>) => { assert.equal(event, "session_shutdown"); shutdowns.push(fn); },
    sendUserMessage: () => { throw Error("must not submit messages"); },
  } as unknown as ExtensionAPI;
  const writeVersion = (version: unknown, name = "@christianmoesl/pi-radar") => writeFile(file, JSON.stringify({ name, version }));
  return {
    root, file, statuses, clear, writeVersion,
    start: () => showReloadNotice(pi, ctx, file),
    poll: async () => { assert.ok(poll); await poll(); },
    stop: async () => { for (const stop of shutdowns) await stop(); },
    timer: () => timer,
  };
}

test("newer installed pi-radar shows red status, unchanged/older versions show nothing", async t => {
  const h = await harness(t);
  await h.writeVersion(radarPackageVersion);
  await h.start();
  assert.deepEqual(h.statuses, []);
  assert.equal(h.timer()?.hasRef(), false);
  await h.writeVersion("999.0.0");
  await h.poll();
  assert.deepEqual(h.statuses.at(-1), ["radar-reload", "red:pi-radar updated · /reload to apply"]);
  const count = h.statuses.length;
  await h.poll();
  assert.equal(h.statuses.length, count, "no redundant status updates");
  await h.writeVersion(radarPackageVersion);
  await h.poll();
  assert.deepEqual(h.statuses.at(-1), ["radar-reload", undefined]);
  await h.writeVersion("0.0.0");
  await h.poll();
  assert.equal(h.statuses.length, count + 1);
});

test("missing/malformed/wrong packages stay quiet and polling recovers", async t => {
  const h = await harness(t);
  await h.start(); // package replacement may temporarily remove package.json
  await writeFile(h.file, "{");
  await h.poll();
  await h.writeVersion("999.0.0", "@christianmoesl/pi-sbx");
  await h.poll();
  await writeFile(h.file, "null");
  await h.poll();
  await h.writeVersion("invalid");
  await h.poll();
  assert.deepEqual(h.statuses, []);
  await h.writeVersion("999.0.0");
  await h.poll();
  assert.match(h.statuses.at(-1)![1]!, /reload/);
  await rm(h.file);
  await h.poll();
  assert.deepEqual(h.statuses.at(-1), ["radar-reload", undefined]);
});

test("a replaced package symlink is followed, not the old pnpm version directory", async t => {
  const h = await harness(t);
  const oldDir = join(h.root, "old"), newDir = join(h.root, "new");
  await mkdir(oldDir); await mkdir(newDir);
  await writeFile(join(oldDir, "package.json"), JSON.stringify({ name: "@christianmoesl/pi-radar", version: radarPackageVersion }));
  await writeFile(join(newDir, "package.json"), JSON.stringify({ name: "@christianmoesl/pi-radar", version: "999.0.0" }));
  await symlink(join(oldDir, "package.json"), h.file);
  await h.start();
  assert.deepEqual(h.statuses, []);
  await rm(h.file);
  await symlink(join(newDir, "package.json"), h.file);
  await h.poll();
  assert.match(h.statuses.at(-1)![1]!, /reload/);
});

test("shutdown clears the notice and stops polling, including an in-flight check", async t => {
  const h = await harness(t);
  await h.writeVersion("999.0.0");
  await h.start();
  const pending = h.poll();
  await h.stop();
  const count = h.statuses.length;
  await pending;
  await h.poll();
  assert.equal(h.statuses.length, count);
  assert.deepEqual(h.statuses.at(-1), ["radar-reload", undefined]);
  assert.equal(h.clear.mock.callCount(), 1);
});

for (const mode of ["rpc", "json", "print"]) {
  test(`${mode} sessions never start a status timer`, async t => {
    const h = await harness(t, mode);
    await h.writeVersion("999.0.0");
    await h.start();
    assert.equal(h.timer(), undefined);
    assert.deepEqual(h.statuses, []);
  });
}
test("UI-less child sessions never start a status timer", async t => {
  const h = await harness(t, "tui", false);
  await h.writeVersion("999.0.0");
  await h.start();
  assert.equal(h.timer(), undefined);
  assert.deepEqual(h.statuses, []);
});

test("a symlinked Pi profile still follows the stable npm package link", async t => {
  const root = await mkdtemp(join(tmpdir(), "radar-profile-link-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const profile = join(root, "profile"), canonical = join(root, "canonical");
  await mkdir(join(canonical, "npm"), { recursive: true });
  await symlink(canonical, profile);
  // Node resolves loaded modules through every symlink, including macOS /var.
  const source = join(await realpath(canonical), "npm/node_modules/.pnpm/old/node_modules/@christianmoesl/pi-radar/extensions/pi-radar/reload-notice.ts");
  assert.equal(installedPackageFile(profile, source), join(profile, "npm/node_modules/@christianmoesl/pi-radar/package.json"));
});
