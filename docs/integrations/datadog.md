# Datadog integration

Datadog supplies actionable monitor work items through the monitor search API.

## Capabilities

`Source`, `StatusReporter`, and `Reconciler`.

## Configuration and authentication

`datadog.monitor_query` scopes collection and `datadog.monitor_statuses` selects `Alert`, `Warn`, and `No Data`. Credentials come from `RADAR_DATADOG_API_KEY` and `RADAR_DATADOG_APP_KEY`; Radar does not persist them. Omitted `datadog.enabled` activates collection only when both credentials and a monitor query exist; `false` skips collection and reconciliation, and `true` reports missing prerequisites as an error. Radar never invents a query or defaults to collecting every monitor.

## Collection and refs

Each full refresh performs one monitor search. Refs use `datadog:monitor:<id>` identities and direct monitor URLs. Alert emits immediate attention; Warn and No Data emit attention. A monitor missing from a complete response becomes done. Failed or truncated responses preserve previous observations.

The integration does not collect logs, traces, metrics, events, or historical transitions.

## Validation

```sh
go test ./internal/integration/datadog
```
