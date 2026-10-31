import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { McpServices } from "../src/mcp.ts";

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
            const result = await client.call(
              fixture.server,
              fixture.tool,
              fixture.arguments,
              undefined,
              true,
            );
            assert.notEqual(result.isError, true);
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
