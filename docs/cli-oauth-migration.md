# CLI OAuth hard switch

Tracking: [epic #27](https://github.com/ChristianMoesl/radar/issues/27).

The production Jira/TWG and Datadog/Pup transports, foreground onboarding,
read-only investigators and sandbox CLI provisioning are implemented. Radar-owned
PR linking removes the GitHub for Atlassian dependency. The breaking release is
**not release-qualified**: natural expiry, server-side revocation, remaining
platform browser/onboarding checks and real provider OAuth inside SBX remain open.
Do not publish the breaking release until those gates pass.

On 2026-10-20 the user accepted TWG's check/request race: another program could
change the saved profile after Radar checks it and before TWG reads it for the
request. Radar sanitizes inherited overrides, pins the named profile/site/account,
and verifies OAuth metadata and actual connectivity before each read. These are
separate CLI invocations, not an atomic authentication guarantee. See the
[TWG contract report](twg-oauth-blocker.md). Durable-agent Jira reply posting is
separate work in [#42](https://github.com/ChristianMoesl/radar/issues/42); the
bundled assistant reads issues/comments and drafts replies without posting.

## Jira manual cutover

1. Stop the Radar daemon before replacing its binary or configuration.
2. Install TWG 1.3.8+. `radar setup` offers the official pinned installer,
   rechecks installation/version and launches interactive OAuth login. Manual
   installation:

   ```sh
   installer=$(mktemp)
   curl -fsSL https://teamwork-graph.atlassian.com/cli/install -o "$installer"
   bash "$installer" --version 1.3.8 --install-dir "$HOME/.local/bin" --skip-login --skip-skills --yes
   rm -f "$installer"
   export PATH="$HOME/.local/bin:$PATH"
   ```

3. Remove the entire `jira` mapping from Radar's `secrets.yaml`, including an
   empty mapping. Remove `email`, `api_token`, `access_token` and `api_base_url`
   under `jira` in `config.yaml`. Unset `RADAR_JIRA_EMAIL`, `RADAR_JIRA_API_TOKEN`,
   `RADAR_JIRA_ACCESS_TOKEN`, `RADAR_JIRA_API_BASE_URL`, `RADAR_JIRA_BASE_URL` and
   `RADAR_JIRA_CLOUD_ID`. These inputs are intentionally rejected, never migrated
   or deleted automatically.
4. Keep the site URL, authoritative issue types, status mapping, ticket prefixes
   and linking policy. Run `radar setup` to discover the Cloud ID, select a named
   OAuth profile (default `radar`), launch login, verify the site's account and an
   actual issue read, then confirm the displayed identity. Retry or defer failures.
   Only non-secret selectors are saved:

   ```yaml
   jira:
     enabled: true
     base_url: https://example.atlassian.net
     cloud_id: discovered-cloud-id
     profile: radar
     account_id: verified-account-id
   ```

5. Restart Radar and verify collection. Revoke the retired Jira token separately
   after validation; Radar never revokes it.

Jira uses native `jira workitem query/get`, flattened issue fields and cursor
pagination. There is no authenticated REST fallback or transport selector.
Failed, malformed, incomplete or unauthorized reads preserve previous facts.
Assigned work, explicit bindings after reassignment, informational references,
Done reconciliation, browse URLs and stable task/source identities retain their
existing behavior. No task, note or workspace reset is required.

## Datadog manual cutover

1. Stop the daemon and install [Pup 2.0.0+](https://github.com/DataDog/pup).
   `radar setup` offers the official installer and rechecks its version. Manual
   installation:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/DataDog/pup/v2.0.0/install.sh | PUP_VERSION=2.0.0 sh
   ```

2. Remove the `datadog` mapping from Radar's `secrets.yaml`; remove `api_key`,
   `app_key` and `access_token` fields from `config.yaml`. Unset
   `RADAR_DATADOG_API_KEY`, `RADAR_DATADOG_APP_KEY` and `RADAR_DATADOG_SITE`.
3. Keep the site and monitor query/status policy. Choose the named Pup session in
   `datadog.org` (default `radar`). Run `radar setup`; it launches browser login
   with `monitors_read`, verifies OAuth/site/session/scopes and performs a scoped
   monitor read before saving. Retry or defer failures.
4. Restart Radar. Remove/revoke the old keys separately after validation.

Monitor identities and recovery behavior are unchanged. Pup's saved-key fallback
is guarded by invalid newline API-key header values; empty strings are insufficient.
Released-binary tests prove that missing OAuth cannot dispatch a request with
saved keys. Radar never extracts tokens or reads CLI credential files.

## Agents and sandbox setup

The sandbox image provisions TWG 1.3.8 and Pup 2.0.0 without credentials. Custom
kits must install the CLIs themselves. Authenticate **inside the selected
execution environment**, using its terminal and writable CLI-owned OAuth storage:

```sh
twg login --new-profile radar --site <cloud-id>
pup auth login --site datadoghq.eu --org radar --scopes monitors_read,logs_read_data,metrics_read,timeseries_query,apm_read
```

Grant only scopes needed for configured reads. Do not copy host token databases
into SBX or inject exported access tokens. Missing sandbox CLI/session/refresh
access fails there without host fallback. Real provider OAuth and callback/refresh
behavior inside SBX still need qualification.

For the Jira assistant, configure `baseUrl`, `cloudId`, `profile`, `accountId`,
`jql` and `modelId`; remove the old `JIRA_EMAIL`, `JIRA_API_TOKEN` and
`JIRA_ACCESS_TOKEN` inputs. Copy its `twg.ts` helper alongside the example.
`allowPosting: true` is intentionally rejected with a link to #42. Existing
conversation/document/task versions are retained. An old pending post checkpoint
fails visibly without sending or repeating a comment; it requires manual review.

Replace the Datadog investigator's old `baseUrl` with `site` and `org`. Its
conversation/incident stores retain their format. Logs, spans and metric reads
run through the selected `WorkspaceExecution`; incomplete evidence is explicit,
and no remediation operation is offered.

## Read-only rollout preflight

Run `go run ./scripts/preflight-cli-oauth` before installing the breaking release.
It reports file presence and obsolete field names, never credential values, and
changes no files. Retired config/secrets are intentionally rejected and require
manual edits. Task state, notes, workspaces and durable-agent stores keep their
formats; example connection settings require the edits above.

In the fresh Linux environment on 2026-10-20, config, secrets, task state and the
durable-agent store were absent. Tests created a workspace directory. No existing
user data needed migration or reset. Qualification uses isolated Radar state and
the CLI's selected store; it neither copies nor inspects that store.

## Qualification evidence and remaining gates

| Capability | Evidence | Remaining gate |
| --- | --- | --- |
| CLI distribution | Official checksum-verified TWG 1.3.8 and Pup 2.0.0 install/run contracts passed on Linux amd64, Linux arm64 and macOS arm64 | Browser/provider checks on other platforms |
| Jira transport | Production `Source.Collect` passed against three approved issues on Linux amd64, including inherited-static-override sanitization, forced one-row cursor pages, native gets and missing-profile fact retention | Natural expiry, server revocation and denied permissions |
| Jira mapping/linking | Domain and collector fixtures cover assigned/discovered work, bindings after reassignment, Done, issue-type policy, PR references, stable IDs and failure retention; six private PR fixtures previously passed real `gh` reads | Broader live transition/linking matrix |
| TWG authentication | Named OAuth metadata, domain/Cloud ID/account and successful `doctor` connectivity checked before each request; actual-binary tests characterize missing profiles, saved PATs and the accepted check/request race | Atomic dispatch is an accepted limitation, not a core blocker |
| Onboarding | Shared install/version/login/retry/defer flow and all terminal integration-choice fixtures pass; real Linux device login succeeded | Full foreground browser/SSO/callback matrix on supported platforms |
| Read-only Jira assistant | Real native issue/comment reads through a registered local workspace passed; selected local/SBX executor fixtures cover pagination, incomplete reads, conversation retention and restart/retry | Real SBX OAuth; reply posting is separate #42 |
| Datadog monitors | Previous live EU daemon checks covered Alert/Warn/No Data/OK, stable IDs, missing login and restart; six concurrent forced refreshes plus later reads passed | Natural expiry and revocation |
| Datadog investigator | Previous live numeric metric samples through local workspace execution; durable conversation/retry/recovery fixtures | Logs/traces were excluded from the approved live scope; real SBX OAuth unverified |
| Sandbox | Image installs both CLIs; executor fixtures prohibit host fallback; existing real SBX boundary checks are separate | Provider login/storage/refresh in actual SBX |

Reproduce offline release contracts with checksum-verified binaries:

```sh
RADAR_TEST_PUP=/absolute/path/to/pup go test ./internal/integration/datadog -run TestReleasedPupOAuthGuard
RADAR_TEST_TWG=/absolute/path/to/twg go test ./internal/integration/jira -run TestReleasedTWG
```

The TWG characterizations use synthetic credential stores and a rejecting local
proxy; they do not send fixture credentials to providers. They document CLI
limitations, not an atomic OAuth guarantee. Normal CI skips live tests.

For read-only production Jira qualification, create a private selector JSON file
with `base_url`, `cloud_id`, `profile`, `account_id` and approved `issue_keys`
(at least two), then run with the CLI's existing store selected explicitly:

```sh
TWG_CONFIG_DIR=/path/to/cli-owned-store RADAR_TEST_JIRA_FIXTURES=/path/to/selectors.json \
  go test ./internal/integration/jira -run '^TestLiveTWGOAuthCollection$' -count=1 -v
```

The [live harness](live-e2e.md) now runs that production check plus isolated Jira
daemon restart/missing-login retention/recovery and registered local workspace
issue/comment reads. These new Jira checks passed locally on 2026-10-20.
The complete earlier harness passed on 2026-10-19 before
the Jira cutover; do not treat that as a run of the expanded harness. Hosted
activation still needs the documented GitHub Environment setup; the available
connection could not manage its secrets/variables (HTTP 403). Keep qualification
#28 and release #32 open until their remaining live gates pass.

## Network access

Preserve inherited HTTPS proxies and CA trust, plus package-manager/git access.
Required provider/release hosts include `teamwork-graph.atlassian.com`,
`api.atlassian.com`, `auth.atlassian.com`, `id.atlassian.com`, the selected Jira
site, `api.datadoghq.eu`, `app.datadoghq.eu`, `api.github.com`, `github.com`,
`raw.githubusercontent.com`, `release-assets.githubusercontent.com` and
`objects.githubusercontent.com`. Company SSO may require another browser host.
Actual HTTPS requests to the approved Jira site and Datadog EU succeeded in the
current environment. Network access alone does not establish authenticated access.

Creating/updating remote fixtures requires explicit authorization and a designated
test project/repository. Keep fixtures clearly labeled and record cleanup
separately. Three Jira issues, six PR fixtures and two non-notifying monitors
were previously approved; no issue, comment, PR or monitor was deleted. Monitors
were left healthy. Current Jira production verification uses read-only requests.
