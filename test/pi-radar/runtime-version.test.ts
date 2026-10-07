import assert from "node:assert/strict";
import { mkdtemp, readdir, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { reportLoadedVersion } from "../../extensions/pi-radar/runtime-version.ts";
import { radarPackageVersion } from "../../extensions/pi-radar/version.ts";

test("loaded version is captured in the module, independently per session, with shutdown cleanup", async t => {
  const root = await mkdtemp(join(tmpdir(), "radar-loaded-version-"));
  const previous = process.env.PI_CODING_AGENT_DIR;
  process.env.PI_CODING_AGENT_DIR = root;
  const shutdowns: (() => Promise<void>)[] = [];
  t.after(async () => {
    for (const stop of shutdowns) await stop();
    if (previous === undefined) delete process.env.PI_CODING_AGENT_DIR;
    else process.env.PI_CODING_AGENT_DIR = previous;
    await rm(root, { recursive: true, force: true });
  });
  const pi = { on(event: string, handler: () => Promise<void>) { assert.equal(event, "session_shutdown"); shutdowns.push(handler); } };
  for (let i = 0; i < 2; i++) await reportLoadedVersion(pi as unknown as ExtensionAPI, { cwd: `/workspace-${i}` } as ExtensionContext);
  const directory = join(root, "radar/loaded");
  const files = await readdir(directory);
  assert.equal(files.length, 2);
  for (const file of files) {
    const record = JSON.parse(await readFile(join(directory, file), "utf8"));
    assert.equal(record.version, radarPackageVersion);
    assert.equal(record.profile, root);
    assert.equal(record.pid, process.pid);
    assert.ok(Date.now() - record.updated < 5000);
  }
  await shutdowns[0]();
  assert.equal((await readdir(directory)).length, 1);
  await shutdowns[0](); // idempotent
  assert.equal((await readdir(directory)).length, 1);
  await shutdowns[1]();
  assert.deepEqual(await readdir(directory), []);
});
