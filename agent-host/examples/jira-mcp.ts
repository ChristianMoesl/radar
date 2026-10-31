import type { McpServices } from "../src/mcp.ts";
import type { JsonValue } from "../src/index.ts";

export type JiraConnection = {
  server: string;
  cloudId: string;
  commentsTool?: string;
};

export async function jiraRead(
  mcp: McpServices,
  connection: JiraConnection,
  request: { kind: "issues"; jql: string } | { kind: "comments"; key: string },
  signal?: AbortSignal,
): Promise<JsonValue[]> {
  const bounded = AbortSignal.any([
    AbortSignal.timeout(45000),
    ...(signal ? [signal] : []),
  ]);
  const items: JsonValue[] = [],
    seen = new Set<string>(),
    cursors = new Set<string>();
  let cursor: string | undefined, total: number | undefined;
  for (let page = 0; page < 100; page++) {
    const issues = request.kind === "issues";
    const tool = issues
      ? "searchJiraIssuesUsingJql"
      : (connection.commentsTool ?? "getJiraIssue");
    const args: Record<string, unknown> = issues
      ? {
          cloudId: connection.cloudId,
          jql: request.jql,
          fields: ["summary", "updated", "description", "status"],
          maxResults: 100,
          ...(cursor ? { nextPageToken: cursor } : {}),
        }
      : tool === "getJiraIssue"
        ? {
            cloudId: connection.cloudId,
            issueIdOrKey: request.key,
            fields: ["comment"],
          }
        : {
            cloudId: connection.cloudId,
            issueIdOrKey: request.key,
            startAt: items.length,
            maxResults: 100,
            orderBy: "created",
          };
    const result = (await mcp.json(
      connection.server,
      tool,
      args,
      bounded,
    )) as Record<string, any>;
    if (
      !result ||
      typeof result !== "object" ||
      Array.isArray(result) ||
      result.partial ||
      result.truncated
    )
      throw new Error("Jira MCP returned invalid JSON");
    if (!issues && tool === "getJiraIssue" && result.key !== request.key)
      throw new Error("Jira MCP returned a different issue");
    const data =
      !issues && tool === "getJiraIssue" ? result.fields?.comment : result;
    const rows = issues ? data.issues : data?.comments;
    if (
      !Array.isArray(rows) ||
      rows.length > 100 ||
      data?.errorMessages?.length ||
      data?.errors ||
      data?.truncated ||
      data?.partial
    )
      throw new Error("Jira MCP returned an incomplete or invalid page");
    for (const row of rows) {
      const id = issues ? row.key : row.id;
      if (
        typeof id !== "string" ||
        !id ||
        seen.has(id) ||
        (issues
          ? !/^[A-Z][A-Z0-9_]*-[1-9][0-9]*$/.test(id) ||
            typeof row.fields?.summary !== "string" ||
            typeof row.fields?.updated !== "string" ||
            !row.fields.updated ||
            typeof row.fields?.status?.name !== "string" ||
            !row.fields.status.name
          : typeof row.updated !== "string" ||
            !row.updated ||
            row.body === undefined)
      )
        throw new Error("Jira MCP returned invalid or duplicate records");
      seen.add(id);
      items.push(row);
    }
    if (Buffer.byteLength(JSON.stringify(items)) > 1024 * 1024)
      throw new Error(
        "Jira MCP result exceeds 1 MiB; narrow the configured JQL",
      );
    if (issues) {
      if (data.isLast === true) {
        if (data.nextPageToken)
          throw new Error("Contradictory Jira MCP pagination");
        return items;
      }
      if (
        data.isLast !== false ||
        !rows.length ||
        typeof data.nextPageToken !== "string" ||
        !data.nextPageToken ||
        cursors.has(data.nextPageToken)
      )
        throw new Error("Jira MCP issue completeness could not be established");
      cursor = data.nextPageToken as string;
      cursors.add(cursor);
    } else {
      if (
        !Number.isSafeInteger(data.total) ||
        data.total < items.length ||
        data.startAt !== items.length - rows.length ||
        (total !== undefined && total !== data.total)
      )
        throw new Error("Jira MCP comment totals changed or are missing");
      total = data.total;
      if (items.length === total) return items;
      if (tool === "getJiraIssue")
        throw new Error(
          "Jira MCP returned partial comments; configure commentsTool with a paginated read tool exposed by your MCP server",
        );
      if (!rows.length) throw new Error("Jira MCP comment pagination stalled");
    }
  }
  throw new Error(
    "Jira MCP page limit exceeded; previous discussion state retained",
  );
}
