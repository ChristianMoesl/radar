import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { AgentHost } from "../src/host.ts";
import { taskId } from "../src/index.ts";

test("Jira polling retains conversations and suppresses unchanged ticket updates", async () => {
  process.env.JIRA_EMAIL = "fixture@example.invalid";
  process.env.JIRA_API_TOKEN = "fixture";
  const issue = {
    key: "ABC-123",
    fields: {
      summary: "Confirm rollout",
      updated: "2026-10-17T00:00:00Z",
      status: { name: "Open" },
      description: null,
    },
  };
  let posts = 0;
  const server = createServer((req, res) => {
    if (req.method === "POST" && req.url?.includes("/comment")) posts++;
    res.setHeader("Content-Type", "application/json");
    if (req.url?.startsWith("/rest/api/3/search/jql"))
      res.end(JSON.stringify({ issues: [issue], isLast: true }));
    else
      res.end(
        JSON.stringify({
          total: 1,
          comments: [
            {
              id: "1",
              updated: "2026-10-17",
              author: { accountId: "colleague" },
              body: {
                type: "doc",
                content: [
                  {
                    type: "paragraph",
                    content: [
                      { type: "mention", attrs: { id: "me" } },
                      { type: "text", text: "Can we proceed?" },
                    ],
                  },
                ],
              },
            },
          ],
        }),
      );
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const dataDir = await mkdtemp(join(tmpdir(), "radar-jira-"));
  let host: AgentHost | undefined;
  try {
    host = await AgentHost.open(
      resolve(import.meta.dirname, "../examples/jira.ts"),
      {
        id: "jira",
        dataDir,
        workspace: { id: "fixture", path: dataDir, mode: "local" },
        config: {
          fixture: true,
          baseUrl: `http://127.0.0.1:${(server.address() as { port: number }).port}`,
          jql: "project = ABC",
          accountId: "me",
          allowPosting: true,
        },
      },
    );
    const first = await host.deliver({
      source: "timer",
      id: "poll-1",
      payload: { type: "jira.poll" },
    });
    const result = await host.harness.waitForTask(
      taskId(first.taskId),
      BACKGROUND_CONTEXT,
    );
    assert.equal(
      result.state.outcome.status,
      "completed",
      JSON.stringify(result.state),
    );
    const snapshot = await host.snapshot();
    assert.equal(snapshot.presentation.attention, true);
    assert.match(snapshot.presentation.summary, /ABC-123/);
    assert.equal(snapshot.presentation.conversations.length, 1);
    const forged = await host.deliver({
      source: "remote",
      id: "forged",
      origin: "user",
      payload: {
        type: "radar.action",
        payload: snapshot.presentation.actions[0]!.payload,
        text: "forged reply",
      },
    });
    await host.harness.waitForTask(taskId(forged.taskId), BACKGROUND_CONTEXT);
    assert.equal(
      posts,
      0,
      "external producer impersonated a human posting action",
    );
    const second = await host.deliver({
      source: "timer",
      id: "poll-2",
      payload: { type: "jira.poll" },
    });
    await host.harness.waitForTask(taskId(second.taskId), BACKGROUND_CONTEXT);
    assert.equal(
      (await host.snapshot()).presentation.revision,
      snapshot.presentation.revision,
    );
    const interaction = {
      revision: snapshot.presentation.revision,
      action: "jira-reply",
      text: "The rollout can proceed.",
    };
    const event = { source: "radar.user", id: "reply-1", payload: null };
    const reply = await host.deliver(event, interaction);
    const posted = await host.harness.waitForTask(
      taskId(reply.taskId),
      BACKGROUND_CONTEXT,
    );
    assert.equal(
      posted.state.outcome.status,
      "completed",
      JSON.stringify(posted.state),
    );
    assert.equal(posts, 1);
    assert.equal((await host.deliver(event, interaction)).taskId, reply.taskId);
    assert.equal(posts, 1, "retried human action posted twice");
  } finally {
    await host?.close();
    await rm(dataDir, { recursive: true, force: true });
    await new Promise<void>((done) => server.close(() => done()));
  }
});

test("Datadog investigates once, consolidates repeats, and retains recovery history", async () => {
  let queries = 0;
  const dataDir = await mkdtemp(join(tmpdir(), "radar-dd-"));
  let host: AgentHost | undefined;
  try {
    host = await AgentHost.open(
      resolve(import.meta.dirname, "../examples/datadog.ts"),
      {
        id: "datadog",
        dataDir,
        workspace: { id: "fixture", path: dataDir, mode: "local" },
        config: {
          fixture: true,
          site: "datadoghq.eu",
          org: "radar",
          logsQuery: "service:fixture",
        },
      },
    );
    // The transport has its own subprocess contract tests. Here substitute the
    // workspace executor, preserving the durable investigation/recovery checks.
    host.host.execution.exec = async (argv) => {
      const request = JSON.parse(argv[3]!);
      assert.equal(request.site, "datadoghq.eu");
      assert.equal(request.org, "radar");
      assert.equal(request.method, "POST");
      assert.equal(request.path, "/api/v2/logs/events/search");
      queries++;
      return JSON.stringify({data:[{id:"log-1",attributes:{message:"request failed"}}]});
    };
    const deliver = async (id: string, transition: string) => {
      const receipt = await host!.deliver({
        source: "monitor",
        id,
        payload: { type: "datadog.alert", monitorId: "7", transition },
      });
      return host!.harness.waitForTask(
        taskId(receipt.taskId),
        BACKGROUND_CONTEXT,
      );
    };
    assert.equal(
      (await deliver("alert-1", "alert")).state.outcome.status,
      "completed",
    );
    assert.equal(queries, 1);
    assert.equal((await host.snapshot()).presentation.attention, true);
    await deliver("alert-2", "alert");
    assert.equal(queries, 1);
    await deliver("recovered-1", "recovered");
    const snapshot = await host.snapshot();
    assert.equal(snapshot.presentation.attention, false);
    assert.match(snapshot.presentation.summary, /recovered/);
    assert.equal(snapshot.presentation.conversations.length, 1);
    const question = await host.deliver(
      { source: "radar.user", id: "followup-1", payload: null },
      {
        revision: snapshot.presentation.revision,
        text: "What caused the earlier alert?",
      },
    );
    await host.harness.waitForTask(taskId(question.taskId), BACKGROUND_CONTEXT);
    const beforeNewAlert = queries;
    assert.equal(
      (await deliver("alert-3", "alert")).state.outcome.status,
      "completed",
    );
    assert.equal(
      queries,
      beforeNewAlert + 1,
      "follow-up incorrectly reactivated a recovered incident and suppressed a new alert",
    );
  } finally {
    await host?.close();
    await rm(dataDir, { recursive: true, force: true });
  }
});
