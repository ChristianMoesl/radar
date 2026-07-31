# Jira title references plan

## Status

Plan only. Implementation is deferred.

## Goal

Allow Radar to discover Jira issues referenced by key in an existing task title, even when the issue is unassigned or its issue type is outside the configured authoritative set.

A discovered Jira issue has one of two roles:

- **Authoritative**: participates fully in the Radar task's title, attention classification, identity, linking, and lifecycle.
- **Informational**: contributes only an inspectable Jira reference, status metadata, and openable URL.

Only authoritative references may replace a task title.

## Configuration

Rename `jira.issue_types` to `jira.authoritative_issue_types` directly. Do not retain an alias or compatibility fallback.

```json
{
  "jira": {
    "authoritative_issue_types": ["Task", "Bug", "Sub-task"],
    "status_mapping": {
      "In Progress": "in_progress",
      "In Review": "in_progress",
      "Blocked": "attention"
    },
    "unmapped_status": "low_priority"
  }
}
```

Semantics:

- When `authoritative_issue_types` is omitted, default it to `Task`, `Bug`, and `Sub-task`.
- An explicitly empty array means no automatically collected or title-discovered Jira issue type is authoritative.
- Issue-type names are trimmed and matched case-insensitively.
- Empty issue-type names are invalid.
- `status_mapping` continues to classify authoritative Jira work.
- `radar task attach-jira` remains explicitly authoritative regardless of issue type.

This is intentionally an incompatible configuration rename. Update the model and documentation directly; do not read `issue_types` as a legacy alias.

## Collection model

Jira collection has two inputs.

### 1. Assigned authoritative work

Search assigned, non-done Jira issues restricted to `authoritative_issue_types`.

- Every result is authoritative.
- When `authoritative_issue_types` is explicitly empty, skip the assigned-issue search rather than searching all issue types.
- The omitted default therefore collects assigned Tasks, Bugs, and Sub-tasks.

### 2. Title-discovered references

Scan current Radar tasks for Jira-style ticket keys. Fetch each distinct referenced issue directly by key, regardless of:

- assignee;
- configured authoritative issue types;
- whether it appeared in the assigned-issue search.

At minimum, discovery should inspect the current projected task title. It should also inspect the durable manual intent title and active source-ref titles so a Jira-supplied display title does not erase the text that originally established the reference.

For each fetched issue:

- If it was explicitly attached with `radar task attach-jira`, it is authoritative.
- Otherwise, if its Jira issue type matches `authoritative_issue_types`, it is authoritative.
- Otherwise, it is informational.

Deduplicate network requests by normalized Jira key. An issue found by both assigned search and title discovery is fetched/projected once as authoritative.

## First-class reference role

Represent authority explicitly instead of inferring it from source name, signal, or metadata.

Extend the normalized source-ref model with a role such as:

```text
authoritative | informational
```

Use one canonical spelling throughout protocol, integration, and state packages. Existing providers should emit `authoritative`; title-discovered out-of-scope Jira refs emit `informational`.

Because this changes persisted source-ref invariants, bump the state schema version and use the established `radar reset` path. Do not add state migration or legacy role inference.

## Projection rules

An authoritative Jira reference may:

- supply the projected Jira title;
- contribute its mapped attention signal;
- add ticket linking keys and become part of task identity;
- merge records that represent the same ticket;
- control remote completion and reopening;
- disable manual done/reopen when attached to a manual task.

An informational Jira reference may only:

- appear in task inspection and the source-ref list;
- expose its Jira URL;
- expose issue key, status, status category, issue type, and priority metadata;
- be opened through the existing source-link interaction.

An informational Jira reference must not:

- replace the projected task title;
- contribute `immediate`, `attention`, `in_progress`, `low_priority`, or `done` signals;
- change the task's canonical key;
- merge otherwise independent task records;
- complete or reopen a task;
- disable manual done/reopen;
- keep an otherwise completed or removed source-backed task active;
- bypass mute or deprioritization behavior.

## Attaching a discovered reference to its task

A title-discovered informational reference must stay attached to the task whose title mentioned it without using the Jira ticket key as task identity.

Add a generic target-task association to collected observations rather than teaching state to parse Jira keys. For example:

```text
Observation.TargetTaskID = <stable Radar task ID>
```

The collector carries this target into the candidate task, and state matches the stable numeric Radar ID before canonical/source-ref matching. This mechanism is generic and can support future informational references from other integrations.

Informational Jira refs need per-task identities so the same issue can be mentioned by multiple independent tasks without moving one source-ref record between them. For example:

```text
jira:mention:<radar-task-id>:<jira-key>
```

Store the canonical Jira issue identity and key in metadata for inspection, but do not expose ticket linking keys on the informational ref.

Authoritative title discoveries use the canonical Jira issue source identity and ticket linking keys. Target the mentioning task on first association so a manual task preserves its Radar ID. Reconciliation must merge duplicate records with the same authoritative ticket key while preserving the existing manual-record winner rules.

## Multiple Jira keys in one title

Fetch every distinct key in title order.

- Informational references all remain attached independently.
- Authoritative references all participate in lifecycle and linking.
- The first authoritative Jira key in title order is the deterministic primary Jira title provider.
- Completion requires every authoritative remote reference attached to the task to be complete, consistent with existing multi-remote lifecycle rules.

Document this behavior and keep ordering stable across refreshes.

## Refresh and removal behavior

Title discovery is dynamic:

- A currently mentioned key is refreshed on each full Jira collection.
- Removing a key from all title-bearing facts removes the corresponding title-discovered informational reference on a complete refresh.
- Explicit `attach-jira` associations remain durable even when the key is no longer present in a title.
- Configuration changes can promote a discovered issue from informational to authoritative or demote it back on the next full refresh.

Role changes must not leave an inactive authoritative ref controlling title, identity, manual lifecycle availability, or completion. Reconciliation should retire the previous derived ref cleanly and recompute the task from current active authoritative refs.

## Error handling and limits

- Normalize and deduplicate ticket keys before fetching.
- Use a bounded number of direct Jira requests per refresh.
- Keep deterministic request ordering for tests and logs.
- A failed direct fetch should produce a useful Jira source-status detail without deleting a previously known reference solely because of a transient failure.
- Missing or inaccessible issues should remain non-fatal to collection of other keys.
- Add a conservative maximum number of title-discovered Jira fetches per refresh and report truncation in source status rather than issuing unbounded requests.

## Implementation areas

### Configuration

- `internal/config/config.go`
  - Rename the field to `AuthoritativeIssueTypes` / `authoritative_issue_types`.
  - Add omitted defaults and preserve explicit-empty semantics.
  - Normalize validation and case-insensitive matching.
- `internal/config/config_test.go`
  - Cover omitted, explicit empty, configured values, invalid entries, and generated config.

### Jira integration

- `internal/integration/jira/issues.go`
  - Separate assigned search from direct issue fetching.
  - Return normalized issue type/status data needed for role classification.
  - Skip assigned search for an explicit empty authoritative set.
- `internal/integration/jira/source.go`
  - Discover ticket keys from previous tasks.
  - Union assigned and directly fetched issues.
  - Classify authoritative versus informational refs.
  - Preserve Done-category authority only for authoritative refs.
  - Target title-derived observations at the mentioning Radar task.

### Integration and protocol model

- `internal/integration/collect.go`
  - Add the generic target-task association to observations.
- `internal/protocol/protocol.go`
  - Add the first-class source-reference role.
- Source providers and contract tests
  - Populate and validate the authoritative role for existing source refs.

### State and projection

- `internal/state/`
  - Bump the state version.
  - Match targeted observations by stable Radar task ID.
  - Persist informational refs without using them for canonical identity.
  - Exclude informational refs from signal precedence, title precedence, lifecycle, remote-authority checks, and active-resource checks.
  - Handle role changes and duplicate authoritative ticket records deterministically.
  - Preserve explicit Jira attachment authority.

### Frontends and documentation

- `internal/tui/`
  - Label informational refs clearly during inspection, for example `Jira reference`.
  - Keep existing open-link behavior.
- `README.md`
  - Document the renamed option, default set, explicit-empty behavior, title discovery, and explicit attachment.
- `ARCHITECTURE.md`
  - Document source-reference roles and targeted informational observations.
- `docs/attention-algorithm.md`
  - State that informational refs never participate in title, attention, or lifecycle precedence.

## Testing strategy

### Config tests

- Omitted `authoritative_issue_types` defaults to Task, Bug, and Sub-task.
- Explicit `[]` stays empty.
- Configured names are matched case-insensitively after trimming.
- Empty names fail with a field-specific error.
- The removed `issue_types` key is not accepted as a compatibility alias.

### Jira source tests

- Assigned authoritative issues are collected through filtered JQL.
- Explicit empty authoritative types skip assigned collection.
- A title key fetches an unassigned issue directly.
- A configured issue type becomes authoritative.
- An unconfigured issue type becomes informational.
- Explicit attachment is authoritative despite issue type.
- Assigned and title-discovered duplicates issue only one direct projection.
- Done informational issues do not emit a done signal.
- Direct-fetch failures preserve other results and report partial status.

### State tests

- An informational Jira ref appears on its originating task without changing ID or title.
- Informational status mapping never promotes or deprioritizes the task.
- Informational Done does not complete the task.
- Manual done/reopen remains available with informational refs.
- Informational refs do not merge two tasks mentioning the same Jira issue.
- An authoritative discovered ref preserves the originating manual task ID and supplies the Jira title.
- Configuration-driven promotion/demotion recomputes title, identity, priority, and lifecycle correctly.
- Multiple keys use deterministic title precedence.
- Refresh removal deletes derived informational refs but keeps explicit attachments.

### TUI and server tests

- Inspection distinguishes informational Jira refs.
- Informational Jira links remain openable.
- Existing task create, done/reopen, attach, and priority mutations continue to work.

Run after each implementation phase:

```sh
make test
make install
```

## Acceptance criteria

- The public config key is `jira.authoritative_issue_types`; `jira.issue_types` is removed.
- Omission defaults to Task, Bug, and Sub-task.
- Explicit empty means no automatically discovered Jira refs are authoritative.
- Jira keys in tracked task titles are fetched even when assignment and type filters do not match.
- Configured issue types become authoritative; other discovered types remain informational.
- Only authoritative references can change title, attention, identity, merging, or lifecycle.
- Informational refs remain inspectable and openable without affecting task behavior.
- Explicit `attach-jira` remains authoritative.
- Full refresh, daemon restart, role changes, and title removal preserve stable Radar task identity and do not leave stale authority behind.

## Explicit non-goals

This change does not add:

- Jira issue creation;
- a compatibility alias for `jira.issue_types`;
- user-configurable authority per individual issue key;
- informational refs that affect attention or completion;
- automatic parsing of Jira keys from arbitrary comments or descriptions;
- a second explicit Jira attachment command.
