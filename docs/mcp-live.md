# Live agent MCP qualification

The [Live agent MCP workflow](../.github/workflows/mcp-live.yml) qualifies Jira
and Datadog agents independently of the host collectors. It needs the two
dedicated MCP sign-ins, without TWG/Pup stores, Terraform credentials, a GitHub
fixture token, or an existing live-test selector file. The full cross-provider
[collector workflow](live-e2e.md) remains separate.

Use Node 24.5+ and the current repository dependencies. With the dedicated
`RADAR_MCP_CONFIG` and `RADAR_MCP_STATE_DIR` selected, authenticate through
`radar-mcp login jira` and `radar-mcp login datadog` on the computer whose browser
receives the loopback callback. Then run from the repository:

```sh
node --experimental-strip-types scripts/setup-mcp-live.mjs
```

The setup command discovers Jira sites through the observed Atlassian v2
`data.resources` contract. With multiple sites, it asks which test site to use.
It searches for designated `Radar OAuth E2E` fixtures; if it cannot find at
least two, it asks for approved issue keys. No issue, comment or monitor is
created or changed. The resulting selectors and a report containing field names
and fixture keys are saved privately next to the selected MCP configuration.
An existing selector file is not needed or overwritten.

Local qualification must pass before setup uploads any CI sessions. Jira reads
exercise the agent's complete issue/comment reader and one-row issue/comment
pages. `getJiraIssue` qualifies full-view issue details separately;
`listJiraIssueComments` supplies the discussion instead of the issue's compact
field projection.
At least two designated issues and one discussion with at least two existing
comments are required to prove real continuation. Comment checks read the issues
selected by the approved issue fixture. Empty discussions are read correctly,
but cannot qualify live pagination. The harness never creates comments to fill
this prerequisite; missing test data fails with an explicit setup error.
The native Datadog metrics tool requires `queries` and `telemetry.intent`;
monitor reads also require `telemetry.intent`. The fixed metric is the built-in
API usage metric, and monitor reads select the designated test tag. No logs,
traces or writing tools enter this gate. Missing comments, incomplete responses,
denied permissions and unavailable tools fail qualification.

## GitHub setup

Reuse `live-e2e-main`, with the trusted-main branch restrictions documented in
[live provider setup](live-e2e.md#one-time-github-setup). The workflow verifies
those restrictions and the exact trusted revision before requesting credentials.
It never runs PR code with these sessions.

If `LIVE_E2E_STATE_WRITER_TOKEN` is absent, setup asks for a fine-grained GitHub
token restricted to this repository with **Environments: read and write**. Create
it in GitHub and enter its value only into the local `gh secret set` prompt.
Never put a token or callback code into chat. The workflow's normal
`GITHUB_TOKEN` cannot update environment secrets.

Setup then saves the opaque MCP client stores directly through GitHub's encrypted
secret upload as `MCP_LIVE_SESSIONS`, writes the non-secret repository variables
`RADAR_MCP_LIVE_READS` and `RADAR_MCP_LIVE_ENABLED`, and dispatches `mcp-live.yml`.
No credential values or session bundle are printed. `TFCTL_TOKEN` is unrelated
to this setup. A failed local read does not enable or dispatch CI.

## Session ownership and results

After setup enables it, the workflow runs on relevant MCP changes pushed to
trusted `main`, daily, or through an explicit main dispatch.
The workflow runs Linux amd64, Linux arm64 and macOS arm64 sequentially. Before
provider requests, each runner checkpoints the unchanged session bundle using
the writer token. It saves rotated sessions after both passing and failing
tests. Workflow runs do not cancel each other. The next platform loads the
session saved by its predecessor.

These dedicated OAuth stores belong exclusively to this workflow after seeding.
Keep their local originals idle, and use a separate grant for interactive agents
or the full collector workflow. This avoids spending the same rotating refresh
token from two copies. SQLite profile locks are not transported. `--mcp-only`
on the shared storage helper accepts only the two selected remote OAuth profiles
and restores only MCP state; full collector and MCP-only bundles cannot be
restored in each other's scope. Existing full-bundle behavior is retained.

Only sanitized JUnit results are uploaded, for 14 days. Runner credential files
are removed after the job. Interrupted writeback may require reauthentication
and explicit reseeding. No source store is deleted or remote grant revoked.

A successful run proves native read contracts, completeness and reconnect on
the selected platforms. The harness also records whether each access token was
already naturally expired before the first read. In that case a separate check
requires a renewed future expiry to be saved after the reads/reconnect. It never
changes timestamps or prints client/token state. If no expired token was
observed, that check is skipped and natural expiry remains unqualified; a green
read matrix alone does not establish refresh. Server-side revocation, other
browser onboarding flows and Jira reply posting remain separate gates.
The [2026-10-31 evidence](qualification/mcp-reads-2026-10-31.json) records the
passing three-platform read matrix and its exact limits.
