import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { AgentHost } from "../src/host.ts";
import { taskId } from "../src/index.ts";

test("Jira reads use host MCP and retain conversations across partial failures and restart", async () => {
  const dataDir = await mkdtemp(join(tmpdir(), "radar-jira-"));
  let host: AgentHost | undefined;
  const input = {
    id: "jira",
    dataDir,
    workspace: {
      id: "fixture",
      path: dataDir,
      mode: "sbx" as const,
      sandbox: "fixture",
    },
    config: {
      fixture: true,
      baseUrl: "https://example.atlassian.net",
      cloudId: "cloud-fixture",
      jql: "project = ABC",
      accountId: "me",
    },
  };
  let revision = "one",
    failComments = false,
    reads = 0;
  const attach = () => {
    host!.host.execution.exec = async () => {
      throw Error("service calls must never execute in SBX");
    };
    host!.host.mcp.json = async (server, tool, request) => {
      assert.equal(server, "jira");
      assert.equal(request.cloudId, "cloud-fixture");
      reads++;
      if (tool === "searchJiraIssuesUsingJql")
        return {
          isLast: true,
          issues: [
            {
              key: "ABC-123",
              fields: {
                summary: "Confirm rollout",
                updated: revision,
                status: { name: "Open" },
                description: null,
              },
            },
          ],
        };
      assert.equal(tool, "getJiraIssue");
      assert.equal(request.issueIdOrKey, "ABC-123");
      if (failComments) throw Error("partial comments");
      return {
        key: "ABC-123",
        fields: {
          comment: {
            startAt: 0,
            total: 1,
            comments: [
              {
                id: "1",
                updated: revision,
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
          },
        },
      };
    };
  };
  const poll = async (id: string) => {
    const receipt = await host!.deliver({
      source: "timer",
      id,
      payload: { type: "jira.poll" },
    });
    return host!.harness.waitForTask(
      taskId(receipt.taskId),
      BACKGROUND_CONTEXT,
    );
  };
  try {
    host = await AgentHost.open(
      resolve(import.meta.dirname, "../examples/jira.ts"),
      input,
    );
    attach();
    assert.equal((await poll("first")).state.outcome.status, "completed");
    const first = await host.snapshot();
    assert.equal(first.presentation.attention, true);
    assert.match(first.presentation.summary, /ABC-123/);
    assert.equal(first.presentation.conversations.length, 1);
    assert.deepEqual(first.presentation.actions, []);
    assert.equal((await poll("unchanged")).state.outcome.status, "completed");
    assert.equal(
      (await host.snapshot()).presentation.revision,
      first.presentation.revision,
    );
    revision = "two";
    failComments = true;
    assert.notEqual((await poll("partial")).state.outcome.status, "completed");
    assert.equal(
      (await host.snapshot()).presentation.revision,
      first.presentation.revision,
    );
    await host.close();
    host = await AgentHost.open(
      resolve(import.meta.dirname, "../examples/jira.ts"),
      input,
    );
    attach();
    failComments = false;
    assert.equal((await poll("retry")).state.outcome.status, "completed");
    const after = await host.snapshot();
    assert.equal(
      after.presentation.conversations[0]!.id,
      first.presentation.conversations[0]!.id,
    );
    assert.deepEqual(after.presentation.actions, []);
    assert.ok(reads >= 8);
  } finally {
    await host?.close();
    await rm(dataDir, { recursive: true, force: true });
  }
});

test("Jira comment posting is a separate feature with actionable manual cutover", async () => {
  const dataDir = await mkdtemp(join(tmpdir(), "radar-jira-posting-"));
  try {
    await assert.rejects(
      AgentHost.open(resolve(import.meta.dirname, "../examples/jira.ts"), {
        id: "jira",
        dataDir,
        workspace: { id: "fixture", path: dataDir, mode: "local" },
        config: {
          fixture: true,
          baseUrl: "https://example.atlassian.net",
          allowPosting: true,
        },
      }),
      /separate feature tracked in #42/,
    );
  } finally {
    await rm(dataDir, { recursive: true, force: true });
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
          reads: {
            logs: {
              tool: "search_datadog_logs",
              arguments: { query: "service:fixture" },
              timeRange: { from: "from", to: "to" },
            },
          },
        },
      },
    );
    host.host.execution.exec = async () => {
      throw new Error("remote services must not execute in the workspace");
    };
    host.host.mcp.call = async (server, tool, args) => {
      assert.equal(server, "datadog");
      assert.equal(tool, "search_datadog_logs");
      assert.equal(args.query, "service:fixture");
      assert.equal(typeof args.from, "string");
      queries++;
      return {
        content: [{ type: "text", text: "request failed" }],
        structuredContent: {
          truncated: true,
          data: [{ id: "log-1", attributes: { message: "request failed" } }],
        },
      };
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
    assert.match(
      (await host.snapshot()).presentation.summary,
      /partial investigation/,
    );
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
