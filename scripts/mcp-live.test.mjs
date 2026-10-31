import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { jiraSites, liveReads, responseShape } from "./setup-mcp-live.mjs";

test("MCP setup consumes the observed Atlassian v2 resource envelope", () => {
  assert.deepEqual(jiraSites({ data: { resources: [
    { cloudId: "fixture-cloud", url: "https://example.atlassian.net", products: [{ id: "jira", access: "read-only" }] },
    { cloudId: "other", url: "https://example.atlassian.net/wiki", products: [{ id: "confluence" }] },
  ] } }).map(site => site.cloudId), ["fixture-cloud"]);
  assert.throws(() => jiraSites([]), /data.resources/);
});

test("native Datadog read fixtures meet the observed required fields and exclude writes", async () => {
  const configs = [liveReads("fixture-cloud", ["ABC-1", "ABC-2"]),
    JSON.parse(await readFile(new URL("./live-e2e.example.json", import.meta.url), "utf8")).mcp.reads];
  for (const reads of configs) {
    assert.equal(reads.length, 5);
    assert.equal(reads[1].arguments.view, "full");
    const comments = reads.find(read => read.tool === "listJiraIssueComments");
    assert.ok(comments, "the real server exposes a separate paginated comments tool");
    assert.equal(comments.arguments.view, "full");
    assert.equal(comments.arguments.maxResults, 1);
    for (const read of reads.filter(read => read.server === "datadog")) {
      assert.deepEqual(read.arguments.telemetry && Object.keys(read.arguments.telemetry), ["intent"]);
      assert.equal(typeof read.arguments.telemetry.intent, "string");
      if (read.tool === "get_datadog_metric") {
        assert.ok(Array.isArray(read.arguments.queries) && read.arguments.queries.length > 0);
        assert.equal(Object.hasOwn(read.arguments, "query"), false,
          "the real metrics schema rejects singular query as an additional property");
      } else assert.equal(read.tool, "search_datadog_monitors");
    }
  }
});

test("Jira qualification cannot silently use one fixture or injected JQL", () => {
  assert.throws(() => liveReads("fixture-cloud", ["ABC-1"]), /at least two/);
  assert.throws(() => liveReads("fixture-cloud", ["ABC-1", "ABC-1"]), /distinct/);
  assert.throws(() => liveReads("fixture-cloud", ["ABC-1", "ABC-2) OR project = OTHER"]), /approved/);
  assert.equal(liveReads("fixture-cloud", ["ABC-1", "ABC-2"])[0].arguments.jql, "key in (ABC-1,ABC-2)");
});

test("discovery diagnostics retain shapes and keys without provider bodies", () => {
  const shape = responseShape({ data: { key: "ABC-1", fields: { comment: {
    comments: [{ body: "private fixture discussion", author: "private author" }], total: 1,
  } } } });
  assert.deepEqual(shape.keys, ["data"]);
  assert.deepEqual(shape.commentKeys, ["comments", "total"]);
  assert.equal(JSON.stringify(shape).includes("private"), false);
});
