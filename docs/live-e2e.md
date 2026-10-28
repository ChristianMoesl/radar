# Live provider E2E in GitHub Actions

The separate [SBX OAuth proxy test](sbx-oauth-proxy-test.md) prepares fresh native
CLI logins on a disposable runner and checks token masking, refresh and sandbox
restart. It uses neither the persisted live-E2E sessions nor their writeback flow.

The separate [Terraform live validation workflow](../.github/workflows/terraform-live.yml)
tests a manually selected PR revision after explicit environment approval. It
reuses the shared `live-e2e` environment and consumes its `TFCTL_TOKEN`
personal-user secret with the approved workspace selectors. By default, the
approved job uses the existing read-only `LIVE_E2E_GITHUB_TOKEN` to fetch the
pinned Terraform fixture and create a fresh speculative run before qualification;
supplying an existing `run_id` skips fixture creation. It needs no OAuth session
bundle or Jira/Datadog account. No separate environment or copies
of existing credentials are needed. Its trusted workflow comes from
`main`; ordinary PR events do not start it. See
[Terraform live setup and coverage](integrations/terraform.md#live-validation-in-github-actions).

The [Live provider E2E workflow](../.github/workflows/live-e2e.yml) uses ordinary
GitHub-hosted Ubuntu runners and the dedicated test accounts. No self-hosted
runner is needed. Normal PR CI stays offline. Live runs execute trusted `main`
manually or daily at 05:23 UTC after setup is explicitly enabled.

The live gate checks:

| Environment | Assertions |
| --- | --- |
| Jira / TWG | Named OAuth profile and exact Cloud ID/account, forced refresh, production Radar collection with one-row cursor pages and native gets, inherited-override sanitization, isolated daemon identity/failed-login retention/recovery and registered local workspace issue/comment reads |
| GitHub | Radar's actual provider resolves exact references for the selected open, closed, merged and conflicting-branch PR fixtures |
| Datadog / Pup | Named EU OAuth session, required scopes, six concurrent forced refreshes and subsequent reads, real Alert / Warn / No Data / recovery, collector credential guards, task identity and failure retention through daemon restarts, actual numeric metric samples through a registered local workspace |

Jira checks now exercise the production OAuth transport. Daemon checks require
at least two selected issues assigned to the OAuth account and still open.
This workflow does not qualify logs/traces, natural expiry, server-side
revocation, browser onboarding, live model output, webhook delivery or OAuth in
SBX. Those remain separate release gates. A missing service, expired login,
permission error, empty metric query or fixture mismatch fails the live run.

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
registered local workspace. The full expanded cross-provider harness has not
been rerun in this environment.

## How sessions survive disposable runners

The `live-e2e` GitHub Environment holds an encrypted `LIVE_E2E_SESSIONS` secret.
The workflow restores the opaque CLI directories into private temporary files.
Pup and TWG own all token interpretation and refresh. The CI storage helper only
transports the directories; Radar's application never reads credentials.

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
session from its predecessor. Do **not** move `LIVE_E2E_SESSIONS` to repository
secrets: those are read when the workflow is queued and can contain stale
rotating refresh tokens. Do not share these dedicated CLI stores with interactive
work or another workflow after seeding them.

GitHub cannot guarantee cleanup/writeback after a forcibly terminated runner.
Failed writeback makes the run fail; interrupted refreshes may require a fresh
browser login and re-seed. Repeated browser approval is otherwise unnecessary
while the providers continue accepting refreshes.

## One-time GitHub setup

In the Radar repository, reuse **Settings → Environments → live-e2e** and restrict
deployment branches to an explicit selected **branch** rule for `main`, with no
tag rules. For a public repository, require a trusted reviewer and disable
administrator bypass. Scheduled runs will also wait for approval. Enable
prevent-self-review when another trusted maintainer can approve; solo
maintainers must leave it off to approve their own runs. Create the environment
first only when configuring a new repository. Protect `main` with
reviewed pull requests and blocked force pushes. This environment is never
referenced by a PR-triggered workflow.

Add these secrets to that environment, entering token values directly in GitHub
or through `gh secret set`, never in chat:

| Environment secret | Purpose |
| --- | --- |
| `LIVE_E2E_STATE_WRITER_TOKEN` | Fine-grained GitHub token restricted to the Radar repository, with **Environments: read and write**, to update `LIVE_E2E_SESSIONS`. The normal workflow `GITHUB_TOKEN` cannot update environment secrets. Renew this token before its selected expiry. |
| `LIVE_E2E_GITHUB_TOKEN` | Read-only access to the private GitHub fixture repository: **Contents: read**, **Pull requests: read**, and implicit metadata access. `gh` uses it; the default workflow token cannot read another private repository. Authorize organization access if required. |
| `LIVE_E2E_SESSIONS` | Seeded by the command below; includes the CLI-managed client registration and refreshable sessions. |

Create a local copy of [the non-secret selector example](../scripts/live-e2e.example.json).
Replace the Cloud ID, issue keys, fixture repository/PR expectations and the two
approved monitor IDs. Keep the real selectors outside this repository. On the
machine performing initial login, set `config_dir` to dedicated absolute CLI
directories; CI replaces these paths with its own temporary directories.

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
  pup --org radar auth login --scopes monitors_read,monitors_write,metrics_read,timeseries_query
TWG_CONFIG_DIR=/path/to/dedicated/twg twg login --help
```

Use TWG's documented device approval flow to create the named OAuth profile and
select the verified Cloud ID. Confirm `auth profile list` reports `authMethod:
oauth` and file storage; OS-keychain credentials cannot be transported. Never
run `pup auth token` or print credential files. The existing dedicated test
sessions can also be seeded while they are valid, with no fresh approval.

With `gh` authenticated as a user who can update the `live-e2e` environment:

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

The harness creates isolated Radar state and a local metric workspace, then stops
its daemon and removes temporary state. It never changes the default Radar
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
