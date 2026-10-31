import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { McpServices } from "../src/mcp.ts";
import { jiraRead } from "../examples/jira-mcp.ts";

// Select only approved, non-secret native MCP read arguments. Authentication
// remains with the host MCP client/server, independently of collector CLIs.
test(
  "Live host MCP service reads and reconnect",
  {
    skip: !process.env.RADAR_TEST_MCP_FIXTURES,
    timeout: 180000,
  },
  async (context) => {
    const fixtures = JSON.parse(
      await readFile(process.env.RADAR_TEST_MCP_FIXTURES!, "utf8"),
    ) as {
      server: string;
      tool: string;
      arguments: Record<string, unknown>;
    }[];
    assert.ok(Array.isArray(fixtures) && fixtures.length > 0);
    const selectedIssues = new Map<string, string[]>();
    for (const fixture of fixtures) {
      await context.test(`${fixture.server} / ${fixture.tool}`, async () => {
        for (let attempt = 0; attempt < 2; attempt++) {
          const client = new McpServices();
          try {
            const args = fixture.arguments;
            if (fixture.tool === "getJiraIssue") {
              const result = await client.json(fixture.server, fixture.tool, args, context.signal) as {
                partial?: boolean; truncated?: boolean;
                data?: { key?: string; fields?: { summary?: string }; partial?: boolean; truncated?: boolean };
              };
              assert.notEqual(result.partial, true);
              assert.notEqual(result.truncated, true);
              assert.equal(result.data?.key, args.issueIdOrKey);
              assert.equal(typeof result.data?.fields?.summary, "string");
              assert.notEqual(result.data?.partial, true);
              assert.notEqual(result.data?.truncated, true);
              continue;
            }
            if (
              ["searchJiraIssuesUsingJql", "listJiraIssueComments"].includes(
                fixture.tool,
              )
            ) {
              assert.equal(typeof args.cloudId, "string");
              assert.equal(
                typeof args[
                  fixture.tool === "listJiraIssueComments" ? "issueIdOrKey" : "jql"
                ],
                "string",
              );
              // Exercise the definition's completeness checks, not just a
              // successful MCP request. Force one-row issue/comment pages.
              const json = client.json.bind(client);
              client.json = (server, tool, args, ...rest) =>
                json(
                  server,
                  tool,
                  ["searchJiraIssuesUsingJql", "listJiraIssueComments"].includes(tool)
                    ? { ...args, maxResults: 1 }
                    : args,
                  ...rest,
                );
              const scope = `${fixture.server}/${String(args.cloudId)}`;
              if (fixture.tool === "listJiraIssueComments") {
                const keys = selectedIssues.get(scope) ?? [String(args.issueIdOrKey)];
                let longest = 0;
                for (const key of keys) {
                  const comments = await jiraRead(client,
                    { server: fixture.server, cloudId: String(args.cloudId) },
                    { kind: "comments", key }, context.signal);
                  longest = Math.max(longest, comments.length);
                }
                console.log(`Jira comment coverage: ${keys.length} designated issues, largest discussion ${longest} comments`);
                assert.ok(longest >= 2,
                  "Live comment pagination needs a designated issue with at least two existing comments; empty discussions do not establish continuation");
                continue;
              }
              const rows = await jiraRead(
                client,
                { server: fixture.server, cloudId: String(args.cloudId) },
                { kind: "issues", jql: String(args.jql) },
                context.signal,
              );
              assert.ok(rows.length >= 2, "Live issue pagination needs at least two designated issues");
              selectedIssues.set(scope, (rows as unknown as { key: string }[]).map(issue => issue.key));
              continue;
            }
            const result = await client.call(
              fixture.server,
              fixture.tool,
              fixture.arguments,
              context.signal,
              true,
            );
            assert.notEqual(result.isError, true);
            const metadata = result as {
              _meta?: { partial?: boolean; truncated?: boolean };
              structuredContent?: { partial?: boolean; truncated?: boolean };
            };
            for (const flags of [metadata._meta, metadata.structuredContent]) {
              assert.notEqual(
                flags?.partial,
                true,
                "live qualification requires complete evidence",
              );
              assert.notEqual(
                flags?.truncated,
                true,
                "live qualification requires complete evidence",
              );
            }
            assert.ok(
              result.structuredContent !== undefined || result.content?.length,
              "a live MCP read must return evidence; success alone does not establish ingestion",
            );
          } finally {
            await client.close();
          }
        }
      });
    }
  },
);
