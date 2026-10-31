# Terraform integration

Terraform tracks HCP Terraform runs created by the personal user authenticated
through `tfctl`. A remote workspace selects what to collect; each run is one
contributing work item, with its plan and change counts as context.

## Setup and configuration

Run `radar setup` and opt into Terraform. Enter a CLI profile name and the
organization. If login is needed and the profile does not exist, setup offers to
create it for `app.terraform.io` before authenticating. Creation requires separate
confirmation and does not change tfctl's active profile. Existing profiles,
including their hostnames, are reused unchanged. For Europe/Enterprise, create
the profile with `tfctl profile profiles create NAME --hostname=HOST --no-activate`
before setup. Profile creation and login are immediate CLI-owned changes, reported
in the final review; cancelling Radar setup does not undo them.

Setup reuses a working login, verifies the personal account, then
fetches the accessible workspaces for that organization. Select one or more with
↑/↓ and Space, then press Enter; existing choices are preselected when available.
Setup verifies every selected workspace and saves the exact names through the
normal configuration review. If discovery or access fails, retry or defer the
Terraform connection without losing the rest of setup. Missing tools can be
installed with Homebrew, or manually from the official releases. Login runs only
in foreground setup. An expired or unavailable connection never opens a background
prompt.

```yaml
terraform:
  profile: default
  organization: acme
  workspaces:
    - production-network
```

`profile` defaults to `default`. The profile owns the service hostname and API
credentials, including credentials reused from `terraform login`. Radar stores
no Terraform token. Every request explicitly selects the profile, without
changing the active profile. Configure Europe/Enterprise hostnames in tfctl.

Omit `enabled` for automatic prerequisite detection. `false` skips collection,
reconciliation, and bound lookup. `true` reports missing scope or CLI as an error.
An empty organization or workspace list disables automatic collection. A
configured connection whose authentication fails reports an error.

The minimum supported CLI is **tfctl 0.4.0**. That release writes version output
to stderr, requires global flags after the leaf subcommand, and returns structured
inactive auth status on an unsuccessful exit. It cannot distinguish absent,
expired, rejected, and unreachable authentication, so Radar does not silently
disable an ambiguous failed connection. Check it with:

```sh
tfctl auth status --profile=default --json
```

## Personal ownership and collection

Radar resolves `/account/details` and requires a personal user, then compares
each run's `created-by` user ID exactly with that account. The same check applies
to discovery, tracked-run refresh, and explicit bound lookup. A PR author, Git
event sender, commit author, matching message, or shared automation token does
not establish personal ownership. VCS-triggered runs appear only when Terraform
attributes their creation to that exact account.

Collection runs on the existing two-minute full refresh. Local refreshes,
dashboard reads, and status snapshots do not fetch runs. Workspace run lists
explicitly include speculative plans, saved plans, and normal run operations.
Each selected workspace is scanned once for both active and terminal runs.
New terminal outcomes are admitted when their creation/latest status timestamp
is within the existing three-day history window. Already tracked and bound runs
are read by ID regardless of that window.

Lists are paginated explicitly, without tfctl's `--all` option, which truncates
at 2,000 records. Radar does not assume listing order: it follows pagination to a
confirmed end, including through empty intermediate pages. Terraform can return
empty filtered results while its pagination still describes the unfiltered
history. Requests use 100 records per page and are spaced at 24 per minute across
all selected workspaces, below the endpoint's 30-per-minute limit. There is no
fixed page-count cap or overall scan deadline: large histories can make a full
refresh take longer than two minutes. Connection setup and individual CLI reads
remain time-bounded, and canceling collection interrupts pacing. Required missing
pages/reads are never represented as a complete scan.

Known observations survive failed pages, unknown states, inaccessible runs,
timeouts, and rate limits. A missing row or ambiguous 404 is not completion.
Optional plan counts/VCS context can be unavailable without changing confirmed
run state; absent counts remain unavailable rather than becoming zero. Radar
reads plan metadata, not full execution-plan JSON, state, variables, or plan logs.

## Refs, attention, and completion

The stable identity is `terraform:run:<service-host>:<run-id>`, independent of
mutable workspace names or local profile names. Refs are authoritative,
`work_item`, contributing, and retained in terminal history. Their metadata
records the remote workspace ID/name, organization, creator, raw run status,
plan ID/counts, creation time, and verified optional repository/branch facts.
The remote workspace is not a local Radar workspace and supplies no local path.

| Run facts | Radar contribution |
| --- | --- |
| Queued, planning, checking/estimating, applying | In progress |
| Awaiting confirmation, saved plan, policy override required | Attention |
| Errored or terminal failed policy, awaiting review | Attention |
| Applied | Done, Applied |
| Successful terminal speculative plan | Done, Plan completed / speculative |
| Terminal no-change outcome | Done, No changes |
| Canceled or discarded | Done with the actual outcome |
| Unknown/incomplete state | Partial diagnostics; keep previous facts |

`radar ack <task-id>` (also used by normal link-opening behavior) quiets a
terminal failure immediately. The next confirmed collection completes that run
contribution while retaining the failed outcome and its reviewed event cursor.
The same reviewed failure does not replay during ordinary rediscovery. A new run
has its own identity and review event. Acknowledgement cannot complete a run
waiting for approval or a policy override. Acknowledgements retain their existing
cache-local semantics; they may need reviewing again after a cache reset.

Terraform and GitHub PRs share display order 1 and contributing authority. Jira
keeps preferred titles and first source-ref placement. Attention is still the
strongest effective independent source signal: a Terraform failure can raise
attention on a task titled by Jira. An applied run cannot complete an active Jira
issue/PR. Authored notes remain primary; existing all-contributor completion,
explicit-reopen baselines, reported agent activity, and result-review holds apply.

## Linking, workflows, and actions

Configured ticket marks from a single source branch take precedence over the
run message; multiple branch marks disable automatic association. Without a
branch mark, configured marks in the run message can join existing work.
Structured PR URLs/numbers/repositories/branches are validated by the registered
review provider, which owns its identity format. Verified non-default GitHub source
branches can join matching repository/worktree branches. Workspace name,
repository alone, and shared/default branch do not merge unrelated deployments.

The normal overview, Inspect, search, CLI/JSON output, source status/ref counts,
notification transitions, mute/unmute, and Done/Muted history apply. Open actions
offer both the run and the remote workspace. Notifications use the effective
run URL even when Jira supplies the task title. Muting adopts/binds the normal
canonical note without creating a local workspace or other runtime resources.
Those bindings and the preference survive downtime, restart, and cache reset;
unresolved bound work blocks unsupported automatic completion.

A verified repository and non-default branch can seed the existing local
workspace flow. This discovers a unique local checkout, verifies/refetches the
branch, and returns an existing-branch seed. Otherwise use normal repository
selection or an already linked Radar workspace. Terraform owns no infrastructure
mutation or cleanup operation; existing local cleanup safety remains in force.

## Capabilities and validation

Capabilities: `Source`, `StatusReporter`, `SetupProvider`, `Reconciler`,
`BoundSourceResolver`, `ActionProvider`, and conditional `WorkspaceSeedProvider`.
Reconciliation occurs through direct tracked-ID reads during collection, rather
than an additional disappearance-based pass.

```sh
go test ./internal/integration/terraform/...
RADAR_TEST_TFCTL=/path/to/tfctl go test ./internal/integration/terraform -run TestReleasedTFCTL -v
make test
```

The opt-in release test uses the real CLI against a local TLS service, trusted
through a test CA, with an isolated fixture profile and synthetic token. It
uses a unique short-lived certificate. On macOS, the fixture runs only on a
disposable GitHub runner, adding its system Keychain trust because the native
verifier ignores `SSL_CERT_FILE`. Cleanup deletes the certificate; runner disposal
removes the remaining trust record, whose native removal requires a GUI on
macOS 26. Developer trust stores are never changed.
A separate local-only release test qualifies missing-profile creation and reuse,
including preservation of the active profile and existing custom hostnames. It
runs on macOS without changing trust stores.
The tests qualify profile/version/auth/API JSON and read-only query construction. The Linux amd64
0.4.0 archive was checked against HashiCorp's published SHA256:
`055d365cf165708dd304747f27a7242c51ce61a0bf680bd83fd3abe7cbcaa356`.
The provider-contract CI matrix runs the same pinned release fixture on Linux
amd64/arm64 and macOS arm64. Local evidence is Linux amd64; the other platforms
require their CI result. This does not prove live Terraform account permissions
or production ordering. Live account qualification remains outstanding in environments
without Terraform credentials/service access. Use a selected workspace to check
personal ownership, speculative success, approval/apply, failure/acknowledgement,
cancellation, outage recovery, and restart while a run completes.

### Live validation in GitHub Actions

The separate [Terraform live validation workflow](../../.github/workflows/terraform-live.yml)
runs only for a manually selected open PR targeting `main`, after approval of its
exact head commit. Start the workflow from trusted `main`; the workflow and approval
helper come from that dispatch revision, while the approved PR code is checked out
separately for testing. Ordinary pushes and PR events never start this live workflow.

Reuse the existing **Settings → Environments → live-e2e** environment, which
holds the live-validation credentials. Configure its approval protection:

- Under **Deployment branches and tags**, choose **Selected branches and tags**
  and add a **branch** rule for exactly `main`, with no tag rules. Use an explicit
  selection; **Protected branches only** allows all branches when no branch
  protection rules exist.
- Enable **Required reviewers** and select yourself (or another trusted maintainer).
  Enable **Prevent self-review** when another maintainer can approve; a solo
  maintainer must leave it off to approve their own dispatches.
- Disable **Allow administrators to bypass configured protection rules**.
- Store `TFCTL_TOKEN` in **Environment secrets**, not repository variables or
  committed configuration. Reuse it there if it is already configured; no new
  environment or credential copies are needed. Terraform consumes only this
  secret; the Jira/Datadog session bundle and other live credentials stay in the
  same shared environment. Required approval also gates the scheduled provider
  workflow that uses this environment.

The credential-free readiness job checks that required reviewers exist,
administrator bypass is disabled, and the only deployment rule allows the
`main` branch. A missing environment or weaker settings fail the run before
requesting the live job. Referencing an environment in YAML does not configure
GitHub's required-reviewer setting; configure that setting in GitHub first.

If not already present, add **`TFCTL_TOKEN`** as a personal Terraform user token
with normal read access to the checked-in fixture scope: `app.terraform.io`, organization `radar-oss`,
workspace `radar-oauth-e2e`. It must belong to a personal account with permission
to upload configuration, queue plans, and apply/cancel/discard fixture runs in
this disposable workspace. The approved job creates a fresh run as that account;
team/organization tokens cannot
establish personal ownership. An explicitly selected existing run must have the
same creator. No Jira, Datadog or npm setup is needed for this workflow, and no
token should be sent through chat.

Protect `main` with required reviewed pull requests and blocked force pushes.
Review the code and dependencies at the selected PR commit before approving:
code executing with the secret can use the credential, regardless of the
workflow's intended fixture operations.
Use a dedicated personal Terraform test account restricted to the disposable
organization/workspace and set token expiry. A user API token inherits that
account's permissions; Radar's read-only collection does not narrow them.
The fixture runs must be created by that same account for exact ownership checks.
Keep the workspace's cloud credentials and infrastructure limited to test resources.

Enter the secret value in GitHub or through the CLI prompt:

```sh
gh secret set TFCTL_TOKEN --repo acme/radar --env live-e2e
gh workflow run terraform-live.yml --repo acme/radar --ref main -f pr_number=123
```

In the web UI, open **Actions → Terraform live validation → Run workflow**, select
`main`, and enter the PR number. Read the readiness summary's exact commit link,
then choose **Review deployments → live-e2e → Approve and deploy**. The
live runner and its Terraform credential wait for this approval. After approval,
the trusted helper rechecks that the PR is still open with the same head. A new
commit needs a new dispatch and approval; checkout always uses the pinned SHA.
Approving this deployment does not merge the PR. Results appear in this manually
dispatched Actions run; it is not an automatic required check on every PR.

The workflow pins its GitHub actions to exact commits, installs checksum-pinned
tfctl 0.4.0 and Terraform 1.17.0, and releases credentials only after environment
approval. Unless `run_id` selects an existing fixture, it uses the trusted
dispatch commit's [bundled fixture](../../testdata/terraform-live) and runs native `terraform init` / `terraform plan`
with the personal token. The fixture has only a built-in `terraform_data`
resource; it creates no external infrastructure. Only its three root `.tf` files
are uploaded from a disposable directory. The CLI creates a speculative run and
does not apply, destroy, or change workspace settings. Its raw output stays out
of CI logs; the exact run ID is passed to Radar's qualification test. In the
same approved job, a second fresh speculative run sets `fail_plan=true` to
exercise the fixture's deliberate precondition failure. CI requires both the
expected failure diagnostic and a run URL in the approved workspace; an
unrelated CLI error or unexpected success fails the job.

The same approval also authorizes the trusted
[lifecycle harness](../../scripts/terraform-live-lifecycle.py). It uploads only
the three fixture files as a provisional configuration, with automatic run
queueing disabled. It creates two personal runs with auto-apply disabled and
unique revision markers, verifies Attention while confirmation is required,
and cancels the queued second run. It then stops an isolated production Radar
daemon, applies the first run, and restarts the daemon after Terraform reaches
`applied`. Both terminal tasks must retain their original Radar IDs and report
Done with the actual `applied` or `canceled` outcome.

This stage changes the built-in fixture's Terraform state. Before applying, it
requires the plan metadata to report zero resource destructions. It refuses a
locked workspace or an existing unfinished run, validates the exact creator,
workspace and configuration version before each action, and releases only runs
created during this check when cleaning up. It does not force-cancel, unlock,
destroy, change workspace settings, or act on a manually selected `run_id`.
The fixture creates no external infrastructure.

Recovery checks inject failed list reads and failed service reads at the
isolated daemon's tfctl boundary while using live account data. They require
partial/error source status with unchanged task IDs and facts, persistence
through a restart during the read outage, and a return to healthy collection
after removing the fault. They do not interrupt HCP Terraform itself or prove
an actual provider-wide outage. The daemon uses disposable configuration,
SQLite state, CLI selectors, socket and PID paths; developer state is untouched.

The qualification step passes the same secret to tfctl's standard environment
authentication with a temporary profile containing only hostname/organization selectors. Normal Radar
use can continue to reuse an existing CLI login; `TFCTL_TOKEN` is not a required
Radar setting. No CLI session bundle, secret writeback token, GitHub fixture
token, or Jira/Datadog credentials are needed for this workflow. No cloud-workspace credentials or
network-policy change is required when the test runs on GitHub-hosted runners.

The opt-in `TestLiveTerraformCollection` exercises production source status,
personal account/workspace verification, complete paginated discovery, source-ref
contracts, plan-summary availability, both validated open actions, and a cold
binding read. `TestLiveTerraformFailureAcknowledgement` requires the exact
deliberate speculative run to be `errored`, show Attention with an
acknowledgement cursor, and become Done on a fresh read after acknowledgement
in isolated Radar memory. The failed Terraform outcome must remain unchanged.
Both tests send only read requests, after their separate fixture creation steps. Logs
contain counts and enum states, without account IDs,
run messages, raw responses, plans, or credentials.

Leave the optional `run_id` input empty to create and qualify a fresh personal
plan-only run. This avoids relying on manual runs or the three-day terminal
discovery window. Supply a personally created `run-...` ID to qualify a
specific/older terminal fixture for the collection check. The deliberate failure
fixture and lifecycle runs are always created and checked separately under the
same approval. Empty discovery
without a selected fixture, incomplete
scans, failed
authentication and ownership/scope mismatches fail the run. The workflow forces
uncached Go execution so a previous live result cannot satisfy a new dispatch.

The lifecycle stage builds the selected PR daemon and qualifies the remaining
transition/recovery scenarios under that same approval. Its success and the
two source-level Go checks are required before recording the complete live
matrix. Ordinary pull-request CI runs the lifecycle harness against a read-only
fake CLI and the real daemon/database, without account access or Terraform
mutations. It never receives this workflow's Terraform secret.

Hosted smoke validation passed on **2026-10-29** in
[run 40834019053](https://github.com/ChristianMoesl/radar/actions/runs/40834019053)
using trusted workflow revision `cf830ea`, Terraform 1.17.0 and tfctl 0.4.0.
After manual approval, CI created a fresh personal speculative run, discovered
one `planned_and_finished` run with one plan summary, and passed exact ownership,
scope, source-ref, open-action and cold-binding checks. Failure acknowledgement
was not exercised in that first run.

Both live scenarios passed on **2026-10-31** in
[run 41025493654](https://github.com/ChristianMoesl/radar/actions/runs/41025493654),
using trusted workflow revision `808e663` and approved PR revision `537463d`.
`TestLiveTerraformCollection` qualified a fresh successful speculative plan.
`TestLiveTerraformFailureAcknowledgement` qualified a fresh deliberate
precondition failure: Attention became Done after acknowledgement on a fresh
read, with `failure_reviewed=true` and Terraform's `errored` outcome retained.
No apply or destroy was performed in that run.

The complete hosted smoke matrix passed on **2026-10-31** in
[run 41193871929](https://github.com/ChristianMoesl/radar/actions/runs/41193871929),
using trusted workflow revision `c6ed5d2`, approved PR revision `2b8a42d`,
Terraform 1.17.0 and tfctl 0.4.0. Both source-level Go checks passed again. The
production daemon observed Attention while apply confirmation was required,
then confirmed cancellation as Done with the `canceled` outcome. Injected list
and service read failures retained task IDs and facts, including through a
restart during the read outage; removing the fault restored healthy collection.
While the daemon was stopped, the trusted fixture harness applied the built-in
configuration after verifying zero planned resource destructions. Restart
recovered Done with the `applied` outcome and the original task ID. No destroy
was performed. Recovery evidence uses live account data with injected read
failures; it does not claim a provider-wide outage.

[Full CI for `c6ed5d2`](https://github.com/ChristianMoesl/radar/actions/runs/41193800919)
also passed every job, including the integrated test suite, all three released
provider CLI contract targets, and 64 offline live-harness checks. These results
complete #41's qualification matrix.

This integration adds configuration and generic source facts without changing
the persisted cache, authored binding schema, or integration-owned file layouts.

## Agent service access

This guide describes Radar’s host collector. Agent service access defaults to
[host MCP connections](../agent-mcp.md), independently of the workspace SBX
runtime. No provider CLI or credential session is required inside SBX.
