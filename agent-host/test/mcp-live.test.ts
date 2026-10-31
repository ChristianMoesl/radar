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
    for (const fixture of fixtures) {
      await context.test(`${fixture.server} / ${fixture.tool}`, async () => {
        for (let attempt = 0; attempt < 2; attempt++) {
          const client = new McpServices();
          try {
            const args = fixture.arguments;
            if (
              ["searchJiraIssuesUsingJql", "getJiraIssue"].includes(
                fixture.tool,
              )
            ) {
              assert.equal(typeof args.cloudId, "string");
              assert.equal(
                typeof args[
                  fixture.tool === "getJiraIssue" ? "issueIdOrKey" : "jql"
                ],
                "string",
              );
              // Exercise the definition's completeness checks, not just a
              // successful MCP request. Force real issue cursor continuation.
              const json = client.json.bind(client);
              client.json = (server, tool, args, ...rest) =>
                json(
                  server,
                  tool,
                  tool === "searchJiraIssuesUsingJql"
                    ? { ...args, maxResults: 1 }
                    : args,
                  ...rest,
                );
              const request =
                fixture.tool === "getJiraIssue"
                  ? {
                      kind: "comments" as const,
                      key: String(args.issueIdOrKey),
                    }
                  : { kind: "issues" as const, jql: String(args.jql) };
              const rows = await jiraRead(
                client,
                { server: fixture.server, cloudId: String(args.cloudId) },
                request,
                context.signal,
              );
              if (request.kind === "issues")
                assert.ok(
                  rows.length > 0,
                  "approved Jira fixtures must return issues",
                );
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
