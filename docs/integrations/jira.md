# Jira integration

Jira supplies work-tracker issues through TWG 1.3.8+ using a named OAuth profile.
TWG owns credential storage and refresh.

## Capabilities

`Source`, `StatusReporter`, `Reconciler`, and the `WorkTracker` composite role.

## Setup and behavior

Run `radar setup` to configure this integration, or edit the settings below.
Omitting `jira.enabled` detects configured prerequisites; `false` disables
collection and `true` reports missing prerequisites as an error.

### What appears in Radar

Radar collects assigned Jira Cloud issues and issues referenced by other collected work. For example, a GitHub PR can supply an exact configured key such as `ABC-123` through its branch, title, description, or commits. Jira uses the GitHub provider's resolved keys, so a conflicting display title cannot override branch ownership. New references to unassigned issues are fetched on the following refresh.

Radar links Jira issues and GitHub PRs itself. **GitHub for Atlassian is not required.** See [GitHub linking rules](github.md#jira-links) for branch priority and manual bindings. A link stored only in Jira's Development panel must be recreated with a reference or an explicit Radar binding. Radar does not read Jira descriptions or comments to infer PR links.

### OAuth connection

`radar setup` offers the official TWG installer, rechecks its version, discovers
the site's Cloud ID without credentials and launches interactive OAuth login.
It verifies the profile, site, account and a native issue read, then asks you to
confirm the displayed account. Failed installation/login/reads can be retried or
deferred; deferred Jira is disabled rather than reported as connected.

Only non-secret selectors are saved:

```yaml
jira:
  enabled: true
  base_url: https://example.atlassian.net
  cloud_id: discovered-cloud-id
  profile: radar
  account_id: verified-account-id
```

Use a dedicated named OAuth profile; `default` and saved PAT profiles are rejected.
Background collection never opens a browser. Before each read Radar removes
inherited TWG auth/mode/endpoint overrides, pins the profile/Cloud ID and checks
OAuth metadata plus actual identity/connectivity. CLI storage selectors and the
runtime's proxy/CA trust remain available. Another program can still change the
saved profile between checking and requesting; this accepted limitation is
[documented here](../twg-oauth-blocker.md).

Old email/token/API-base fields, the entire Jira secret mapping and retired
`RADAR_JIRA_*` connection overrides are rejected with manual instructions. Follow
the [cutover guide](../cli-oauth-migration.md#jira-manual-cutover); no credentials
are migrated automatically and no authenticated HTTP fallback remains.

### Issue types and status mapping

`jira.authoritative_issue_types` controls which automatically collected or reference-discovered Jira issues can control a Radar task. It defaults to `Story`, `Task`, `Bug`, and `Sub-task`:

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

Names are trimmed and matched case-insensitively. An explicitly empty array skips assigned Jira search and makes every automatically reference-discovered issue informational. Omitting the option uses the four default types. The former `jira.issue_types` option is not supported.

Authoritative Jira refs can provide the task title, identity, attention, linking, and contributing lifecycle. An out-of-scope title discovery is shown as an informational **Jira reference** with its URL, status, issue type, priority, and status category, but it cannot rename, merge, reprioritize, complete, or reopen the task. Removing a key from all current source facts removes its derived reference on a complete refresh. When a Jira ref joins an Obsidian-authored task, authoritative active work reopens a completed note. Confirmed completion of all contributing work items is written back to that note before Radar projects it as done, unless its completion baseline protects an explicit reopening.

Every distinct resolved key is collected in deterministic source order, with up to 50 keys fetched through one batched Jira search per refresh. Radar runs that batch concurrently with the assigned-issue search and deduplicates their results. Informational refs remain independent; all authoritative refs participate in linking and lifecycle, the first authoritative key supplies the Jira title, and completion requires every authoritative remote ref to be done. A failed batch or requested keys missing from its result are non-fatal, retain previously known refs, and are reported in Jira source status.

Jira status names are trimmed and matched case-insensitively. Mapping targets may be `low_priority`, `in_progress`, `attention`, or `immediate`; Jira's Done category remains authoritative only for authoritative refs. By default, `In Progress` and `In Review` are in progress and every other authoritative non-done status is low priority. An explicitly empty `status_mapping` sends every authoritative non-done issue to `unmapped_status`.

## Collection and refs

Full refreshes collect assigned authoritative issue types and reference-discovered issue keys. Authoritative refs use `jira:issue:<KEY>` identities and contributing work-item lifecycle authority. Non-authoritative title mentions use task-scoped informational IDs and never contribute linking or lifecycle state.

Jira consumes generic configured mark keys from other providers; it does not parse GitHub identities or call Jira's Development endpoints. Missing active issues are reconciled to done only after a successful remote check. Native query/get responses must have the qualified flattened shape and complete evidence. Cursor pagination is bounded; repeated cursors, duplicate issues, malformed pages and truncated/partial results fail visibly. Failed or incomplete batches preserve previous refs and report partial status.

## Validation

```sh
go test ./internal/integration/jira
```
