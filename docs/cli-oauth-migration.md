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
their formats. This implementation environment has no existing Radar config,
secrets, task snapshot or agent store; its existing workspace directory is unchanged.
The preflight reports that the release is not yet qualified.

## Qualification evidence

| Capability | Evidence | Remaining gate |
| --- | --- | --- |
| Pup CLI contract | Official v2.0.0 release checksum verified; Linux x86_64 binary executed | macOS arm64/Linux arm64 execution |
| Raw monitor search | Actual binary against a local HTTP fixture; raw JSON, scoped query, totals | Read-only real Datadog account |
| OAuth enforcement | Named session overrides config-file static bearer; inherited static credentials removed; invalid API-key fallback rejected before sending a request | Real expiry/revocation/concurrent refresh |
| Onboarding | Shared install/version/retry/defer flow; terminal wizard fixtures; OAuth/read checks | Real browser/SSO/callback on supported platforms |
| Datadog investigator | Workspace-executor tests for local/SBX selection and no host fallback; durable investigation/recovery fixtures | Real SBX OAuth session and telemetry |
| TWG | Public docs and repository inspected; documentation describes 1.3.8; cached public manifest advertises Linux arm64 | CLI download/command qualification and real Jira PR-link parity |

The API-key guard deliberately shadows Pup's saved API keys with invalid HTTP
header values. With a named OAuth session, Pup sends only its stored OAuth bearer.
If the session disappears or refresh fails between checking and requesting,
Pup's HTTP client rejects fallback headers before any request is sent. Radar does
not read, extract, copy or log CLI credentials. This behavior is tested against
Pup 2.0.0; a future incompatible CLI must fail closed and be requalified.

Reproduce offline release-contract verification with a checksum-verified binary:

```sh
RADAR_TEST_PUP=/absolute/path/to/pup go test ./internal/integration/datadog -run TestReleasedPupOAuthGuard
```

Fixtures do not establish live OAuth, Jira association completeness or platform
parity. Keep #28 and the release gate open until real-account evidence is recorded.
