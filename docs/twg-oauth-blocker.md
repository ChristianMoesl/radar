# TWG OAuth dispatch and comment reconciliation blockers

On 2026-10-20, the official stable manifest still selected TWG **1.3.8**
(`1aaff1327b8d`, external build). Its installer and SHA256 verification passed
in the new Linux amd64 environment. HTTPS access to the release host, Atlassian
API and Datadog EU API succeeded through the inherited proxy. The user approved
a new Jira device login; the named OAuth profile, Cloud ID, project and actual
issue reads were verified through the CLI. No credential files were inspected
or tokens exported during live verification.

Two contracts required by the OAuth epic remain unqualified. The production
Jira collector, setup and investigator still use the existing transport. Do not
describe the migration as complete or publish its breaking release.

## OAuth-only dispatch

The release accepts a saved PAT profile with explicit `--profile` and `--site`.
An OAuth profile listing followed by a separate issue request cannot constrain
the request's authentication method atomically.

`TestReleasedTWGSavedProfileAuthModes` runs the actual binary headlessly against
a local rejecting proxy using only synthetic stores and credentials. It proves:

- A saved PAT profile attempts API dispatch even with an explicit name and
  Cloud ID and no inherited `TWG_*` authentication overrides.
- With a saved OAuth profile, injected fixture `TWG_TOKEN` and `TWG_USER` leave
  profile metadata reporting OAuth while `doctor` resolves PAT and reports
  failed connectivity. The test inspects those fields independently of exit
  status. Earlier live qualification also observed successful `doctor` exit
  despite failed connectivity; a successful exit is insufficient.
- A deterministic interleaving that replaces the synthetic OAuth profile with
  PAT after the profile check still allows the next issue request to attempt API
  dispatch. This characterizes the gap between two processes; it is not a test
  of concurrent provider token rotation.

The proxy rejects all destinations before provider TLS. It counts API CONNECT
attempts, never real provider traffic or credential headers. The tests do not
establish live PAT access or qualify a production OAuth guard. Synthetic store
construction is confined to tests; it is not an application strategy.

The released help exposes no request-scoped expected authentication method.
`TWG_TOKEN_MODE` is removed. `TWG_AUTH_DISABLED`, `TWG_OUTBOUND_AUTH` and delegated
access-token injection do not enforce use of the CLI's stored OAuth session.
Custom headers cannot replace Authorization. None is an acceptable workaround.

The required upstream outcome is a supported request-scoped OAuth requirement,
or another documented atomic mechanism, that rejects saved PAT and auth-method
changes **before API dispatch**. Qualify it against the released binary, including
missing profile, injected credentials, method changes, refresh failure and
concurrent requests. A second profile check, checking diagnostic output after a
request, or locking only Radar processes does not close the dispatch race.

## Native comment properties

Two clearly labeled comments were added to an existing approved disposable
issue in the confirmed test project. No issue or comment was deleted. These are
CLI qualification writes, not proof of the Radar investigator or SBX transport.

Observed native contracts:

- `jira workitem comment create --variables-json` accepts a `properties` array
  inside its input. The old string-valued `radar.operation` marker received HTTP
  400. An object-valued marker was accepted:

  ```json
  {
    "issueId": "ABC-123",
    "body": "[Radar OAuth E2E] Comment metadata fixture. No action required.",
    "bodyFormat": "plain",
    "properties": [
      {"key": "radar.operation", "value": {"operation": "fixture:ABC-123:1"}}
    ]
  }
  ```

- `jira workitem comment query --issue-id ABC-123 --first 1` returns a `data`
  array, top-level `pageInfo`, and `meta.pagination` with `startAt`, `maxResults`
  and `total`. The first of two comments advertises cursor `"1"`; `--after 1`
  returns the second with `hasNextPage: false` and `nextCursor: null`.
- Neither those query pages nor `jira workitem get ABC-123 --comments` returned
  comment `properties`. Passing `expand: "properties"` in query machine input
  also returned no properties. Create responses did not expose the properties
  either, so acceptance alone does not verify that the marker can be read back.
- The help map contains issue-property commands, but no comment-property read
  command or comment-query expansion flag. Issue properties are a different
  resource and do not atomically identify a particular accepted comment.

The investigator's existing REST path explicitly requests `expand=properties`.
It now writes and checks an object-valued marker. Its fixture tests cover a
comment accepted before an HTTP 503, agent restart and a new human retry: the
marker is reconciled and no duplicate is posted. This repairs the existing
path; it does not qualify native TWG comment reconciliation.

The native transport needs a supported way to retrieve operation properties
for every comment, with bounded complete pagination and explicit failures.
Until that contract is available and verified, an uncertain write cannot be
retried based on an absent property in TWG output. Do not substitute text or
author matching, append hidden text to the approved reply, or add a direct HTTP
fallback.

## Reproduction and remaining work

```sh
RADAR_TEST_TWG=/absolute/path/to/checksum-verified/twg \
  go test ./internal/integration/jira -run TestReleasedTWG -count=1 -v
```

The provider contract CI runs both the missing-profile checks and these saved
profile characterizations on the three supported native platforms. Local results
in this round establish Linux amd64 only; inspect the new CI run separately.

Local validation passed with umask 022: `go test -p 2 ./...`, including the actual
TWG contracts, and `pnpm check` (296 Pi tests, 22 agent tests passed; four optional
live/review tests skipped). The TypeScript run unset inherited `GIT_AUTHOR_EMAIL`
and `GIT_COMMITTER_EMAIL` so the existing publisher fixture could select its own
bot identity. No publisher code was changed.

The persisted-data preflight found no Radar config, secrets, task state, durable
agent store or workspace directory before testing. After testing, the workspace
directory exists; the other inventoried formats remain absent. This fix changes
neither their formats nor the task/event versions. Existing invalid connection settings
still need the documented manual hard-switch cutover when the transport lands.

Tracking: epic #27, qualification #28, Jira transport #6, onboarding #30,
agents/SBX #31 and validation/release #32. Their remaining work includes the
production hard switch, foreground onboarding, actual investigator execution,
natural expiry, server-side revocation and live SBX OAuth. Datadog logs/traces
were outside this round. The full live E2E harness was not rerun here.
