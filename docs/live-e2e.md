# Live provider E2E in GitHub Actions

Agents use [host MCP](agent-mcp.md). The workflow checks those host connections
alongside the collectors' TWG/Pup transports. MCP qualification uses
`mcp-live.test.ts` with separate client-owned OAuth profiles; neither client sends
credentials into SBX. The old
[SBX OAuth experiment](sbx-oauth-proxy-test.md) is historical.

The separate [Terraform live validation workflow](../.github/workflows/terraform-live.yml)
tests trusted `main` automatically on pushes through `live-e2e-main`, without
approval. A dispatch from `main` with a PR number instead tests that exact PR
revision through the approval-protected `live-e2e` environment. Both environments
hold a `TFCTL_TOKEN` personal-user secret for the selected workspace. The
live job uses the trusted workflow commit's bundled Terraform fixture to
create a fresh speculative run before qualification;
supplying an existing `run_id` skips fixture creation. It needs no OAuth session
bundle, GitHub fixture token or Jira/Datadog account. Its trusted workflow comes
from `main`; ordinary PR events do not start credentialed tests. Leaving the
PR number empty in a manual dispatch selects main without approval. See
[Terraform live setup and coverage](integrations/terraform.md#live-validation-in-github-actions).

The [Live provider E2E workflow](../.github/workflows/live-e2e.yml) uses ordinary
GitHub-hosted Linux amd64, Linux arm64 and macOS arm64 runners and the dedicated
test accounts. No self-hosted
runner is needed. Normal PR CI stays offline. Live runs execute trusted `main`
manually or daily at 05:23 UTC after setup is explicitly enabled.
It uses `live-e2e-main` without reviewer approval and rejects other workflow refs.

The live gate checks:

| Environment | Assertions |
| --- | --- |
| Jira / TWG | Named OAuth profile and exact Cloud ID/account, forced refresh, production Radar collection with one-row cursor pages and native gets, inherited-override sanitization, isolated daemon identity/failed-login retention/recovery |
| GitHub | Radar's actual provider resolves exact references for the selected open, closed, merged and conflicting-branch PR fixtures |
| Host MCP | Native Jira issue/comment reads through the definition's completeness checks, forced issue cursor continuation, Datadog monitor/metric read tools, read-only annotations, partial-result rejection and reconnect |
| Datadog / Pup | Named EU OAuth session, required scopes, six concurrent forced refreshes and subsequent reads, real Alert / Warn / No Data / recovery, collector credential guards, task identity and failure retention through daemon restarts |

Jira checks now exercise the production OAuth transport. Daemon checks require
at least two selected issues assigned to the OAuth account and still open.
Include a third selected issue that is Done; cold authored binding resolution
must preserve its source identity and completion even outside assigned search.
This workflow does not qualify logs/traces, natural expiry, server-side
revocation, browser onboarding, live model output or webhook delivery.
Those remain separate release gates. A missing service, expired login,
permission error or fixture mismatch fails the live run.

The earlier complete harness passed against the approved accounts on 2026-10-19,
before the Jira production checks were added: TWG
reads/pagination, all six private PR fixtures, and the Datadog lifecycle plus
workspace metric samples. Both real CLI stores also passed an opaque bundle
round trip and CLI status validation without exposing credentials or refreshing
the temporary copies. This is local live qualification; a hosted live CI pass
still requires the one-time GitHub environment setup below. The active GitHub
connection could not manage environment secrets/variables (HTTP 403).

On 2026-10-20 the new production Jira source check passed against three approved
issues, including one-row pages, native gets, static-override sanitization and
missing-profile retention. An isolated daemon kept its task IDs through restart,
failed login and recovery; native issue/comment reads also passed through a
registered local workspace.

On 2026-10-31, the expanded **collector** harness passed on Linux amd64 at
`6ee4a36`: production Jira reads/one-row pagination/daemon retention, all six real
GitHub PR-reference fixtures, and Datadog Alert/Warn/No Data/recovery with stable
task IDs through failed login and restarts. Both monitors were left healthy.
Pup also automatically renewed the idle session before forced-refresh checks.
See [the sanitized report](qualification/oauth-collectors-2026-10-31.json).
That run preceded the new MCP gate; it does not qualify MCP access or hosted CI.

## How sessions survive disposable runners

The `live-e2e-main` GitHub Environment holds an encrypted `LIVE_E2E_SESSIONS` secret.
The workflow restores opaque CLI and MCP client stores into private temporary
files. Pup, TWG and the Pi MCP client own token interpretation and refresh.
The CI storage helper transports their bytes without parsing tokens. MCP's
transient SQLite locks are excluded. Live MCP configuration allows remote HTTPS
OAuth profiles only, without stdio commands, static headers or embedded credentials.

The session bundle is **version 2**. Earlier two-CLI bundles are intentionally
rejected before any refresh. Explicitly re-seed from the selected CLI and MCP
client stores after authenticating MCP; no token conversion, fallback or automatic
migration is performed. Existing source stores are retained. The local preflight
found no prior MCP stores; hosted secret contents cannot be read back and were
not inspected. Keep the enabled flag off until the new seed succeeds.

Before any refresh, a checkpoint verifies that saving the unchanged session
works; a missing write permission fails before rotating tokens. After tests and
monitor recovery, an `always()` step replaces that secret with
the updated directories, including after a test failure. `gh secret set` uses
GitHub's public-key encryption and reads the bundle through stdin. Base64/gzip
only encode the bundle; GitHub's secret encryption provides confidentiality.
No session bundle, CLI file, provider response or raw subprocess log is uploaded
as an artifact. Only sanitized JSON and JUnit results are retained for 14 days.

Runs are serialized and never automatically cancel an earlier run. Environment
secrets are read when the live job starts, so a queued run receives the refreshed
session from its predecessor. The platform matrix also runs only one job at a
time, with fail-fast disabled; each platform checkpoints/writes back before the
next starts. Trusted-main jobs use the automatic environment without approval.
Do **not** move `LIVE_E2E_SESSIONS` to repository
secrets: those are read when the workflow is queued and can contain stale
rotating refresh tokens. Do not share these dedicated CLI stores with interactive
work or another workflow after seeding them.

GitHub cannot guarantee cleanup/writeback after a forcibly terminated runner.
Failed writeback makes the run fail; interrupted refreshes may require a fresh
browser login and re-seed. Repeated browser approval is otherwise unnecessary
while the providers continue accepting refreshes.

## One-time GitHub setup

In the Radar repository, configure **Settings → Environments → live-e2e-main** and restrict
deployment branches to an explicit selected **branch** rule for `main`, with no
tag rules. Disable required reviewers, wait timers, custom approval rules and
administrator bypass. Main dispatches and scheduled runs do not require approval.
Restrict writes to `main` to trusted maintainers and automation, and block force
pushes/deletion. PR credentialed validation uses the separate `live-e2e`
environment with required reviewers; it cannot use this automatic main path.

Add these secrets to that environment, entering token values directly in GitHub
or through `gh secret set`, never in chat:

| Environment secret | Purpose |
| --- | --- |
| `LIVE_E2E_STATE_WRITER_TOKEN` | Fine-grained GitHub token restricted to the Radar repository, with **Environments: read and write**, to update `LIVE_E2E_SESSIONS`. The normal workflow `GITHUB_TOKEN` cannot update environment secrets. Renew this token before its selected expiry. |
| `LIVE_E2E_GITHUB_TOKEN` | Read-only access to the private GitHub fixture repository: **Contents: read**, **Pull requests: read**, and implicit metadata access. `gh` uses it; the default workflow token cannot read another private repository. Authorize organization access if required. |
| `LIVE_E2E_SESSIONS` | Version-2 bundle seeded below; includes CLI sessions and MCP client configuration/registration/OAuth state. |

Create a local copy of [the non-secret selector example](../scripts/live-e2e.example.json).
Replace the Cloud ID, issue keys, fixture repository/PR expectations and the two
approved monitor IDs. Keep the real selectors outside this repository. On the
machine performing initial login, set `config_dir` to dedicated absolute CLI
directories; CI replaces these paths with its own temporary directories.
Set `mcp.config_file` and `mcp.state_dir` to dedicated host MCP configuration/state.
Retain only the Jira/Datadog remote profiles in that file. Configure `mcp.reads`
with the four native read tools in the example, using the actual arguments from
`radar-mcp tools`; example arguments are illustrative and must match the server's
schema. Use the approved issue keys and monitor IDs/tags. Logs/traces and writing
tools are rejected by this gate. Jira uses the definition's complete-read adapter;
`getJiraIssue` must return every comment or qualification fails.

The monitors must already exist, have type `query alert`, carry the exact tag
`radar-oauth-e2e`, contain no notification recipients, and have these exact names:

- `Radar OAuth E2E - States`
- `Radar OAuth E2E - No Data`

Only those two selected monitors are updated; no monitor or Jira issue is deleted.
Updates use the existing `datadog.apis.rate_limit.usage.per_org_pct` metric and an
unmatched filter with `on_missing_data: show_no_data`, without sending custom telemetry. The fixture account must
return actual samples for that built-in metric. Each run returns both monitors
to high recovery thresholds with `on_missing_data: resolve` and renotification disabled.
The workflow has a separate recovery step for failed/cancelled tests.

Install checksum-verified Pup 2.0.0 and TWG 1.3.8, then perform their normal browser
logins once using the dedicated stores:

```sh
PUP_CONFIG_DIR=/path/to/dedicated/pup DD_TOKEN_STORAGE=file DD_SITE=datadoghq.eu \
  pup --org radar auth login --scopes monitors_read,monitors_write
TWG_CONFIG_DIR=/path/to/dedicated/twg twg login --help
```

Use TWG's documented device approval flow to create the named OAuth profile and
select the verified Cloud ID. Confirm `auth profile list` reports `authMethod:
oauth` and file storage; OS-keychain credentials cannot be transported. Never
run `pup auth token` or print credential files. The existing dedicated test
sessions can also be seeded while they are valid, with no fresh approval.

Authenticate the MCP client separately, selecting the dedicated configuration
and state directories for every setup command:

```sh
export RADAR_MCP_CONFIG=/path/to/dedicated/mcp.json
export RADAR_MCP_STATE_DIR=/path/to/dedicated/mcp-state
radar-mcp login datadog
radar-mcp login jira
radar-mcp tools datadog
radar-mcp tools jira
```

Do not reuse an actively running agent's MCP state as the CI store. As with CLI
sessions, exclusive ownership after seeding prevents refresh-token rotation races.

With `gh` authenticated as a user who can update the `live-e2e-main` environment:

```sh
python3 scripts/live-e2e-state.py seed \
  --config /path/to/live-config.json --repository acme/radar
gh variable set RADAR_LIVE_E2E_CONFIG --repo acme/radar < /path/to/live-config.json
gh variable set RADAR_LIVE_E2E_ENABLED --repo acme/radar --body true
gh workflow run live-e2e.yml --repo acme/radar --ref main
```

`RADAR_LIVE_E2E_CONFIG` and `RADAR_LIVE_E2E_ENABLED` are **repository variables**,
not secrets. The selector file has no credentials. Enable the flag only after
all three environment secrets are ready. Before setup, scheduled runs are
disabled; manual dispatch fails with setup instructions instead of passing a
skipped live gate. A dispatched non-main ref is rejected before accessing sessions.

## Local verification and recovery

With Go, Node 24, pnpm, Python 3, `git`, `fd`, `tmux`, `gh`, Pup and TWG available:

```sh
pnpm install --frozen-lockfile
go build -o /tmp/radar-live ./cmd/radar
RADAR_BINARY=/tmp/radar-live python3 scripts/live-e2e.py \
  --config /path/to/live-config.json --report-dir /tmp/live-results
```

The harness creates isolated Radar state, then stops its daemons and removes
temporary state. MCP runs on the host without a workspace subprocess. It never changes the default Radar
configuration. Use the same selector file to serialize local invocations; never
run them against the same OAuth sessions concurrently with CI.

If a runner was forcibly killed or recovery failed, disable the automatic flag
and run:

```sh
RADAR_BINARY=/tmp/radar-live python3 scripts/live-e2e.py \
  --config /path/to/live-config.json --report-dir /tmp/live-recovery --recover-only
```

Reauthenticate through the CLIs and re-seed if refresh no longer succeeds. Test
harness safeguards offline with `python3 -m unittest discover -s scripts -p
'test_live_e2e.py'`; normal CI runs this as well.
