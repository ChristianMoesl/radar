import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { WorkspaceExecution } from "../src/environment.ts";
import { twgRead } from "../examples/twg.ts";

test(
  "Live Jira issue and comment reads through the selected workspace",
  {
    skip: !process.env.RADAR_TEST_JIRA_WORKSPACE,
    timeout: 150000,
  },
  async (context) => {
    const selectors = process.env.RADAR_TEST_JIRA_FIXTURES;
    assert.ok(selectors, "select approved non-secret Jira fixtures");
    const fixture = JSON.parse(await readFile(selectors, "utf8"));
    const mode = process.env.RADAR_TEST_JIRA_EXECUTION ?? "local";
    assert.ok(mode === "local" || mode === "sbx");
    const execution = new WorkspaceExecution({
      id: "twg-live",
      path: process.env.RADAR_TEST_JIRA_WORKSPACE!,
      mode,
    });
    const connection = {
      baseUrl: fixture.base_url,
      cloudId: fixture.cloud_id,
      profile: fixture.profile,
      accountId: fixture.account_id,
    };
    try {
      const keys: string[] = fixture.issue_keys;
      assert.ok(
        keys.length >= 2 &&
          keys.every((key) => /^[A-Z][A-Z0-9_]*-[1-9][0-9]*$/.test(key)),
      );
      const issues = (await twgRead(execution, connection, {
        kind: "issues",
        jql: `key IN (${keys.join(",")}) ORDER BY key ASC`,
      })) as { key: string }[];
      assert.deepEqual(
        issues.map((issue) => issue.key).sort(),
        [...keys].sort(),
      );
      let comments = 0;
      for (const key of keys)
        comments += (
          await twgRead(execution, connection, { kind: "comments", key })
        ).length;
      context.diagnostic(
        `${issues.length} approved issues and ${comments} comments through ${mode} workspace execution`,
      );
    } finally {
      execution.close();
    }
  },
);
