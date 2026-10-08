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

```sh
RADAR_DATADOG_API_KEY="..."
RADAR_DATADOG_APP_KEY="..."
RADAR_DATADOG_SITE="datadoghq.eu"
```

`secrets.yaml` stores `datadog.api_key` and `datadog.app_key`; the application key needs monitor-read permission. `config.yaml` stores `datadog.site`, such as `datadoghq.eu` or `us3.datadoghq.com`. The wizard accepts the site hostname or an API endpoint like `https://api.datadoghq.eu/`. Existing `RADAR_DATADOG_API_KEY`, `RADAR_DATADOG_APP_KEY`, and `RADAR_DATADOG_SITE` environment variables take precedence over stored values. The default site is `datadoghq.eu`. Tokens are never stored in `config.yaml`.

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
