# Jira integration

Jira supplies work-tracker issues through Jira Cloud REST APIs.

## Capabilities

`Source`, `StatusReporter`, `Reconciler`, and the `WorkTracker` composite role.

## Setup and behavior

Run `radar setup` to configure this integration, or edit the settings below.
Omitting `jira.enabled` detects configured prerequisites; `false` disables
collection and `true` reports missing prerequisites as an error.

### What appears in Radar

Radar collects authoritative assigned Jira Cloud work and discovers configured linking marks such as `ABC-123` in existing task titles. Title discovery fetches issues directly even when they are unassigned or outside the configured authoritative issue types.

For authoritative issues, Radar also reads Jira's structured Development pull-request relationships. A valid GitHub relationship contributes the PR identity and its exact repository and source branch as linking keys. This joins the Jira issue to the GitHub PR and matching worktree even when their titles and branch names omit the Jira key. Radar does not inspect PR bodies, Jira descriptions, comments, or commit messages for this link. Jira installations without the Development endpoints keep the normal issue collection behavior.

### Credentials

The wizard stores `jira.base_url`, `jira.email`, and the discovered `jira.cloud_id` in `config.yaml`, with `jira.api_token` in `secrets.yaml`. It discovers the Cloud ID from the site's `/_edge/tenant_info` endpoint without sending the token, then verifies authentication with Jira's `/myself` endpoint. A failed check stops setup without saving configuration.

Environment variables remain supported and override the corresponding stored values:

```sh
RADAR_JIRA_BASE_URL="https://your-site.atlassian.net"
RADAR_JIRA_EMAIL="you@example.com"
RADAR_JIRA_API_TOKEN="..."
RADAR_JIRA_CLOUD_ID="..."
# alternatively: RADAR_JIRA_API_BASE_URL="https://api.atlassian.com/ex/jira/<cloud-id>/rest/api/3"
```

### Issue types and status mapping

`jira.authoritative_issue_types` controls which automatically collected or title-discovered Jira issues can control a Radar task. It defaults to `Story`, `Task`, `Bug`, and `Sub-task`:

```yaml
jira:
  authoritative_issue_types:
    - Story
    - Task
    - Bug
    - Sub-task
  status_mapping:
    In Progress: in_progress
    In Review: in_progress
  unmapped_status: low_priority
```

Names are trimmed and matched case-insensitively. An explicitly empty array skips assigned Jira search and makes every automatically title-discovered issue informational. Omitting the option uses the four default types. The former `jira.issue_types` option is not supported.

Authoritative Jira refs can provide the task title, identity, attention, linking, and contributing lifecycle. An out-of-scope title discovery is shown as an informational **Jira reference** with its URL, status, issue type, priority, and status category, but it cannot rename, merge, reprioritize, complete, or reopen the task. Removing a key from all current title-bearing facts removes its derived reference on a complete refresh. When a Jira ref joins an Obsidian-authored task, authoritative active work reopens a completed note. Confirmed completion of all contributing work items is written back to that note before Radar projects it as done, unless its completion baseline protects an explicit reopening.

Every distinct key in a title is collected in deterministic title order, with up to 50 keys fetched through one batched Jira search per refresh. Radar runs that batch concurrently with the assigned-issue search and deduplicates their results. Informational refs remain independent; all authoritative refs participate in linking and lifecycle, the first authoritative key supplies the Jira title, and completion requires every authoritative remote ref to be done. A failed batch or requested keys missing from its result are non-fatal, retain previously known refs, and are reported in Jira source status.

Jira status names are trimmed and matched case-insensitively. Mapping targets may be `low_priority`, `in_progress`, `attention`, or `immediate`; Jira's Done category remains authoritative only for authoritative refs. By default, `In Progress` and `In Review` are in progress and every other authoritative non-done status is low priority. An explicitly empty `status_mapping` sends every authoritative non-done issue to `unmapped_status`.

## Collection and refs

Full refreshes collect assigned authoritative issue types and title-discovered issue keys. Authoritative refs use `jira:issue:<KEY>` identities and contributing work-item lifecycle authority. Non-authoritative title mentions use task-scoped informational IDs and never contribute linking or lifecycle state.

Structured Development pull-request records are passed to registered development-link resolvers. Jira does not recognize GitHub URLs, IDs, or linking-key formats itself. Missing active issues are reconciled to done only after a successful remote check. Failed or incomplete batches preserve previous refs and report partial status.

## Validation

```sh
go test ./internal/integration/jira
```
