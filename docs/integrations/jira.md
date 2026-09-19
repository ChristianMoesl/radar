# Jira integration

Jira supplies work-tracker issues through Jira Cloud REST APIs.

## Capabilities

`Source`, `StatusReporter`, `Reconciler`, and the `WorkTracker` composite role.

## Configuration and authentication

`RADAR_JIRA_BASE_URL`, `RADAR_JIRA_EMAIL`, `RADAR_JIRA_API_TOKEN`, and either `RADAR_JIRA_CLOUD_ID` or `RADAR_JIRA_API_BASE_URL` configure API access. Omitted `jira.enabled` activates collection when these prerequisites exist; `false` skips collection and reconciliation, and `true` reports missing prerequisites as an error. Authoritative issue types, status mapping, and the unmapped-status policy are provider-owned settings.

## Collection and refs

Full refreshes collect assigned authoritative issue types and title-discovered issue keys. Authoritative refs use `jira:issue:<KEY>` identities and contributing work-item lifecycle authority. Non-authoritative title mentions use task-scoped informational IDs and never contribute linking or lifecycle state.

Structured Development pull-request records are passed to registered development-link resolvers. Jira does not recognize GitHub URLs, IDs, or linking-key formats itself. Missing active issues are reconciled to done only after a successful remote check. Failed or incomplete batches preserve previous refs and report partial status.

## Validation

```sh
go test ./internal/integration/jira
```
