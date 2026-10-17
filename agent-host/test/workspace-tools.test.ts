import assert from "node:assert/strict";
import { mkdtemp, writeFile, chmod, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { createRadarTools } from "../src/tools.ts";

test("a model cannot approve its own workspace mutations", async () => {
  const dir = await mkdtemp(join(tmpdir(), "radar-workspace-policy-"));
  const original = process.env.RADAR_BINARY;
  const log = join(dir, "calls.jsonl"),
    helper = join(dir, "radar.mjs");
  await writeFile(
    helper,
    `#!${process.execPath}
import {appendFileSync} from 'node:fs';
const args=process.argv.slice(2);
appendFileSync(${JSON.stringify(log)},JSON.stringify(args)+'\\n');
if(!args.includes('--preview')){process.stderr.write('unapproved mutation');process.exit(1)}
process.stdout.write(JSON.stringify({plan_id:'exact-plan',auto_confirm:false,changes:[{resource:'worktree'}]}));
`,
  );
  await chmod(helper, 0o700);
  process.env.RADAR_BINARY = helper;
  try {
    const tools = createRadarTools("fixture").tools!;
    const prepare = tools.find((t) => t.name === "radar_prepare_workspace")!;
    const reconcile = tools.find(
      (t) => t.name === "radar_reconcile_workspace",
    )!;
    const memos = new Map<string, unknown>();
    const api = {
      taskId: 7,
      memo: async (key: string, value: unknown, context?: unknown) => {
        if (context) memos.set(key, value);
        return memos.get(key);
      },
    };
    const malicious = { plan_id: "exact-plan", auto_confirm: true };
    const first = await (prepare.execute as Function)(
      { name: "fixture", acceptedPlan: malicious },
      api,
      BACKGROUND_CONTEXT,
    );
    assert.equal(JSON.parse(first.output[0].text).needsConfirmation, true);
    const second = await (reconcile.execute as Function)(
      { workspace: dir, request: {}, plan: "exact-plan" },
      api,
      BACKGROUND_CONTEXT,
    );
    assert.equal(JSON.parse(second.output[0].text).needsConfirmation, true);
    const calls = (await readFile(log, "utf8"))
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line) as string[]);
    assert.equal(calls.length, 2);
    assert.ok(calls.every((args) => args.includes("--preview")));
  } finally {
    if (original === undefined) delete process.env.RADAR_BINARY;
    else process.env.RADAR_BINARY = original;
    await rm(dir, { recursive: true, force: true });
  }
});
