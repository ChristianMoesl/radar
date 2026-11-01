import assert from "node:assert/strict";
import test from "node:test";
import { mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { McpAuthenticationError } from "../agent-host/src/mcp.ts";
import { connection, loadSession, observeRevocation } from "./qualify-mcp-lifecycle.mjs";

test("qualification profiles use separate grants and expose no static credentials", () => {
  for (const provider of ["jira", "datadog"]) {
    const c = connection(provider);
    assert.notEqual(c.profile, provider);
    assert.equal(new URL(c.server.url).protocol, "https:");
    assert.deepEqual(Object.keys(c.server), ["url", "oauth"]);
  }
  assert.throws(() => connection("../jira"));
});

test("revocation requires explicit provider rejection and a blocked reopened client", async () => {
  let reads = 0, waits = 0;
  const result = await observeRevocation(async () => {
    reads++;
    if (reads >= 2) throw new McpAuthenticationError("fixture", reads === 2 ? "invalid_grant" : "missing_credentials");
  }, async () => { waits++; });
  assert.deepEqual(result, { rejected: "invalid_grant", reopened: "missing_credentials" });
  assert.equal(reads, 3);
  assert.equal(waits, 1);
});

test("network and missing-credential errors cannot masquerade as provider revocation", async () => {
  for (const error of [new Error("private provider body token"), new McpAuthenticationError("fixture", "missing_credentials")])
    await assert.rejects(observeRevocation(async () => { throw error; }), rejected => {
      assert.match(rejected.message, /unqualified/);
      assert.equal(rejected.message.includes("private provider body token"), false);
      return true;
    });
});

test("accepted access and reopened access never produce a passing revocation report", async () => {
  let reads = 0;
  await assert.rejects(observeRevocation(async () => { reads++; }, async () => {}), /not observed/);
  assert.equal(reads, 36);
  reads = 0;
  await assert.rejects(observeRevocation(async () => {
    if (++reads === 1) throw new McpAuthenticationError("fixture", "invalid_client");
  }), /unexpectedly retained access/);
});

test("revocation selection rejects CI profiles and symlinks to other credential stores", async () => {
  const directory = await mkdtemp(join(tmpdir(), "radar-mcp-lifecycle-test-"));
  try {
    const { profile, server } = connection("jira");
    await mkdir(join(directory, "state", profile), { recursive: true, mode: 0o700 });
    const credential = join(directory, "state", profile, "oauth.json");
    await writeFile(credential, "synthetic-private-state", { mode: 0o600 });
    await writeFile(join(directory, "qualification.json"), JSON.stringify({ version: 1, provider: "jira", profile }), { mode: 0o600 });
    await writeFile(join(directory, "mcp.json"), JSON.stringify({ mcpServers: { [profile]: server } }), { mode: 0o600 });
    assert.equal((await loadSession(directory)).expected.profile, profile);
    await writeFile(join(directory, "qualification.json"), JSON.stringify({ version: 1, provider: "jira", profile: "jira" }), { mode: 0o600 });
    await assert.rejects(loadSession(directory), /never CI-owned/);
    await writeFile(join(directory, "qualification.json"), JSON.stringify({ version: 1, provider: "jira", profile }), { mode: 0o600 });
    await rm(credential);
    await symlink(join(directory, "mcp.json"), credential);
    await assert.rejects(loadSession(directory), /never symlinks/);
  } finally { await rm(directory, { recursive: true, force: true }); }
});
