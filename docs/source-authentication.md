# Source authentication and reconnection

Radar delegates credential storage and renewal to each source CLI. Background
collection never runs login, opens a browser, reads CLI credential files or copies
tokens into Radar. Use **foreground `radar setup`** to reconnect the selected
connection when its CLI requires login. A new browser/SSO login succeeding does
not establish why the previous connection failed.

## What expires, and what can renew

| Source | Behavior and useful checks |
| --- | --- |
| Datadog / Pup 2.0.0+ | Pup renews an expired access token (also within its five-minute expiry buffer) while loading config, including for `auth status`. Radar's named-session status precheck runs **after** that attempt. Renewal requires the saved refresh grant **and** the CLI client registration. Check `DD_SITE=datadoghq.eu pup --org radar auth status`, using your configured site/session. An expired result does not distinguish revoked/expired grants, missing registration, storage errors or unavailable OAuth endpoints. |
| Jira / TWG 1.3.8+ | Radar checks the selected OAuth profile's site/account metadata, then calls `doctor` to verify OAuth identity/connectivity before reading issues. It does not reject a profile based on a locally calculated access-token expiry. Use `twg --profile radar --site CLOUD_ID doctor` with your configured profile/Cloud ID. A failed connectivity/identity check alone cannot establish grant revocation. |
| Terraform / tfctl 0.4.0+ | `tfctl auth status --profile=default --json` validates the selected profile. This contract exposes `active: false` for rejected **or unreachable** authentication; it does not expose a refresh-grant lifecycle. Radar cannot infer that a browser session expired or renew an arbitrary personal API token. Reconnect the selected profile in foreground setup if its credential must be replaced. |
| GitHub / gh | Radar uses the existing gh credential and API transport. Do not assume that a PAT, OAuth credential, or organization SSO authorization shares Pup/TWG's access/refresh-token lifecycle. Use `gh auth status --hostname github.com`; distinguish invalid credentials from permission/SSO failures and connectivity. |

Never paste token output, CLI stores or raw provider diagnostics into an issue.
If a failure recurs, record the source/CLI version, configured profile/session and
site, timestamp, sanitized status/exit code, and whether a read-only retry works
**before** reconnecting. Preserve permission, rate-limit and network failures as
such rather than automatically diagnosing expiry or repeatedly logging in.
A CLI requiring interaction must be reconnected in the foreground, not retried
with another account, a static-token fallback, or broader permissions.

## Failure retention and recovery

An unsuccessful or incomplete collection does not establish that old work items
recovered. Previous observations remain available. Once the selected CLI session
works again, the next normal refresh retries it without requiring a daemon
restart; a complete successful response can then reconcile recovered items.

Datadog status now exposes sanitized OAuth verification guidance instead of only
"monitor search failed". In particular, missing refresh grants direct foreground
reconnection, while an expired result says **silent renewal failed**, not that
revocation was proven. Arbitrary CLI nonzero exits likewise retain permission and
connectivity possibilities. Raw provider diagnostics remain private.

## Investigation evidence (2026-11-10)

The original reported errors are unavailable. No individual live failure or
universal "all source CLIs expire" cause has been independently reproduced.
No real account, browser login, host profile, or live credential was used for
this investigation.

- The checksum-verified **released Pup 2.0.0** runs against a loopback TLS OAuth/API
  fixture with disposable tokens, client registration, and process-local CA trust
  on Linux. An expired access token and a near-expiry token each renew natively
  before Radar's precheck completes; the monitor request uses the renewed OAuth
  bearer and no API/application keys. No production OAuth implementation changed.
- The same fixture separately returns `invalid_grant` (representing expired or
  revoked refresh authorization) and HTTP 503 from renewal. Both fail closed
  before monitor reads, without exposing the response body. Missing refresh
  grants and missing client registration also fail closed without a token POST.
  **Pup's status cannot distinguish `invalid_grant` from these other failures.**
- Deterministic collection tests retain the existing alert during authentication
  failure, expose safe foreground guidance, then resume with the same source
  identity after simulated foreground reconnection. Only a subsequent complete
  healthy response recovers that alert. These tests do not qualify a live browser
  or SSO flow.
- Released TWG 1.3.8 with a synthetic expired OAuth profile attempts native
  renewal during `doctor`. A rejecting local CONNECT proxy observes the attempt
  without forwarding any credentials. This does not qualify successful TWG
  renewal or grant revocation. Existing tests enforce profile/site/account and
  OAuth identity; tfctl's released contract covers rejected tokens/ambiguous
  inactive status. Existing [qualification evidence](cli-oauth-migration.md#qualification-evidence-and-follow-up-testing)
  includes previously recorded native renewal; that is historical evidence, not
  a new live reproduction or evidence of server-side revocation.

Run the deterministic suite with:

```sh
go test ./internal/command ./internal/integration/datadog ./internal/integration/jira ./internal/integration/terraform ./internal/integration/github
RADAR_TEST_PUP=/path/to/checksum-verified/pup go test ./internal/integration/datadog -run TestReleasedPup -count=1
```

The existing provider-CLI CI matrix runs `TestReleasedPup*`; the new native OAuth
TLS renewal fixture runs on Linux amd64/arm64 and explicitly skips macOS, where
it would require a different trust fixture. No system trust store is modified.
Original live root cause and actual server-side revocation remain unqualified.
