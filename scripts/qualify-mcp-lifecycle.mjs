#!/usr/bin/env node
// Foreground qualification on the computer receiving the loopback callback.
// New disposable grants only; never restore or modify CI-owned sessions.
import assert from "node:assert/strict";
import { lstat, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createInterface } from "node:readline/promises";
import { pathToFileURL } from "node:url";
import { setTimeout as pause } from "node:timers/promises";
import { McpAuthenticationError, McpServices } from "../agent-host/src/mcp.ts";
import { schemaShape } from "../agent-host/test/mcp-contract-shape.ts";

export function connection(provider) {
  assert.ok(["jira", "datadog"].includes(provider), "Select jira or datadog");
  return {
    profile: `radar-qualification-${provider}`,
    server: {
      url: provider === "jira" ? "https://mcp.atlassian.com/v2/mcp" : "https://mcp.datadoghq.eu/v1/mcp",
      oauth: { scope: provider === "jira"
        ? "read:me read:account offline_access read:jira:agent-interface search:jira:agent-interface write:jira:agent-interface"
        : "mcp_all" },
    },
  };
}

export async function observeRevocation(read, wait = pause) {
  for (let attempt = 0; attempt < 36; attempt++) {
    try {
      await read();
    } catch (error) {
      if (!(error instanceof McpAuthenticationError)
          || !["invalid_grant", "invalid_client"].includes(error.reason))
        throw new Error("Revocation is unqualified: the provider did not explicitly reject its saved OAuth grant/client. Network, permission and missing-store failures do not prove revocation.");
      const reason = error.reason;
      // A separate client context must remain blocked after rejected state was
      // invalidated. The callback owns open/close for every invocation.
      try {
        await read();
      } catch (reopened) {
        if (reopened instanceof McpAuthenticationError) return { rejected: reason, reopened: reopened.reason };
        throw new Error("Reopened-client revocation check is unqualified; private diagnostics withheld");
      }
      throw new Error("A reopened client unexpectedly retained access after revocation");
    }
    if (attempt < 35) await wait(5000);
  }
  throw new Error("Revocation was not observed within three minutes; no passing evidence was recorded");
}

async function readCatalog(directory, profile) {
  const client = new McpServices({ config: join(directory, "mcp.json"), state: join(directory, "state") });
  try { return await client.tools(profile); }
  finally { await client.close(); }
}

async function prepare(provider) {
  const { profile, server } = connection(provider);
  const directory = await mkdtemp(join(tmpdir(), "radar-mcp-lifecycle-"));
  const config = join(directory, "mcp.json");
  await writeFile(config, JSON.stringify({ mcpServers: { [profile]: server } }), { mode: 0o600 });
  await writeFile(join(directory, "qualification.json"), JSON.stringify({ version: 1, provider, profile }), { mode: 0o600 });
  console.log(`Create a disposable ${provider} sign-in, separate from CI. OAuth application: Radar MCP client (${profile}).`);
  if (provider === "jira") console.log("Jira read/write scope is needed to inspect the reply contract. This command invokes no comment writer.");
  const client = new McpServices({ config, state: join(directory, "state") });
  let tools;
  try {
    await client.login(profile, url => console.error(`Approve in the browser on this computer:\n${url}`));
    tools = await client.tools(profile);
  } finally { await client.close(); }
  const contracts = tools.filter(tool => /comment|discover|execute/i.test(tool.name)).map(tool => ({
    name: tool.name, readOnly: tool.annotations?.readOnlyHint === true, input: schemaShape(tool.inputSchema),
  }));
  const report = join(directory, "contracts.json");
  await writeFile(report, JSON.stringify({ provider, profile, contracts }, null, 2), { mode: 0o600 });
  console.log("BEGIN QUALIFICATION CONTRACTS\n" + JSON.stringify({ provider, profile, contracts }, null, 2) + "\nEND QUALIFICATION CONTRACTS");
  console.log(`Prepared isolated session: ${directory}\nShare only ${report}; it contains structural schemas, no credentials.`);
  console.log("Keep the private sign-in idle for qualification. Do not seed it into the read-only CI workflow.");
}

export async function loadSession(directory) {
  directory = resolve(directory);
  let manifest, config;
  try {
    manifest = JSON.parse(await readFile(join(directory, "qualification.json"), "utf8"));
    config = JSON.parse(await readFile(join(directory, "mcp.json"), "utf8"));
  } catch { throw new Error("Select the private session directory created by this qualification script"); }
  const expected = connection(manifest.provider);
  assert.ok(manifest.version === 1 && manifest.profile === expected.profile
    && Object.keys(config.mcpServers ?? {}).length === 1
    && JSON.stringify(config.mcpServers[expected.profile]) === JSON.stringify(expected.server),
  "Select an unmodified disposable qualification session, never CI-owned stores");
  for (const path of [directory, join(directory, "mcp.json"), join(directory, "qualification.json"),
    join(directory, "state"), join(directory, "state", expected.profile), join(directory, "state", expected.profile, "oauth.json")]) {
    const info = await lstat(path);
    assert.ok(!info.isSymbolicLink() && (info.isDirectory() || info.isFile()) && (info.mode & 0o077) === 0,
      "Qualification stores must be private regular files/directories, never symlinks to another grant");
  }
  return { directory, manifest, expected };
}

async function verify(directory) {
  const { directory: selected, manifest, expected } = await loadSession(directory);
  directory = selected;
  const read = () => readCatalog(directory, expected.profile);
  await read();
  const terminal = createInterface({ input: process.stdin, output: process.stdout });
  try {
    console.log(`Verified authenticated access. In ${manifest.provider}'s connected-app settings, revoke only Radar MCP client (${expected.profile}).`);
    console.log("If the provider does not identify that exact disposable client, stop instead of revoking another grant.");
    assert.ok((await terminal.question("After revoking that disposable grant, type revoked: ")).trim() === "revoked", "No revocation was confirmed");
  } finally { terminal.close(); }
  const observed = await observeRevocation(read);
  const report = { date: new Date().toISOString(), provider: manifest.provider, profile: expected.profile,
    server: expected.server.url, status: "passed", ...observed,
    scope: "Provider-rejected OAuth grant/client and blocked reopened native MCP client; no browser login or provider writes in verification",
    excluded: ["Collector CLI revocation", "Durable prior-fact retention", "Jira comment write recovery"] };
  const output = join(directory, "revocation.json");
  await writeFile(output, JSON.stringify(report, null, 2), { mode: 0o600 });
  console.log(`Revocation qualified. Share only ${output}.`);
}

async function main() {
  process.umask(0o077);
  const [action, argument] = process.argv.slice(2);
  assert.ok(process.stdin.isTTY && process.stdout.isTTY, "Run foreground qualification in an interactive terminal on the browser's computer");
  if (action === "prepare") await prepare(argument);
  else if (action === "verify-revocation" && argument) await verify(argument);
  else throw new Error("Usage: node --experimental-strip-types scripts/qualify-mcp-lifecycle.mjs prepare jira|datadog | verify-revocation <private-session-directory>");
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href)
  main().catch(error => { console.error(error instanceof McpAuthenticationError ? error.message : "Qualification did not complete: " + error.message); process.exitCode = 1; });
