# TWG authentication limitations and separate comment work

On 2026-10-20, the user accepted the gap between checking a saved OAuth profile
and dispatching the next TWG request. Reliable durable-agent Jira reply posting
was split into [#42](https://github.com/ChristianMoesl/radar/issues/42). Neither
blocks the core read-only OAuth migration. Production collection, onboarding and
the read-only assistant now use native TWG commands; authenticated REST paths
and Radar-owned Jira credentials have been removed.

## Accepted check/request race

The official checksum-verified TWG 1.3.8 accepts saved PAT profiles with explicit
`--profile` and `--site`. Its help exposes no request-scoped expected auth method.
`auth profile list` and the subsequent provider command are separate processes:
another program can replace the saved profile after the check but before the
request loads it. Checking twice or locking only Radar cannot prevent that.

Radar removes inherited `TWG_*` overrides except CLI-owned storage selectors,
selects the named profile and Cloud ID explicitly, rejects saved non-OAuth
profiles, checks the configured domain/account, then requires OAuth and successful
identity/connectivity in `doctor` before each provider read. It preserves proxy
settings and CA trust. The user accepted the remaining race; these checks must
never be described as atomic OAuth-only dispatch.

`TestReleasedTWGSavedProfileAuthModes` characterizes the actual binary headlessly
against a rejecting local proxy with synthetic stores/credentials:

- Saved PAT profiles attempt API dispatch despite explicit profile/Cloud ID.
- Injected `TWG_TOKEN`/`TWG_USER` can override an OAuth profile: metadata still
  reports OAuth while `doctor` resolves PAT. Exit status alone does not establish
  successful connectivity.
- Replacing a synthetic OAuth profile with PAT between checks and a request lets
  the next request attempt dispatch. This is a controlled interleaving, not a
  concurrent provider refresh/rotation test.

The proxy rejects destinations before provider TLS and counts CONNECT attempts;
it never receives credential headers or sends test credentials to a provider.
Synthetic-store construction exists only in tests. Radar never reads or copies
live CLI credential files or exports tokens. Unsupported token/header overrides
are not a substitute for a request-scoped auth requirement.

The production live test passed against three approved issues in Linux amd64,
including inherited-static-override sanitization, one-row cursor pagination,
native get requests and missing-profile preservation of previous facts. Broader
expiry/revocation/browser/SBX gates remain in the [migration guide](cli-oauth-migration.md).

## Comment properties: separate #42

Earlier CLI qualification added two clearly labeled comments to an approved
disposable issue; no issue/comment was deleted. Native create accepts an
object-valued operation property:

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

The old string-valued property received HTTP 400. Native query pages return a
`data` array, top-level `pageInfo` and `meta.pagination` totals; `--after` consumes
the next cursor. Neither query, get-with-comments nor a machine-input properties
expansion exposed comment properties. Create responses omitted them too. The
qualified help contains issue-property commands, not comment-property retrieval.
Issue properties cannot atomically identify a particular accepted comment.

Without operation-property readback, a client cannot safely decide whether an
uncertain comment write already succeeded. Do not replace reconciliation with
text/author matching, hidden text or a direct HTTP fallback. The bundled assistant
therefore reads issues/comments, retains conversations and produces drafts.
`allowPosting: true`, old reply actions and pending post checkpoints fail visibly
without sending a comment. Reliable posting and its human approval/reconciliation
workflow are tracked in #42, outside the core migration.

## Reproduction

```sh
RADAR_TEST_TWG=/absolute/path/to/checksum-verified/twg \
  go test ./internal/integration/jira -run TestReleasedTWG -count=1 -v
```

Provider-contract CI runs these characterizations on all three supported native
platforms. Distribution/fixture checks do not prove live OAuth platform parity
or provider behavior inside SBX. No breaking release has been published.
