# Host MCP connections for agents

This guide describes host MCP work added on `main` after v0.7.0. The collector
OAuth cutover already shipped in [v0.7.0](https://github.com/ChristianMoesl/radar/releases/tag/v0.7.0);
that tag does not include this later agent migration.

Remote service tools use MCP on the host. Workspace files, Git operations,
builds and tests still use `host.execution` and the workspace's configured SBX
boundary. Jira, Datadog and Renovate examples use `host.mcp`; no service command
or provider credential is sent into a sandbox. The default image no longer
installs Pup or TWG. It does not add tfctl or Terraform. GitHub CLI remains an
inherited base-image utility and is not the agent service transport.

Radar's background collectors are separate: their existing host-side TWG, Pup,
tfctl and gh transports keep their current authentication and behavior.

## Configure connections once on the host

The agent-host package provides `radar-mcp` and the shared `McpServices` client,
using `@earendil-works/pi-mcp` 1.3.0. Install/build the package as described in
[durable agents](durable-agents.md). Node 24 is required; using an HTTP proxy
requires Node 24.5+ so native HTTP and fetch honor its proxy environment.

Copy [the profile example](../agent-host/examples/mcp.example.json) to
`~/.config/radar/mcp.json`, retaining only the connections you use. Override the
file with an absolute `RADAR_MCP_CONFIG` path when needed. The file is trusted
host configuration: stdio commands are installed host MCP servers, not sandbox
commands. No package is downloaded implicitly. Their stderr is captured privately.
Run the commands below with the definition project's `node_modules/.bin` on
PATH, or invoke its `radar-mcp` binary directly. Restart Radar after changing the
daemon's inherited `RADAR_MCP_CONFIG`/`RADAR_MCP_STATE_DIR` environment.

The example selects the official Datadog EU and Atlassian remote servers, and
installed host stdio servers for Terraform and GitHub:

- [Datadog setup](https://github.com/DataDog/documentation/blob/master/hugo/content/en/mcp_server/setup.md): select the endpoint for your region; the example is EU.
- [Atlassian MCP](https://github.com/atlassian/atlassian-mcp-server): `/v2/mcp` is the recommended endpoint.
- [Terraform MCP server](https://github.com/hashicorp/terraform-mcp-server): install on the host and configure its own `TFE_ADDRESS`/`TFE_TOKEN` requirements there. This server's authentication is distinct from Radar's collector tfctl profile.
- [GitHub MCP server](https://github.com/github/github-mcp-server): install and authenticate on the host. Use the minimum read/comment permissions needed by the selected definition.

For remote servers, explicitly authenticate in your host terminal:

```sh
radar-mcp login datadog
radar-mcp login jira
radar-mcp tools datadog
radar-mcp tools jira
```

The login command prints the OAuth authorization URL; open it in your browser.
The loopback callback listens on `127.0.0.1:8765` on that machine. Optional profile
`oauth.clientId`, `oauth.scope` and `oauth.callbackPort` select registration,
scope and port when the server requires them. Remote hosts need access to that
loopback callback through their normal secure terminal/port-forwarding setup.
Do not send a callback code or tokens through agent messages or workflow inputs.
Stdio servers own their own authentication; `radar-mcp login` never invents an
OAuth flow for them. `radar-mcp tools terraform` checks their real connection.

The MCP client owns its separate OAuth state under
`~/.local/state/radar/mcp/<profile>/oauth.json`, in a private directory with
0600 credential files. `RADAR_MCP_STATE_DIR` selects another host directory.
The Pi MCP library owns discovery, PKCE, registration and refresh; no Pup/TWG/gh
credential database is read or copied. Agent runners serialize the whole profile
request across processes, including refresh, using a token-free SQLite lock.
Process death releases the lock. Each call opens a fresh MCP session, so a later
call or agent restart reuses current persisted authentication rather than a
stale connection. Background requests never open a browser or start a new login.

`tools` displays the server's native tool names, schemas and annotations. JSON
read calls require tools marked `readOnlyHint: true`; annotation-free or writing
tools cannot enter read-only examples. Server-side permissions remain necessary.
An explicit write, such as Renovate's already-configured comment, uses the
separate non-read call intent and retains uncertain-write reconciliation.
Responses are limited to 1 MiB, calls are bounded/cancellable, and remote errors,
provider bodies and server stderr are not emitted as diagnostics.

## Minimal tool-using agents

For ordinary model-driven investigations, [`defineAgent`](durable-agents.md#minimal-tool-using-agent) registers and dispatches exact selected native tools without per-definition runtime wiring. Choose `mcp.placement: "host"` explicitly to reuse the host profiles below, or `"workspace"` with the existing workspace config/state paths. Neither placement falls back to the other. A minimal Datadog definition is in `agent-host/examples/datadog-simple.ts`; the model chooses queries rather than receiving a hand-coded telemetry pipeline. Existing advanced definitions and their retained state do not change.

## Explicit host-client tool selections

A definition can give a host `McpServices` client a non-secret selection from its
persisted agent configuration:

```json
{
  "mcpConnections": [
    { "server": "datadog", "tools": ["search_datadog_monitors"] }
  ]
}
```

```ts
import { McpServices } from "@christianmoesl/radar-agent-host/mcp";

const mcp = new McpServices(undefined, host.config.mcpConnections ?? []);
const tools = await mcp.tools("datadog");
// Register only these schemas in the definition's native tool registry, and
// dispatch their exact server/tool bindings through this same client.
```

The second constructor argument selects exact profile/tool pairs, not wildcards
or aliases. An empty array denies all connections. Omitting that argument retains
the existing unrestricted **trusted host-code** client; it must not be exposed as
a generic model dispatcher. The restricted client snapshots its selection,
filters discovery, rejects excluded calls before connecting, and rechecks the
selected catalog before dispatch. A reconnect, new client with the same persisted
selection, or newly discovered tool cannot grant access.
Missing or duplicate selected tools fail visibly.
Calling with `readOnly=false` cannot disable the restricted client's read check.

Select independently verified read operations and provider-side read-only
permissions. `readOnlyHint` remains an additional advisory check, **not** permission
to expose any tool advertising it. This primitive does not install model tools or
enforce policy on arbitrary definition JavaScript.

**Placement of `McpServices` is unchanged:** this client, including its restricted
form, runs on the host. Use the definition-owned workspace adapter below when
MCP must run through the associated local/SBX execution environment.

## Native selected tools in the workspace

`createMcpTools` installs only selected schemas into the existing native durable
registry. It does not install a generic MCP dispatcher, shell, raw HTTP or write
tool. Persist this **non-secret** configuration with the registered definition:

```json
{
  "mcp": {
    "config": "/workspace/mcp/mcp.json",
    "state": "/workspace/mcp/state",
    "connections": [
      { "server": "datadog", "tools": ["search_datadog_monitors"] }
    ]
  }
}
```

```ts
import { createRegistry } from "@christianmoesl/radar-agent-host";
import { createMcpTools } from "@christianmoesl/radar-agent-host/mcp-tools";

// In the existing definition's async configure(host):
const selected = await createMcpTools(host.execution, host.config.mcp);
const registry = createRegistry();
registry.install(selected.extension);
// Return { models, registry } with the definition's existing model runtime.
// selected.inspection shows the workspace binding and exact server/tool ->
// stable model-name mapping, without reading or returning credentials.
```

The selected workspace must have Node 24+ and the agent-host package installed
in its own project. Configuration/state paths above are absolute **inside that
execution environment**; supply its installed servers and explicitly authenticate
its read-only session there. The adapter does not copy a host/interactive/CI OAuth
store, install a package, initiate login or fall back to host MCP. Missing packages,
authentication, selected tools or unavailable SBX fail visibly. For SBX, the MCP
client and any stdio server process run in the selected sandbox. A remote MCP
service still runs remotely. The trusted host definition is not sandboxed.

Every bridge operation uses `workspace-exec` with the original expected workspace
binding. Removed SBX, a changed sandbox, changed registration or disabled SBX
cannot redirect an existing runner to local host execution. The definition must
be explicitly restarted/reconfigured for a different binding.

Selection is snapshotted before discovery and enforced again inside every bridge
process before dispatch. Only exact selected reads become native tools; model
names are stable hashes of the exact server/tool pair, with readable identities
in their descriptions and inspection. Every call rechecks the server catalog.
Reconnect, restart or additional discovered tools never widens the selection.
Native read calls are replay-safe, cancellable and bounded. Provider content,
structured evidence and metadata are kept together in the native result; explicit
partial/truncated/error responses fail rather than becoming an all-clear.

Input schemas are checked with strict JSON Schema validation (2020-12 by default,
or explicit draft-07), including formats. Invalid schemas, unsupported keywords,
remote references and asynchronous validators are rejected. Arguments are checked
before native coercion and again against the current server schema before sending:
no string-to-number conversion, inserted defaults or removed properties.
`readOnlyHint` remains only an additional advisory check: select independently
verified read operations and provider-side read-only permissions.

Local protocol/native persistence fixtures and Go execution-routing/fallback tests
cover this adapter. They are not proof of a real Datadog read or a live SBX run.
Those qualification results must be recorded separately; the older host live-MCP
matrix cannot qualify workspace placement. Existing calendar events may trigger
the definition's ordinary event task; no additional scheduler is introduced.

### Fixed-read qualification example

`agent-host/examples/mcp-check.ts` uses the same selected bridge from its native
Radar event task and persists the complete bounded evidence in a durable document
and Radar presentation. It needs no model or anomaly-detection logic. Alongside
`mcp` above, configure one to ten fixed reads with native arguments verified
against your authenticated server's catalog:

```json
{
  "reads": [
    { "server": "datadog", "tool": "search_datadog_monitors", "arguments": {
      "query": "tag:team:example-team"
    } }
  ]
}
```

The query is illustrative, not a verified selector or universal provider schema.
Use only your approved read scope. Events cannot replace the configured tool or
arguments. Copy the example and adjust its SDK imports as described in the
[editable examples guide](durable-agents.md), then register it normally:

```sh
radar agent add ./mcp-check.ts --id mcp-check --config ./check.json
radar agent schedule set mcp-check morning --time 09:00 --weekdays mon,tue,wed,thu,fri --timezone Europe/Vienna
radar agent start mcp-check
radar agent inspect mcp-check
```

The example records the intended calendar occurrence, memoizes successful reads
for task recovery, and uses the existing native receipt/event deduplication.
Partial/failed reads produce an attention-marked incomplete check, never an
all-clear. Success is evidence collection, not a health assertion or external
notification. Protocol fixtures cover calendar-shaped events, persistence,
reopen, deduplication and failure; real Datadog/SBX qualification remains separate.

## Jira assistant

Keep `baseUrl`, `cloudId`, `accountId`, `jql`, `modelId` and existing fixture/model
settings. Remove the old agent `profile`; optionally select `mcpServer` (default
`jira`). Copy `jira-mcp.ts` instead of the removed `twg.ts` helper. This does not
change the host collector's separate `jira.profile`.

The assistant reads Atlassian v2's `data` envelope. It uses
`searchJiraIssuesUsingJql` for issues and the native `listJiraIssueComments` for
discussions, with `view: "full"` so compact presets cannot omit required content.
Issue cursors and exact comment totals are verified across every page. The
native comments tool accepts `cloudId`, `issueIdOrKey`, `startAt`, `maxResults`
and `orderBy`, returning `data.comments`, `data.startAt` and `data.total`.
Missing, partial or stalled pages preserve the previous discussion state.
`getJiraIssue` is qualified separately as an issue read; it is not the comments
transport. Remove the obsolete `commentsTool` setting when updating copied
definitions/helpers. Replies stay drafts; posting remains separate #42.

## Datadog investigator

Replace the agent's `site`, `org`, `baseUrl`, `logsQuery`, `metrics` and
`spansQuery` connection/query settings with `mcpServer` (default `datadog`) and
`reads`. Each named read contains the exact native tool name and `arguments`
from `radar-mcp tools datadog`. Optional `timeRange` gives the native field names
that receive the investigation's ISO timestamps:

```json
{
  "mcpServer": "datadog",
  "modelId": "your-model",
  "reads": {
    "api_usage": {
      "tool": "get_datadog_metric",
      "arguments": {
        "queries": ["avg:datadog.apis.rate_limit.usage.per_org_pct{*}"],
        "telemetry": { "intent": "Investigate monitor evidence using the configured read scope" }
      },
      "timeRange": { "from": "from", "to": "to" }
    }
  }
}
```

This shows the binding format; confirm the actual argument names/types against
your server's schema. There is no REST-parameter translation or CLI fallback.
Time fields must be distinct, absent from the fixed `arguments`, and receive
ordered timestamps; the model cannot overwrite a configured query/scope field.
The allowlist contains only `get_datadog_metric`, `search_datadog_monitors`,
`search_datadog_logs` and `search_datadog_spans`; use at most ten fixed reads.
The model can repeat a named read but cannot replace its tool or scoped query.
MCP content, sampling and truncation metadata remain part of the evidence; errors
produce a partial investigation. Existing conversations, incident versions,
repeat consolidation and recovery history remain intact. Logs/traces stay
outside the currently approved live test scope.

## Terraform and other service definitions

Use the same host connection, with the exact native read tool and argument
schema returned by the server. No tfctl or provider CLI belongs in SBX:

```ts
const result = await host.mcp.json("terraform", toolName, approvedArguments, signal);
```

There is no bundled Terraform agent definition to migrate. This shared client
is its service boundary when one is authored. Interactive Pi sessions use their
own host-side MCP extension/client; durable agents do not import another client's
private OAuth databases. Local Git and workspace setup continue through SBX.
The default Radar instructions prefer these host MCP tools in interactive
sessions too. Existing user instruction files are retained; update their service
guidance manually and configure the host Pi MCP client as described in
[Pi integration](integrations/pi.md#remote-service-connections).

## Verification and manual cutover

Normal CI exercises a real local MCP HTTP/OAuth fixture, rotating refresh across
two client instances, tool pagination, errors, restart/reconnect, and the actual
stdio MCP protocol during a Renovate runner SIGKILL/recovery test. Jira/Datadog
durable tests give the workspace executor a failing stub, including an SBX
binding, proving service requests do not use it. These tests use dummy tokens.
On 2026-10-31 the full Go/Pi checks, agent tests with the actual Radar binary,
isolated package install/import smoke and Linux amd64 image build/offline smoke
passed. The real provider MCP test and real SBX review test were not run locally.

For real read-only qualification, create a non-secret JSON list of approved
`server`, `tool`, `arguments` objects matching your authenticated MCP profiles:

```sh
RADAR_TEST_MCP_FIXTURES=/path/to/approved-mcp-reads.json \
  node --experimental-strip-types --test agent-host/test/mcp-live.test.ts
```

The live test requires a read-only annotation and evidence, closes/reopens the
client and repeats the read. If an access token is already expired at startup,
it also requires renewed future expiry to be saved after those reads. Otherwise
it explicitly skips natural-expiry qualification. Server-side revocation needs
a separate disposable grant and foreground check. The old SBX/Pup/TWG proxy
experiment is historical and no longer a release gate for this architecture.
The dedicated [hosted agent MCP workflow](mcp-live.md) includes these reads and
the Jira definition's completeness/pagination checks. Its setup command uses
the two MCP sign-ins to create selectors, run local qualification, seed encrypted
client state and dispatch CI. It needs no collector CLI stores or pre-existing
selector file. Local protocol fixtures are not a hosted or real-provider pass.

The [2026-10-31 live read matrix](qualification/mcp-reads-2026-10-31.json) passed
with the selected Jira and Datadog accounts on Linux amd64, Linux arm64 and macOS
arm64, including populated issue/comment pagination, monitors/metrics and
reconnect. This resolves the live read gate. On 2026-11-01, [the renewal
matrix](https://github.com/ChristianMoesl/radar/actions/runs/41272962639) observed
both Jira and Datadog access tokens naturally expired before Linux amd64 reads,
then persisted renewed future expiry. All eight checks passed with no skips in
that job; token timestamps were never edited. Provider-side revocation, fresh
collector browser onboarding and Jira reply posting remain unqualified. See the
[disposable lifecycle procedure](mcp-live.md#disposable-lifecycle-and-reply-contract-qualification)
for the remaining MCP checks; CI-owned sessions must stay exclusive to CI.

The full [cross-provider workflow](live-e2e.md) remains available for combined
collector qualification with its own dedicated sessions.

Stop an agent before changing its copied definition/helpers/configuration,
configure/authenticate MCP on the host, then restart it. No agent/task/document
schema or conversation reset is introduced, and no existing CLI credential is
deleted or revoked. Existing sandboxes retain installed tools until explicitly
recreated; updating the image does not alter their private state.
