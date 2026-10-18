import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import test from "node:test";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { AgentHost } from "../src/host.ts";
import { taskId } from "../src/index.ts";

const entry = resolve(import.meta.dirname, "../examples/counter.ts");
test("admission survives reopen, deduplicates concurrently, and rejects changed content", async () => {
  const dataDir = await mkdtemp(join(tmpdir(), "radar-agent-"));
  let host: AgentHost | undefined;
  try {
    host = await AgentHost.open(entry, {
      id: "counter",
      dataDir,
      workspace: { id: "fixture", path: dataDir, mode: "local" },
      config: {},
    });
    const event = { source: "fixture", id: "one", payload: { a: 1, b: 2 } };
    const receipts = await Promise.all(
      Array.from({ length: 10 }, () => host!.deliver(event)),
    );
    assert.equal(new Set(receipts.map((r) => r.taskId)).size, 1);
    await host.harness.waitForTask(
      taskId(receipts[0]!.taskId),
      BACKGROUND_CONTEXT,
    );
    assert.equal(
      (await host.snapshot()).presentation.summary,
      "1 events received",
    );
    await assert.rejects(
      host.deliver({ ...event, payload: { a: 2 } }),
      /different content/,
    );
    await assert.rejects(
      AgentHost.open(entry, {
        id: "other",
        dataDir,
        workspace: { id: "fixture", path: dataDir, mode: "local" },
        config: {},
      }),
      /already has an owner/,
    );
    await host.close();
    host = await AgentHost.open(entry, {
      id: "counter",
      dataDir,
      workspace: { id: "fixture", path: dataDir, mode: "local" },
      config: {},
    });
    const duplicate = await host.deliver({ ...event, payload: { b: 2, a: 1 } });
    assert.equal(duplicate.taskId, receipts[0]!.taskId);
    assert.equal(
      (await host.snapshot()).presentation.summary,
      "1 events received",
    );
  } finally {
    await host?.close();
    await rm(dataDir, { recursive: true, force: true });
  }
});

test("human actions are durable and stale decisions fail without starting work", async () => {
  const dataDir = await mkdtemp(join(tmpdir(), "radar-agent-"));
  const host = await AgentHost.open(entry, {
    id: "counter",
    dataDir,
    workspace: { id: "fixture", path: dataDir, mode: "local" },
    config: {},
  });
  try {
    const receipt = await host.deliver({
      source: "fixture",
      id: "first",
      payload: null,
    });
    await host.harness.waitForTask(taskId(receipt.taskId), BACKGROUND_CONTEXT);
    const { revision } = (await host.snapshot()).presentation;
    const event = { source: "radar.user", id: "click-1", payload: null };
    const interaction = { revision, action: "count" };
    const clicked = await host.deliver(event, interaction);
    await host.harness.waitForTask(taskId(clicked.taskId), BACKGROUND_CONTEXT);
    assert.equal(
      (await host.deliver(event, interaction)).taskId,
      clicked.taskId,
    );
    await assert.rejects(
      host.deliver({ ...event, id: "click-2" }, interaction),
      /interaction changed/,
    );
    assert.equal(
      (await host.snapshot()).presentation.summary,
      "2 events received",
    );
  } finally {
    await host.close();
    await rm(dataDir, { recursive: true, force: true });
  }
});
