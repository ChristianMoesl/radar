import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { McpServices, mcpPaths } from "../src/mcp.ts";
import { jiraRead } from "../examples/jira-mcp.ts";
import { schemaShape } from "./mcp-contract-shape.ts";

async function oauthExpiry(server: string): Promise<number | undefined> {
  assert.match(server, /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/);
  let value: string;
  try {
    value = await readFile(join(mcpPaths().state, server, "oauth.json"), "utf8");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw new Error("Cannot read live MCP expiry metadata; private diagnostics withheld");
  }
  try {
    assert.ok(Buffer.byteLength(value) <= 1024 * 1024);
    const expiry: unknown = JSON.parse(value).tokensExpireAt;
    return typeof expiry === "number" && Number.isFinite(expiry) && expiry > 0 ? expiry : undefined;
  } catch {
    throw new Error("Invalid live MCP expiry metadata; private diagnostics withheld");
  }
}

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
    // Observe only expiry metadata, before any provider call. Never alter a
    // timestamp to manufacture expiry, and never print token/client state.
    const expired = new Map<string, boolean>();
    for (const server of new Set(fixtures.map(fixture => fixture.server))) {
      const expiry = await oauthExpiry(server);
      expired.set(server, expiry !== undefined && expiry <= Date.now());
    }
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
    for (const [server, observed] of expired) {
      await context.test(`${server} / natural OAuth expiry refresh`, {
        skip: observed ? false : "Access token was not observably expired before this run",
      }, async () => {
        const renewed = await oauthExpiry(server);
        assert.ok(renewed !== undefined && renewed > Date.now(),
          "Successful native reads must persist renewed OAuth expiry after natural expiration");
      });
    }
    const jira = fixtures.find(fixture => fixture.tool === "listJiraIssueComments");
    if (jira) await context.test("Native Jira comment contract discovery (no writes)", async () => {
      const client = new McpServices();
      try {
        const tools = (await client.tools(jira.server, context.signal)).filter(tool => /comment/i.test(tool.name));
        console.log("Jira comment contracts: " + JSON.stringify(tools.map(tool => ({
          name: tool.name,
          readOnly: tool.annotations?.readOnlyHint === true,
          input: schemaShape(tool.inputSchema),
        }))));
      } finally {
        await client.close();
      }
    });
  },
);
