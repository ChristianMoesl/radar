# CLI OAuth hard switch

Tracking: [epic #27](https://github.com/ChristianMoesl/radar/issues/27).

The migration is **not release-qualified yet**. Datadog's Pup transport, shared
onboarding and app-independent PR linking are implemented. Jira's TWG transport,
remaining onboarding/expiry/revocation checks and real SBX OAuth checks remain open. Do not publish the breaking release until those checks pass.

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
pup auth login --site datadoghq.eu --org radar --scopes monitors_read,logs_read_data,metrics_read,timeseries_query,apm_read
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
| Pup CLI contract | Official v2.0.0 checksum/installer and offline contracts passed on all three platforms; live EU browser OAuth and provider requests passed on Linux amd64 | Browser/live checks on the other platforms |
| Raw monitor search | Live EU account, two approved non-notifying monitors, real Alert/Warn/No Data/OK changes through the Radar daemon; stable IDs and recovery after restart | Larger live result-limit/rate-limit cases remain fixture-qualified |
| OAuth enforcement | Live named OAuth survives injected static fixtures; missing session preserves both active tasks across daemon restart; six concurrent forced refreshes and six later reads passed | Natural expiry and server-side revocation |
| Onboarding | Shared install/version/retry/defer flow; terminal wizard fixtures; OAuth/read checks | Real browser/SSO/callback on supported platforms |
| Datadog investigator | Live metric series through actual local WorkspaceExecution; metric-backed investigation, duplicate delivery, restart and recovery retain the conversation with an explicit fixture model | Logs/traces excluded from this verification round; real SBX OAuth/telemetry unverified |
| TWG distribution | Official 1.3.8 installer and checksum-verified missing-profile contract passed in CI on Linux amd64/Linux arm64/macOS arm64 | Browser login and live provider behavior on the other platforms |
| TWG issue requests | Live OAuth profile, project read, issue creation/read/update, two-page JQL, nested status categories, forced refresh and six concurrent reads | Complete failure/revocation coverage and Radar integration |
| TWG authentication | Actual binary rejects missing named profiles before API dispatch; inherited static credentials override a valid named OAuth profile | Production environment sanitization and saved non-OAuth profile/race rejection |
| Radar-owned Jira/PR linking | User approved exact references plus explicit bindings; live `gh` reads passed all six private PR fixtures, including closed/merged and branch-conflict cases; full-page and failure checks use fixtures | Full OAuth end-to-end checks after the Jira transport cutover |

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

Fixtures do not establish live OAuth, Jira OAuth integration or platform
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
User-approved TWG and Pup OAuth sessions are now configured. Datadog EU monitor
and metric E2E checks passed in the selected local environment. Network access alone does not establish authenticated provider access.

[CI for the qualification changes](https://github.com/ChristianMoesl/radar/actions/runs/39264359204)
passed all 14 jobs, including the actual Pup and TWG offline binary contracts on
all three Radar platforms, installation/packaging checks, Go/Pi/agent checks and
the existing real SBX durable-agent check. The full Go suite also passed locally
with a normal file-creation umask, and `make install` completed after pushing.
These results do not establish the full Jira OAuth integration or Pup/Jira OAuth
inside SBX.

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
- Radar-owned PR references and explicit bindings, including multiple PRs,
  branch conflicts, closed/merged states, pagination and failure preservation.
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
is Mach-O arm64. The added release-contract CI subsequently executed the official
installer and binary on all three supported platforms; browser OAuth was tested
only on Linux amd64.

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
  omit older associations. An explicit `--since 36500d` was accepted and returned
  the same empty result; this does not prove historical association coverage.

A user-created, dedicated GitHub fixture repository is verified private and
writable through the connected identity. Six fixture PRs cover open, closed
and merged states, three titles/branches without Jira keys, a conflicting
head-branch key and an unambiguous control. Fixture commits and PR bodies name
their intended test issues. At that stage the goal was Development-panel parity;
the user subsequently chose those explicit references as Radar's linking policy.
No fixture branch or issue was deleted.

Organization app-installation access still returns HTTP 403 through the current
connection. The user confirmed organization connection and repository selection
through the official
[GitHub for Atlassian setup](https://support.atlassian.com/jira-cloud-administration/docs/integrate-with-github/)
using a Jira site admin and GitHub organization owner. A fresh PR was created
after that confirmation, and the user confirmed it is visible in the issue's
Development panel. Native development JQL now returns the fixture issue, and
`context get` finds six explicit `issue_associated_pr` edges. The
product-specific context route still returns no PR edges. Generic targets have
only opaque graph ARIs; the sampled PR-detail and anchor reads did not provide
provider identities. Generic context explicitly cannot cursor-page these edges,
even when `hasNextPage` is false. Observed populated context usage was two AI
credits per request; the subgraph diagnostic reported seven. Full reproduction,
coverage limitations and the required upstream outcome are in the
[live PR parity blocker report](twg-pr-parity-blocker.md). Remote links, count
matches and inferred identities are not substitutes for exact associations.

Jira's production transport and bundled investigator remain pending. Keep #28
and the hard-switch release open until the authentication guard and remaining
OAuth/live-provider checks pass. The graph investigation above is historical;
Radar no longer uses those relationships.

### App-independent PR linking (2026-10-19)

The user approved replacing Development-panel parity with Radar-owned linking:
exact configured keys or URLs in source branches, PR titles/descriptions, and
PR-owned commit messages, plus existing authored-note bindings for PRs without
references. One source-branch key wins over conflicting references elsewhere;
multiple branch keys require an explicit binding. See the
[linking rules](integrations/github.md#jira-links).

The Development endpoint collector and cross-provider resolver capability were
removed. GitHub uses its existing `gh` transport; Jira consumes generic resolved
mark keys. No GitHub for Atlassian installation or Teamwork Graph relationship
query is required, and linking consumes no Rovo credits. Links present only in
Jira's Development panel need a reference or binding before cutover. Existing
task/note/workspace formats are unchanged; derived commit marks use the existing
source metadata map.

A read-only live check against the six authorized private GitHub fixtures passed
through the actual provider code. PRs 1–3 have no key in title/source branch but
link through explicit body/commit references, including closed and merged PRs.
The conflicting fourth PR links only to its source-branch issue; both controls
link to the intended issue. Jira OAuth collection itself is still pending.
Pagination, changed-head/count checks, missing pages/cursors, denied access,
partial-result retention, ambiguous branches, workspace separation, and linked
active-Jira/merged-PR lifecycle are covered by deterministic tests.

The optional live test reads a non-secret JSON fixture list:

```json
[{"repository":"acme/app","number":7,"prefixes":["ABC"],"state":"OPEN","keys":["mark:ABC-123"]}]
```

```sh
RADAR_TEST_GITHUB_LINK_FIXTURES=/path/to/approved-fixtures.json go test ./internal/integration/github -run TestLivePullRequestReferences -v -count=1
```

The [TWG graph report](twg-pr-parity-blocker.md) is retained as historical evidence,
not a current release gate. The full Go suite and focused race checks passed.
Read-only preflight found no config, secrets, task snapshot, durable-agent store,
or GitHub cache files in this environment; the workspace directory is unchanged.
No persisted format changes or resets are required. No installed app or remote
fixture was removed.


### Datadog EU live verification (2026-10-19)

The user created an EU test account and approved Pup OAuth for the intended
organization. Pup 2.0.0 was installed using its official checksum-verifying
installer. The released CLI supports remote login by accepting the one-time
loopback callback URL through its own interactive prompt. Pup handled the code
exchange and stored/refreshed the credentials. Radar did not read token files,
export access tokens, or copy host credentials into another environment.

The user approved two explicitly named monitors with no notification recipients:
`Radar OAuth E2E - States` and `Radar OAuth E2E - No Data`. The first used an
existing Datadog API-usage metric; the second initially queried a deliberately
absent metric. No custom metric or telemetry ingestion was required. Observed:

- Browser OAuth, named EU session verification, real monitor searches and raw
  completeness metadata passed. Inherited static token/key fixtures did not
  override OAuth. A read-only opt-in Go test also passed against both populated
  unhealthy monitors.
- Six simultaneous `pup auth refresh` processes succeeded, followed by six
  successful headless monitor reads. This qualifies that forced-refresh case;
  natural expiry and server-side revocation have not been exercised.
- A real Radar daemon observed Alert as `immediate`, Warn and No Data as
  `attention`, and both monitors' eventual OK states as `done`/Recovered. Source
  IDs and completed tasks survived a daemon restart.
- Selecting a missing named session reported a source error and preserved both
  active tasks through another daemon restart. Restoring the intended session
  resumed successful collection without recreating tasks.
- A real metric query returned one series through the actual registered local
  workspace execution path. The Datadog investigator consumed that live metric,
  retained its conversation across runner restart, reused the original task for
  duplicate delivery, and retained history on recovery. Its report used the
  explicitly selected deterministic fixture model, not a live LLM.

Live requests found and corrected three investigator defects: lowercase OAuth
`bearer` must be accepted, the v1 timeseries route requires `timeseries_query`,
and span searches require the JSON API `data.type`/`data.attributes` envelope.
The metric request passed after the scope correction. Corrected trace requests
reached the account's index validation; neither trace nor log reads succeeded
because the new account had no searchable indexes. The user explicitly limited
this round to monitors and metrics. Logs/traces remain unverified; their checks
are skipped unless an explicit live query is selected. The source still reports
unavailable evidence rather than treating an error as a healthy result.

Reproduction uses the CLI-owned session and non-secret selectors:

```sh
RADAR_TEST_DATADOG_SITE=datadoghq.eu RADAR_TEST_DATADOG_ORG=radar \
RADAR_TEST_DATADOG_QUERY='tag:radar-oauth-e2e' \
go test ./internal/integration/datadog -run TestLivePupOAuthCollection -v -count=1

RADAR_TEST_DATADOG_SITE=datadoghq.eu RADAR_TEST_DATADOG_ORG=radar \
RADAR_TEST_DATADOG_WORKSPACE=/path/to/registered/workspace \
RADAR_TEST_DATADOG_METRIC_QUERY='avg:datadog.apis.rate_limit.usage.per_org_pct{*}' \
node --experimental-strip-types --test agent-host/test/pup-live.test.ts
```

Normal CI skips these live tests. For optional log/trace qualification, select
`RADAR_TEST_DATADOG_LOGS_QUERY`/`RADAR_TEST_DATADOG_SPANS_QUERY` explicitly after
setting up the corresponding Datadog products. The workspace must have its own
working Pup session in its actual execution environment; these commands do not
provision sandbox credentials.

Both remote monitors were left with high recovery thresholds, no notification
recipients, and No Data notifications disabled. No monitor was deleted. The
isolated local investigator and daemon were stopped after verification; their
state and conversation history remain available for review. Jira's production
OAuth migration, foreground onboarding coverage, natural expiry/revocation,
remaining platform browser checks and real SBX OAuth keep the release gated.
