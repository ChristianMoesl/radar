# CLI OAuth hard switch

Tracking: [epic #27](https://github.com/ChristianMoesl/radar/issues/27).

The migration is **not release-qualified yet**. Datadog's Pup transport and shared
onboarding are implemented. Jira's TWG transport, full Development-panel PR-link
parity, platform execution, live refresh/revocation and real SBX OAuth checks
remain open. Do not publish the breaking release until those checks pass.

## Datadog manual cutover

1. Stop the Radar daemon before replacing its binary or configuration.
2. Install [Pup 2.0.0+](https://github.com/DataDog/pup). `radar setup` offers the
   official installer and can launch it after confirmation. Manual installation:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/DataDog/pup/v2.0.0/install.sh | PUP_VERSION=2.0.0 sh
   ```

3. Remove the `datadog` mapping from Radar's `secrets.yaml` manually. Remove any
   `api_key`, `app_key` or `access_token` fields under `datadog` in `config.yaml`.
   Unset `RADAR_DATADOG_API_KEY` and `RADAR_DATADOG_APP_KEY`. These old credentials
   are intentionally rejected; Radar will neither migrate nor delete them.
4. Keep monitor query/status policy and site. Choose a named Pup session in
   `datadog.org` (default `radar`). The session label selects the organization
   chosen during browser authorization. Remove the retired `RADAR_DATADOG_SITE`
   override and put the site in configuration.
5. Run `radar setup`. It checks CLI installation/version, launches
   `pup auth login --site datadoghq.eu --org radar --scopes monitors_read`, verifies
   the OAuth method/session/site/scope and performs a scoped monitor read before
   saving the connection. Installation/login failures can be retried or deferred.
6. Restart Radar and confirm collection succeeds. Remove/revoke the old keys
   separately through Datadog after validation; Radar never revokes them.

No task, note, workspace or durable-agent store reset is required. Existing
monitor identities and recovery rules remain the same. Failed or incomplete
requests retain previous monitor facts.

## Agent and sandbox setup

Replace the Datadog investigator's `baseUrl` with `site` and `org`. Its event,
conversation and incident stores keep their existing format; no reset is needed.
The supplied sandbox image now provisions Pup 2.0.0 without credentials. For custom kits, install Pup yourself. Authenticate **inside the selected execution environment**:

```sh
pup auth login --site datadoghq.eu --org radar --scopes monitors_read,logs_read_data,metrics_read,apm_read
```

Grant only the scopes required by configured reads. The investigator allows logs
and spans searches and metric queries; it offers no remediation operation.
Telemetry remains bounded and partial failures remain explicit.

For SBX, provision Pup in the kit/startup tools and keep the CLI's own OAuth
storage writable inside the sandbox, including after runner restarts. Do not
mount/copy host token databases or inject exported access tokens. Run interactive
login explicitly in the sandbox terminal. Missing sandbox CLI/session/refresh
access fails in that environment; there is no host fallback. Real SBX OAuth and
callback behavior still require qualification before release.

## Read-only rollout preflight

Run `go run ./scripts/preflight-cli-oauth` before installing the breaking release.
It reports file presence and obsolete field names without displaying credentials
or changing files. Config/secrets with retired fields are intentionally rejected
and require manual edits; task state, notes, workspaces and durable agents keep
their formats. The preflight reports that the release is not yet qualified. In
the fresh execution environment on 2026-10-19, config, secrets, task snapshot,
durable-agent store and workspace directory were initially absent. After tests,
the workspace directory existed; the other inventoried formats remained absent.
No existing data required migration or reset for the qualification changes.

## Qualification evidence

| Capability | Evidence | Remaining gate |
| --- | --- | --- |
| Pup CLI contract | Official v2.0.0 release checksum verified; actual binary contract passed in CI on Linux amd64/Linux arm64/macOS arm64 | Live browser OAuth and provider behavior |
| Raw monitor search | Actual binary against a local HTTP fixture; raw JSON, scoped query, totals | Read-only real Datadog account |
| OAuth enforcement | Named session overrides config-file static bearer; inherited static credentials removed; invalid API-key fallback rejected before sending a request | Real expiry/revocation/concurrent refresh |
| Onboarding | Shared install/version/retry/defer flow; terminal wizard fixtures; OAuth/read checks | Real browser/SSO/callback on supported platforms |
| Datadog investigator | Local/SBX executor-selection fixtures, real local workspace checks, durable investigation/recovery fixtures; existing real SBX boundary CI passed | Real SBX OAuth session and telemetry |
| TWG distribution | Official 1.3.8 installer executed on Linux amd64; published checksums verified for Linux amd64/Linux arm64/macOS arm64 | Native arm64 execution and browser login on the other platforms |
| TWG issue requests | Live OAuth profile, project read, issue creation/read/update, two-page JQL, nested status categories, forced refresh and six concurrent reads | Complete failure/revocation coverage and Radar integration |
| TWG authentication | Actual binary rejects missing named profiles before API dispatch; inherited static credentials override a valid named OAuth profile | Production environment sanitization and saved non-OAuth profile/race rejection |
| Jira PR relationships | Actual relationship command executed with OAuth; empty graph returned structured pagination and zero reported AI credits | Populated Development-panel parity, freshness, complete pagination and polling costs |

The API-key guard deliberately shadows Pup's saved API keys with invalid HTTP
header values. With a named OAuth session, Pup sends only its stored OAuth bearer.
If the session disappears or refresh fails between checking and requesting,
Pup's HTTP client rejects fallback headers before any request is sent. Radar does
not read, extract, copy or log CLI credentials. This behavior is tested against
Pup 2.0.0; a future incompatible CLI must fail closed and be requalified.

Reproduce offline release-contract verification with a checksum-verified binary:

```sh
RADAR_TEST_PUP=/absolute/path/to/pup go test ./internal/integration/datadog -run TestReleasedPupOAuthGuard
RADAR_TEST_TWG=/absolute/path/to/twg go test ./internal/integration/jira -run TestReleasedTWGMissingProfile
```

The TWG check runs without a terminal and uses only fixture static credentials.
A local rejecting proxy confirms that an unprofiled static request reaches the
API endpoint, then verifies that issue/query/context requests with a missing
explicit profile return `AUTH_PROFILE_MISSING` (exit 1) without API dispatch.
Update/telemetry requests are rejected locally too and are distinct from API
dispatch. This does not prove that a saved PAT profile or an injected token is
rejected; the live check demonstrated that injected credentials take precedence.

Fixtures do not establish live OAuth, Jira association completeness or platform
parity. Keep #28 and the release gate open until all live qualification gates pass.

## Network access for live qualification

Keep the environment's existing package-manager/git allowlist and allow HTTPS
to these provider and release hosts:

```text
teamwork-graph.atlassian.com
api.atlassian.com
auth.atlassian.com
id.atlassian.com
radar-cm.atlassian.net
api.datadoghq.eu
app.datadoghq.eu
api.github.com
github.com
raw.githubusercontent.com
release-assets.githubusercontent.com
objects.githubusercontent.com
```

The selected Jira test site is `radar-cm.atlassian.net`; Datadog uses EU. A company
SSO domain may also be needed during browser authorization. On 2026-10-19 the
fresh environment successfully downloaded TWG's installer, manifest, checksums
and all three Radar-platform assets. The runtime's network readiness field was
`unknown`; actual successful HTTPS requests establish the access used here.
A user-approved TWG OAuth profile is now configured. Datadog still has no live
session. Network access alone does not establish authenticated provider access.

[CI for the implementation](https://github.com/ChristianMoesl/radar/actions/runs/39255191997)
passed all 14 jobs, including the actual Pup offline binary contract on all three
Radar platforms and the existing real SBX durable-agent check. These results do
not establish live Jira association parity or Pup/Jira OAuth inside SBX.

## Jira live E2E setup

Use `radar-cm.atlassian.net` and an explicitly selected test project and GitHub
test repository. Network access must be enabled before installing and qualifying
TWG. Then start `twg login` in the execution environment: its documented OAuth
flow prints a verification URL and short code that the user can approve in their
own browser. The CLI must own the resulting credentials and refresh them in that
environment. Do not paste passwords, tokens or credential files into chat.

Confirm the installed CLI's login/profile/site flags before scripting onboarding.
Verify the selected account, organization, site and a real issue read after login;
an authorization page alone is not evidence of a working connection. Authenticate
inside SBX separately when qualifying sandbox behavior.

The live matrix must cover:

- Onboarding from a missing CLI/session through installation, browser approval,
  read verification, retry and defer.
- Issue collection, configured JQL, assignee/status changes and stable identities.
- Development-panel PR associations, including multiple PRs, branches, pagination
  and completeness; compare Radar's results with Jira's actual panel.
- Jira investigator reads in the selected local/SBX environment, durable retries
  and explicit partial failures.
- Refresh, concurrent CLI requests, logout/revocation, wrong site/profile and
  denied permissions, without falling back to retired tokens.

Reads can use an existing fixture. Creating or updating issues/PRs requires a
designated test project/repository and explicit authorization. Use clearly
labeled disposable fixtures, and record cleanup separately. A dedicated test
account/session keeps revocation checks separate from normal user work. Live
Datadog monitor/telemetry and Pup OAuth tests require their own Datadog session.

### TWG 1.3.8 live qualification (2026-10-19)

The official installer completed with `--version 1.3.8 --skip-login
--skip-skills --yes --install-dir <scratch-bin>`. All three platform artifacts
matched `SHA256SUMS-v1.3.8`. Linux arm64 is an ELF aarch64 binary and macOS arm64
is Mach-O arm64; downloading and identifying them is not native execution.

Browser device authorization worked. Login using the hostname then failed with
`AUTH_SITE_UNRESOLVED` after a DNS timeout, leaving no saved profile despite the
successful browser step. Reading the public `/_edge/tenant_info` endpoint through
the inherited HTTPS proxy supplied the site's Cloud ID. A new browser login with
`--site <cloud-id>` completed and saved the named `radar` OAuth profile. No CLI
credential files were read or tokens exported. Reproduction, with the intended
site's Cloud ID:

```sh
twg login --new-profile radar --site <cloud-id>
twg --profile radar --site <cloud-id> -o json --output-summary none auth profile list
twg --profile radar --site <cloud-id> -o json --output-summary none doctor
twg --profile radar --site <cloud-id> -o json --output-summary none jira space get ABC
twg --profile radar --site <cloud-id> -o json --output-summary none jira workitem query \
  --jql 'project = ABC ORDER BY key ASC' --fields summary,status,issuetype,priority --first 2
```

Observed contracts and results:

- `auth profile list` reports non-secret profile name, site, Cloud ID and
  `authMethod: oauth`. `doctor` independently reports the resolved method and
  authenticated connectivity. It can exit successfully while connectivity
  fails, so its exit code alone is insufficient.
- **Explicit profile selection does not defeat inherited credentials.** With
  fixture `TWG_TOKEN`/`TWG_USER` alongside the saved OAuth profile, `doctor`
  reported resolved method `pat` and HTTP 401. Radar must remove inherited static
  credentials for every call and verify the resolved method; profile metadata
  alone cannot establish the authentication actually used by a request.
- The help map has no generic `api` command. `jira workitem query` and `get` are
  the observed issue routes; do not implement the earlier proposed `twg api`
  contract or claim `/rest/dev-status` OAuth access from these results.
- Query output has `apiVersion: v2`, `command: jira.workitem.query` and
  `data.issues`. Issue fields are flattened onto each issue; nested
  `status.statusCategory.key` is retained. A two-row page over three fixtures
  returned top-level `pageInfo.hasNextPage: true` and `pageInfo.nextCursor`.
  Passing that cursor with `--after` returned the third fixture. The final page
  omitted `pageInfo`; it did not emit an explicit `hasNextPage: false`.
- A single `get` returns a `data` array plus `meta.acquisition` counts and
  `meta.evidence.coverage`/truncation. Fixture issue identity and browse URLs
  matched the query response.
- Three clearly labeled disposable issues were created in the confirmed test
  project. One was unassigned and transitioned to Done using a transition
  discovered through `jira workitem transitions query`. Fixture cleanup remains
  separate; no issue was deleted. Its explicit read still succeeded with a
  nested `done` category, while the assigned/non-Done query returned only the
  other two fixtures.
- `auth refresh --force` reported `status: refreshed`, `rotated: true` and a
  refreshed named profile. Six simultaneous reads from separate headless
  processes succeeded after that refresh. This does not establish concurrent
  rotation, natural expiry, revocation or sandbox refresh.
- A nonexistent Cloud ID failed with `TWG_DEFAULT_ORG_LOOKUP_FAILED`, rather than
  returning data from the configured site. Permission-denied behavior for an
  existing site remains unqualified.
- Invalid JQL and a nonexistent issue returned structured
  `TWG_TOOL_EXECUTION_FAILED` errors, exit 1, with HTTP status 400 and 404
  respectively. Neither response was an empty successful collection.
- `context jira workitem ABC-123 --relationships
  jira_work_item_links_external_pull_request --detail full --first 1
  --usage-counts` returned `data.relationships: []`,
  `data.pagination.hasNextPage: false`, `endCursor: null`, and reported
  `ai-credit` usage of zero. Empty-graph evidence does not establish populated
  relationship shape, complete associations, freshness or the cost of polling
  populated graphs. The default lookback is seven days and must not silently
  omit older associations.

A user-created, dedicated GitHub fixture repository is verified private and
writable through the connected identity. Five fixture PRs cover open, closed
and merged states, three titles/branches without Jira keys, a conflicting
head-branch key and an unambiguous control. Fixture commits and PR bodies name
their intended test issues; these references alone are not association evidence.
No fixture branch or issue was deleted.

Organization app-installation access still returns HTTP 403 through the current
connection. Connect the test repository through the official
[GitHub for Atlassian setup](https://support.atlassian.com/jira-cloud-administration/docs/integrate-with-github/)
using a Jira site admin and GitHub organization owner. Use the resulting actual
Development-panel associations for qualification; remote links or text matches
are not substitutes.

Jira's production transport and bundled investigator remain pending. Keep #28
and the hard-switch release open until the authentication guard and populated
PR-link parity are proven.
