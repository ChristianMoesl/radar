#!/usr/bin/env node
// Local foreground setup. Credentials remain in client-owned stores or go
// directly through gh's encrypted secret upload; none are printed.
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtemp, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { createInterface } from "node:readline/promises";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
const intent = "Qualify the Radar read-only MCP integration against designated test data";

export function liveReads(cloudId, keys) {
  assert.equal(typeof cloudId, "string", "Select a Jira Cloud ID");
  assert.ok(cloudId.length > 0, "Select a Jira Cloud ID");
  assert.ok(Array.isArray(keys) && keys.length >= 2 && new Set(keys).size === keys.length
    && keys.every(key => /^[A-Z][A-Z0-9_]*-[1-9][0-9]*$/.test(key)),
  "Select at least two distinct approved Jira issue keys for real cursor continuation");
  return [
    { server: "jira", tool: "searchJiraIssuesUsingJql", arguments: {
      cloudId, jql: `key in (${keys.join(",")})`, maxResults: 1,
    } },
    { server: "jira", tool: "getJiraIssue", arguments: {
      cloudId, issueIdOrKey: keys[0], view: "full",
    } },
    { server: "jira", tool: "listJiraIssueComments", arguments: {
      cloudId, issueIdOrKey: keys[0], startAt: 0, maxResults: 1, orderBy: "created", view: "full",
    } },
    { server: "datadog", tool: "search_datadog_monitors", arguments: {
      query: "tag:radar-oauth-e2e", telemetry: { intent },
    } },
    { server: "datadog", tool: "get_datadog_metric", arguments: {
      queries: ["avg:datadog.apis.rate_limit.usage.per_org_pct{*}"],
      from: "now-15m", to: "now", telemetry: { intent },
    } },
  ];
}

export function jiraSites(response) {
  const sites = response?.data?.resources;
  assert.ok(Array.isArray(sites), "Atlassian resource discovery must return data.resources");
  return sites.filter(site => typeof site.cloudId === "string" && typeof site.url === "string"
    && site.products?.some(product => product.id === "jira"));
}

export function responseShape(response) {
  const value = response?.data ?? response;
  return {
    keys: Object.keys(response ?? {}), dataKeys: Object.keys(value ?? {}),
    issueKeys: Array.isArray(value?.issues) ? value.issues.map(issue => issue.key) : [],
    fields: Object.keys(value?.fields ?? {}),
    commentKeys: Object.keys(value?.fields?.comment ?? {}),
    commentPage: Array.isArray(value?.comments)
      ? { startAt: value.startAt, total: value.total, count: value.comments.length,
          rowKeys: Object.keys(value.comments[0] ?? {}) }
      : undefined,
  };
}

function command(file, args, input) {
  try {
    return execFileSync(file, args, { cwd: root, input, encoding: "utf8", timeout: 60000,
      stdio: [input === undefined ? "ignore" : "pipe", "pipe", "pipe"] }).trim();
  } catch (error) {
    const status = /HTTP [1-5][0-9]{2}/.exec(String(error.stderr ?? ""))?.[0];
    throw new Error(`${file} ${args.slice(0, 2).join(" ")} failed${status ? ` (${status})` : ""}; check local authentication and repository permissions`);
  }
}

async function main() {
  const { McpServices, mcpPaths } = await import("../agent-host/src/mcp.ts");
  const paths = mcpPaths();
  const client = new McpServices(paths);
  const terminal = createInterface({ input: process.stdin, output: process.stdout });
  const directory = await mkdtemp(join(dirname(resolve(paths.config)), "mcp-live-"));
  let reads;
  try {
    const sites = jiraSites(await client.json("jira", "getAccessibleAtlassianResources", {}));
    assert.ok(sites.length, "No Jira sites are accessible through this MCP sign-in");
    let site = sites[0];
    if (sites.length !== 1) {
      console.log(sites.map(candidate => candidate.url).join("\n"));
      const url = (await terminal.question("Jira test site URL: ")).trim().replace(/\/$/, "");
      site = sites.find(candidate => candidate.url.replace(/\/$/, "") === url);
      assert.ok(site, "Select one of the accessible Jira sites");
    }
    console.log(`Discovering designated test issues on ${site.url}`);
    const found = await client.json("jira", "searchJiraIssuesUsingJql", {
      cloudId: site.cloudId,
      jql: `text ~ ${JSON.stringify('"Radar OAuth E2E"')} ORDER BY key`,
      fields: ["summary", "updated", "status"], maxResults: 20, view: "full",
    });
    // Discovery records field names and issue keys only, never issue bodies.
    const discovery = responseShape(found);
    let keys = discovery.issueKeys;
    if (keys.length < 2) {
      keys = (await terminal.question("At least two approved Jira test issue keys, comma-separated: "))
        .split(",").map(key => key.trim());
    }
    reads = liveReads(site.cloudId, keys);
    const issue = await client.json("jira", "listJiraIssueComments", reads[2].arguments);
    discovery.comments = responseShape(issue);
    await writeFile(join(directory, "discovery.json"), JSON.stringify(discovery, null, 2), { mode: 0o600 });
    console.log(`Sanitized discovery: ${JSON.stringify(discovery)}`);
    await writeFile(join(directory, "reads.json"), JSON.stringify(reads), { mode: 0o600 });
    await writeFile(join(directory, "config.json"), JSON.stringify({ mcp: {
      config_file: resolve(paths.config), state_dir: resolve(paths.state), reads,
    } }), { mode: 0o600 });
  } finally {
    terminal.close();
    await client.close();
  }

  const tested = spawnSync(process.execPath,
    ["--experimental-strip-types", "--test", "agent-host/test/mcp-live.test.ts"], {
      cwd: root, stdio: "inherit", timeout: 240000,
      env: { ...process.env, RADAR_MCP_CONFIG: resolve(paths.config),
        RADAR_MCP_STATE_DIR: resolve(paths.state), RADAR_TEST_MCP_FIXTURES: join(directory, "reads.json") },
    });
  assert.equal(tested.status, 0, `Local MCP qualification failed; no CI sessions were uploaded. Sanitized discovery: ${join(directory, "discovery.json")}`);

  const repository = command("gh", ["repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner"]);
  const names = JSON.parse(command("gh", ["secret", "list", "--repo", repository,
    "--env", "live-e2e-main", "--json", "name"]));
  if (!names.some(secret => secret.name === "LIVE_E2E_STATE_WRITER_TOKEN")) {
    console.log("Create a fine-grained GitHub token for this repository with Environments: read and write:");
    console.log("https://github.com/settings/personal-access-tokens/new");
    console.log("Paste it only into the following local gh prompt. CI needs it to save rotated OAuth sessions.");
    const saved = spawnSync("gh", ["secret", "set", "LIVE_E2E_STATE_WRITER_TOKEN",
      "--repo", repository, "--env", "live-e2e-main"], { cwd: root, stdio: "inherit" });
    assert.equal(saved.status, 0, "The session writer secret was not configured; CI remains disabled");
  }
  command("python3", ["scripts/live-e2e-state.py", "seed", "--mcp-only",
    "--config", join(directory, "config.json"), "--repository", repository]);
  command("gh", ["variable", "set", "RADAR_MCP_LIVE_READS", "--repo", repository], JSON.stringify(reads));
  command("gh", ["variable", "set", "RADAR_MCP_LIVE_ENABLED", "--repo", repository, "--body", "true"]);
  command("gh", ["workflow", "run", "mcp-live.yml", "--repo", repository, "--ref", "main"]);
  console.log("Live agent MCP CI dispatched. Its dedicated OAuth sessions now belong to CI; keep these local stores idle.");
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.umask(0o077);
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
