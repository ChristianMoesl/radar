import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { McpServices } from "../src/mcp.ts";
import { jiraRead } from "../examples/jira-mcp.ts";
import { datadogRead, validateDatadogReads } from "../examples/datadog-mcp.ts";

// Only the loopback fixture bypasses the external-network proxy.
process.env.NO_PROXY = [process.env.NO_PROXY, "127.0.0.1", "localhost"]
  .filter(Boolean)
  .join(",");
process.env.no_proxy = process.env.NO_PROXY;

async function fixture(oauth = false) {
  const directory = await mkdtemp(join(tmpdir(), "radar-mcp-"));
  let origin = "",
    token = "fixture-access-0",
    refreshes = 0,
    registrations = 0,
    calls = 0,
    expire = false,
    forbidden = false,
    toolError = false,
    partial = false;
  const server = createServer(async (request, response) => {
    const send = (value: unknown, status = 200) => {
      response.writeHead(status, { "content-type": "application/json" });
      response.end(JSON.stringify(value));
    };
    let body = "";
    for await (const chunk of request) body += chunk;
    if (request.url?.startsWith("/.well-known/oauth-protected-resource"))
      return send({
        resource: `${origin}/mcp`,
        authorization_servers: [origin],
        scopes_supported: ["read"],
      });
    if (request.url === "/.well-known/oauth-authorization-server")
      return send({
        issuer: origin,
        authorization_endpoint: `${origin}/authorize`,
        token_endpoint: `${origin}/token`,
        registration_endpoint: `${origin}/register`,
        response_types_supported: ["code"],
        grant_types_supported: ["authorization_code", "refresh_token"],
        code_challenge_methods_supported: ["S256"],
        token_endpoint_auth_methods_supported: ["none"],
      });
    if (request.url === "/register") {
      registrations++;
      return send(
        { ...JSON.parse(body), client_id: "fixture-public-client" },
        201,
      );
    }
    if (request.url === "/token") {
      if (new URLSearchParams(body).get("grant_type") === "refresh_token") {
        refreshes++;
        token = `fixture-access-${refreshes}`;
        expire = false;
      }
      return send({
        access_token: token,
        refresh_token: `fixture-refresh-${refreshes}`,
        token_type: "Bearer",
        expires_in: 3600,
        scope: "read",
      });
    }
    if (request.url !== "/mcp") return send({}, 404);
    if (request.method === "DELETE") {
      response.writeHead(204);
      response.end();
      return;
    }
    if (request.method === "GET") return send({}, 405);
    if (
      oauth &&
      (expire || request.headers.authorization !== `Bearer ${token}`)
    ) {
      response.writeHead(401, {
        "content-type": "application/json",
        "www-authenticate": `Bearer resource_metadata="${origin}/.well-known/oauth-protected-resource"`,
      });
      response.end(JSON.stringify({ secret: "private provider error" }));
      return;
    }
    if (forbidden) return send({ secret: "private forbidden body" }, 403);
    const message = JSON.parse(body);
    if (message.id === undefined) {
      response.writeHead(202);
      response.end();
      return;
    }
    let result: unknown;
    switch (message.method) {
      case "initialize":
        result = {
          protocolVersion: "2025-11-25",
          capabilities: { tools: {} },
          serverInfo: { name: "fixture", version: "1" },
        };
        break;
      case "tools/list":
        result = message.params?.cursor
          ? {
              tools: [
                {
                  name: "second",
                  inputSchema: { type: "object", properties: {} },
                },
              ],
            }
          : {
              tools: [
                {
                  name: "read",
                  inputSchema: { type: "object", properties: {} },
                  annotations: { readOnlyHint: true },
                },
              ],
              nextCursor: "page-two",
            };
        break;
      case "tools/call":
        calls++;
        result = {
          content: [{ type: "text", text: "bounded evidence" }],
          structuredContent: { value: message.params.arguments.value },
          ...(toolError ? { isError: true } : {}),
          ...(partial ? { _meta: { partial: true } } : {}),
        };
        break;
      default:
        return send({
          jsonrpc: "2.0",
          id: message.id,
          error: { code: -32601, message: "unsupported" },
        });
    }
    send({ jsonrpc: "2.0", id: message.id, result });
  });
  await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
  origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
  const config = join(directory, "mcp.json");
  await writeFile(
    config,
    JSON.stringify({
      mcpServers: {
        fixture: { url: `${origin}/mcp`, oauth: { callbackPort: 0 } },
      },
    }),
  );
  const paths = { config, state: join(directory, "state") };
  const clients: McpServices[] = [];
  return {
    paths,
    client: () => {
      const client = new McpServices(paths);
      clients.push(client);
      return client;
    },
    expire: () => {
      expire = true;
    },
    forbid: () => {
      forbidden = true;
    },
    failTool: () => {
      toolError = true;
    },
    partial: () => {
      partial = true;
    },
    counters: () => ({ calls, refreshes, registrations }),
    close: async () => {
      await Promise.all(clients.map((c) => c.close()));
      server.closeAllConnections();
      await new Promise<void>((done) => server.close(() => done()));
      await rm(directory, { recursive: true, force: true });
    },
  };
}

test("MCP negotiates HTTP, follows tool pages, bounds calls and redacts tool/server errors", async () => {
  const f = await fixture();
  try {
    const client = f.client();
    assert.deepEqual(
      (await client.tools("fixture")).map((t) => t.name),
      ["read", "second"],
    );
    assert.deepEqual(
      await client.json("fixture", "read", { value: "evidence" }),
      { value: "evidence" },
    );
    const before = f.counters().calls;
    await assert.rejects(
      client.call("fixture", "second", {}, undefined, true),
      /MCP fixture request failed/,
    );
    await assert.rejects(
      client.call("fixture", "unknown", {}),
      /MCP fixture request failed/,
    );
    assert.equal(f.counters().calls, before);
    f.partial();
    await assert.rejects(client.json("fixture", "read", {}), /partial JSON/);
    f.failTool();
    await assert.rejects(
      client.call("fixture", "read", {}),
      /MCP fixture request failed/,
    );
    f.forbid();
    await assert.rejects(
      client.tools("fixture"),
      (error) => !String(error).includes("private forbidden"),
    );
  } finally {
    await f.close();
  }
});

test("background MCP auth failure does not start registration or browser login", async () => {
  const f = await fixture(true);
  try {
    await assert.rejects(
      f.client().tools("fixture"),
      /radar-mcp login fixture/,
    );
    assert.equal(f.counters().registrations, 0);
  } finally {
    await f.close();
  }
});

test("foreground MCP OAuth persists, refreshes concurrently and survives a new agent process context", async () => {
  const f = await fixture(true);
  try {
    const client = f.client();
    let callback: Promise<Response> | undefined;
    await client.login("fixture", (url) => {
      const redirect = new URL(url.searchParams.get("redirect_uri")!);
      redirect.searchParams.set("state", url.searchParams.get("state")!);
      redirect.searchParams.set("code", "fixture-one-time-code");
      callback = fetch(redirect);
    });
    await callback;
    assert.deepEqual(
      await client.json("fixture", "read", { value: "before" }),
      { value: "before" },
    );
    await client.close();
    const first = f.client(),
      second = f.client();
    f.expire();
    const values = await Promise.all([
      first.json("fixture", "read", { value: "one" }),
      second.json("fixture", "read", { value: "two" }),
    ]);
    assert.deepEqual(values, [{ value: "one" }, { value: "two" }]);
    assert.equal(
      f.counters().refreshes,
      1,
      "profile locking must prevent stale rotating-token refresh",
    );
    const state = await readFile(
      join(f.paths.state, "fixture/oauth.json"),
      "utf8",
    );
    assert.ok(state.includes("fixture-access-1")); // Synthetic fixture only.
  } finally {
    await f.close();
  }
});

test("Jira MCP paginates issues and configured comment reads, rejects incomplete data", async () => {
  const client = new McpServices();
  try {
    const requests: Record<string, unknown>[] = [];
    client.json = async (_server, tool, args) => {
      requests.push(args);
      if (tool === "searchJiraIssuesUsingJql")
        return args.nextPageToken
          ? {
              isLast: true,
              issues: [
                {
                  key: "ABC-2",
                  fields: {
                    summary: "Second",
                    updated: "2",
                    status: { name: "Open" },
                  },
                },
              ],
            }
          : {
              isLast: false,
              nextPageToken: "next",
              issues: [
                {
                  key: "ABC-1",
                  fields: {
                    summary: "First",
                    updated: "1",
                    status: { name: "Open" },
                  },
                },
              ],
            };
      return {
        startAt: args.startAt,
        total: 2,
        comments: [
          {
            id: String(Number(args.startAt) + 1),
            updated: "1",
            body: "comment",
          },
        ],
      };
    };
    assert.equal(
      (
        await jiraRead(
          client,
          { server: "jira", cloudId: "fixture" },
          { kind: "issues", jql: "project = ABC" },
        )
      ).length,
      2,
    );
    assert.equal(
      (
        await jiraRead(
          client,
          {
            server: "jira",
            cloudId: "fixture",
            commentsTool: "paginated_comments",
          },
          { kind: "comments", key: "ABC-1" },
        )
      ).length,
      2,
    );
    client.json = async () => ({
      key: "ABC-1",
      fields: {
        comment: {
          startAt: 0,
          total: 2,
          comments: [{ id: "1", updated: "1", body: "partial" }],
        },
      },
    });
    await assert.rejects(
      jiraRead(
        client,
        { server: "jira", cloudId: "fixture" },
        { kind: "comments", key: "ABC-1" },
      ),
      /partial comments/,
    );
  } finally {
    await client.close();
  }
});

test("Datadog MCP fixes read scope, binds native time fields and rejects writes", async () => {
  const client = new McpServices();
  try {
    client.call = async (server, tool, args) => {
      assert.equal(server, "datadog");
      assert.equal(tool, "get_datadog_metric");
      assert.deepEqual(args, {
        query: "avg:fixture.metric{service:api}",
        start: "2026-01-01T00:00:00Z",
        end: "2026-01-01T00:15:00Z",
      });
      return {
        content: [{ type: "text", text: "sampled metric" }],
        structuredContent: { truncated: true },
      };
    };
    const result = await datadogRead(
      client,
      "datadog",
      {
        tool: "get_datadog_metric",
        arguments: { query: "avg:fixture.metric{service:api}" },
        timeRange: { from: "start", to: "end" },
      },
      "2026-01-01T00:00:00Z",
      "2026-01-01T00:15:00Z",
    );
    assert.equal((result as any).structuredContent.truncated, true);
    assert.throws(
      () =>
        validateDatadogReads({
          write: { tool: "delete_datadog_notebook", arguments: {} },
        }),
      /reads only/,
    );
    assert.throws(
      () =>
        validateDatadogReads({
          scope: {
            tool: "get_datadog_metric",
            arguments: { query: "service:api" },
            timeRange: { from: "query", to: "to" },
          },
        }),
      /reads only/,
      "a model-controlled timestamp cannot overwrite the configured query scope",
    );
  } finally {
    await client.close();
  }
});
