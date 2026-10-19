# Datadog integration

Datadog supplies actionable monitor work items through the monitor search API.

## Capabilities

`Source`, `StatusReporter`, and `Reconciler`.

## Setup and behavior

Run `radar setup` to configure this integration, or edit the settings below.
Omitting `datadog.enabled` detects configured prerequisites; `false` disables
collection and `true` reports missing prerequisites as an error.

### What appears in Radar

Radar collects a current snapshot of configured unhealthy Datadog monitors every two minutes. It makes one monitor-search request per full refresh and does not query logs, traces, metrics, events, or monitor history. Each matching monitor becomes one Radar task:

- `Alert` becomes `immediate`.
- `Warn` and `No Data` become `attention` when included in `monitor_statuses`.
- A previously tracked monitor that no longer matches becomes `done` with reason `Datadog monitor recovered`.

### Monitor scope

Configure the scope as a Datadog monitor search query in the user config. Radar requires a non-empty query so it cannot accidentally collect every unhealthy monitor in an organization. `monitor_statuses` selects which states Radar appends to the query and ingests. It must contain one or more of `Alert`, `Warn`, and `No Data`; matching is case-insensitive. The default includes all three. Do not include alert status in `monitor_query`.

For example, this configuration ignores `No Data` monitors:

```yaml
datadog:
  monitor_query: tag:team:platform
  monitor_statuses:
    - Alert
    - Warn
```

### Credentials

The setup wizard stores keys in `secrets.yaml`. Alternatively, provide environment overrides:

Install [Pup 2.0.0+](https://github.com/DataDog/pup), then run `radar setup`.
Radar recommends Pup, shows the official installer, offers assisted installation,
rechecks PATH/version, and starts `pup auth login` in your terminal. Browser login
is never started by background collection. Setup verifies the named OAuth session,
`monitors_read`, the intended site and an actual scoped monitor search before
saving `enabled: true`. Failed installation/login can be retried or deferred.

```yaml
datadog:
  enabled: true
  site: datadoghq.eu
  org: radar
  monitor_query: tag:team:platform
  monitor_statuses: [Alert, Warn, No Data]
```

`org` is Pup's named session label. Choose the intended Datadog organization in
the browser when signing in. Radar always selects this label explicitly; Pup owns
credential storage and refresh. The default label is `radar`, and the default site
is `datadoghq.eu`. Supported regions retain their existing monitor URLs.

Each refresh checks Pup's version and OAuth status, then invokes one
`pup --org radar --no-envelope --output json api /api/v1/monitor/search` request.
Radar owns query/status construction and response completeness; Pup owns HTTP and
OAuth. No token is extracted and no credential file is read. Inherited static
bearer credentials are removed, named-session selection overrides saved static
bearer configuration, and API-key fallback is prevented before any request.

The old Radar Datadog API/application keys, token prompts and credential environment
overrides are removed. Existing credentials receive manual remediation errors;
there is no automatic migration or credential revocation. See the
[breaking-change guide](../cli-oauth-migration.md).


### Limits and notifications

The query is limited to 1,000 results in one request. If it matches more, collection is marked as an error and the previous Datadog tasks are retained; narrow `datadog.monitor_query`. Because Radar polls current state rather than alert events, an alert that both starts and recovers between full refreshes is intentionally not shown.

A newly collected monitor produces the normal Radar macOS notification. Clicking it opens the monitor directly in Datadog. Radar does not repeat the notification on every refresh while the monitor remains unhealthy.

## Collection and refs

Each full refresh performs one monitor search. Refs use `datadog:monitor:<id>` identities and direct monitor URLs. Alert emits immediate attention; Warn and No Data emit attention. A monitor missing from a complete response becomes done. Failed or truncated responses preserve previous observations.

The integration does not collect logs, traces, metrics, events, or historical transitions.

## Validation

```sh
go test ./internal/integration/datadog
```
