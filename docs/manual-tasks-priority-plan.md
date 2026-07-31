# Manual tasks and priority plan

## Status

Plan only. Implementation is intentionally split into three independently useful changes:

1. Configurable Jira status-to-attention mapping.
2. Radar-owned manual tasks with done/reopen and Jira attachment.
3. A persistent manual urgent-priority toggle.

## Product model

A Radar task is the durable unit of work. Jira issues, GitHub pull requests, Git worktrees, tmux sessions, SBX sandboxes, and links are references that can accumulate around that task; none of them is required for the task to exist.

```text
Radar task
├── stable Radar ID
├── optional Radar-owned intent
├── lifecycle state
├── optional manual priority override
└── zero or more source references
```

A task can therefore evolve without changing identity:

```text
manual task
→ Jira-backed task
→ workspace/PR-backed task
→ done
```

The existing ticket key remains the strongest automatic linking key, but it is not the task's durable identity. The Radar task ID survives attachment and merging.

## Attention model

Radar keeps its existing display categories:

- `immediate`: urgent action is required.
- `attention`: something needs a response.
- `in_progress`: work has actually started.
- `low_priority`: tracked work that is not currently active.
- `done`: completed work retained temporarily in the UI.

Source-derived active signals are ordered as:

```text
immediate > attention > in_progress > low_priority
```

Completion remains terminal and overrides active priority. A manual urgent override can promote an active task to `immediate`, but it cannot reopen a done task.

## Phase 1: configurable Jira attention mapping

### Goal

Stop treating every assigned, non-done Jira issue as `in_progress`. Classify each issue from its Jira workflow status.

### Configuration

Extend `jira` configuration with an exact status-name mapping and an unmapped fallback:

```json
{
  "jira": {
    "issue_types": [],
    "status_mapping": {
      "In Progress": "in_progress",
      "In Review": "in_progress",
      "Blocked": "attention"
    },
    "unmapped_status": "low_priority"
  }
}
```

Built-in defaults:

```text
In Progress → in_progress
In Review   → in_progress
all other assigned, non-done statuses → low_priority
```

Allowed configured targets:

- `low_priority`
- `in_progress`
- `attention`
- `immediate`

`done` is deliberately not configurable. Jira's Done status category remains authoritative for completion and the existing done reconciliation continues to own that transition.

Status names should be trimmed and matched case-insensitively. Configuration loading must reject empty status names, unsupported target values, and an unsupported `unmapped_status`. An omitted mapping uses the built-in defaults; an explicitly empty mapping is valid and sends every non-done status to the configured fallback.

### Projection behavior

The Jira source emits one observation per issue using the mapped signal instead of assigning `in_progress` to the whole result set. The source ref keeps the original Jira status for inspection and uses it as the classification reason.

When Jira is linked to other sources, the strongest active source signal wins. Examples:

- Jira `Selected for Development` alone → `low_priority`.
- The same issue with a worktree or open PR → `in_progress`.
- Actionable PR feedback → `attention`.
- A configured Jira `Blocked` status → `attention`.

### Implementation areas

- `internal/config/config.go`: Jira mapping fields, defaults, and validation.
- `internal/integration/collect.go`: add a first-class low-priority work signal.
- `internal/integration/jira/source.go`: classify each collected issue.
- `internal/integration/jira/issues.go`: expose/normalize the Jira status used by the classifier.
- `README.md` and `docs/attention-algorithm.md`: document mapping and precedence.

### Acceptance criteria

- Assigned Jira issues in `In Progress` and `In Review` appear in `in_progress` by default.
- Other assigned, non-done Jira issues appear in `low_priority` by default.
- A custom mapping takes effect after refresh without modifying persisted source facts.
- Invalid mappings fail config validation with a useful field-specific error.
- Jira Done-category issues still become `done`, regardless of mapping configuration.
- Higher-priority linked GitHub/local signals still promote a low-priority Jira task.

## Phase 2: manual tasks and lifecycle

### Goal

Allow Radar to track small or initially fuzzy work that has no repository, Jira issue, or PR.

Examples:

- `Refine the authentication epic in Jira`
- `Write the release process in Notion`

### Persistent model

Extend `TaskRecord` with Radar-owned intent and lifecycle data rather than introducing a synthetic integration source. A manual task must remain projectable when it has no `SourceRef` records.

The intent preserves at least:

- The original user-entered title.
- Creation/update timestamps.
- Whether the task is manually complete.
- Explicit association keys used to attach future references.

A manual task's natural active category is `low_priority`.

Display-title precedence is:

1. An attached Jira issue supplies the normal projected title.
2. Otherwise, another authoritative remote source may supply the title according to existing projection rules.
3. Otherwise, use the Radar-owned intent title.

The original intent remains visible during inspection after a source title takes over.

### Operations

Provide one canonical task command namespace:

```sh
radar task create --title "Write the release process in Notion"
radar task done <task-id>
radar task reopen <task-id>
radar task attach-jira <task-id> DPSCAP-123
```

The TUI adds:

- `n`: enter a title and create a manual task.
- `d`: mark a selected manual-only task done, or reopen a selected manually completed task.

The daemon protocol should use explicit structured methods and fields for these mutations rather than encoding new mutations into method-name strings.

### Completion rules

- A manual-only task can be marked done explicitly.
- Reopening it returns it to its natural `low_priority` category.
- Once an authoritative Jira or GitHub reference is attached, remote completion rules take over; manual done/reopen is no longer offered for that task.
- A done task remains visible for the existing done-retention period.
- Remaining local resources do not reopen remotely completed work.

### Jira attachment and identity preservation

Attaching `DPSCAP-123` adds the generic association key `ticket:DPSCAP-123` to the manual task. The Jira source ref and later worktrees, tmux/SBX sessions, branches, and PRs can then join through existing source-owned linking keys.

If Jira collection has already created a separate task record for that key, attachment merges the records with these rules:

- Preserve the manual task's numeric Radar ID.
- Preserve its original intent.
- Move each source ref to exactly one task record.
- Use the Jira-backed projection after the merge.
- Do not leave a duplicate user-facing task.

Creating a Jira issue through Radar is not part of this phase. The first version attaches an issue that was created in Jira and will be collected normally.

### State and schema

Update the persisted state model directly and bump its version if the new invariants require it. Do not add legacy aliases or migration fallbacks; an incompatible existing state should use the established `radar reset` path.

State mutations must be atomic under the store lock, save immediately, bump the daemon revision, and update watchers just like collected changes.

### Implementation areas

- `internal/state/`: manual record creation, lifecycle mutation, projection without source refs, association, and merge behavior.
- `internal/protocol/`: structured mutation requests and response errors.
- `internal/server/` and `internal/client/`: daemon operations.
- `cmd/radar/`: scriptable `radar task` commands.
- `internal/tui/`: title-entry flow, `n`, `d`, inspect details, and help text.
- `README.md`, `ARCHITECTURE.md`, and `docs/attention-algorithm.md`: update the task identity and lifecycle documentation.

### Acceptance criteria

- A manual task can be created without any source refs and survives daemon restart and refresh.
- Its Radar ID remains stable.
- It appears naturally in `low_priority`.
- It can be marked done and reopened.
- Refreshing integrations does not delete or accidentally complete it.
- Attaching a Jira key preserves the manual task ID and automatically links later ticket-key-bearing refs.
- Attaching a Jira issue already projected separately produces one merged task.
- Remote lifecycle becomes authoritative after Jira/GitHub attachment.
- Existing collected-only tasks continue to behave as before.

## Phase 3: manual priority modification

### Goal

Allow the user to temporarily make a task urgent and later restore its normal source-derived classification.

### Model

Store an optional Radar-owned priority override on `TaskRecord`:

```text
none | urgent
```

Do not store `low_priority` as an override. Clearing `urgent` restores the natural classification:

- A manual-only task returns to `low_priority`.
- An active mapped Jira task returns to `in_progress`.
- A task with actionable PR feedback returns to `attention`.

This avoids a low-priority override incorrectly suppressing new source information.

### Interaction

In the TUI:

```text
p    toggle manually urgent / natural priority
```

The selected task should immediately move to the appropriate section while preserving selection where practical. Inspection should indicate when urgency was set manually.

Use deterministic scriptable commands rather than a CLI toggle:

```sh
radar task priority <task-id> urgent
radar task priority <task-id> normal
```

### Precedence

Apply classification in this order:

1. Terminal completion → `done`.
2. Muted task → hidden.
3. Manual urgent override → `immediate`.
4. Otherwise use the strongest natural source signal.
5. Existing deprioritization policy may lower naturally classified work, but an explicit manual urgent override wins over general deprioritization rules.

A task that is already naturally `immediate` does not need an override. Pressing `p` should not offer a misleading way to lower an external immediate signal; it either sets/clears the manual override or reports that the task is already immediate because of a source.

Changing priority is a user-initiated action and should not send an OS notification to that same user. Later external transitions continue to use the existing notification rules.

### Implementation areas

- `internal/state/`: persist and project the urgent override.
- `internal/protocol/`, `internal/server/`, and `internal/client/`: priority mutation.
- `internal/filters/`: make explicit urgency precedence over deprioritization clear without bypassing mute.
- `internal/notification/`: suppress self-notification for the mutation.
- `cmd/radar/`: deterministic priority command.
- `internal/tui/`: `p` handling, result feedback, selection stability, inspect indicator, and help text.

### Acceptance criteria

- Pressing `p` on a low-priority manual task moves it to `immediate`.
- Pressing `p` again returns it to `low_priority`.
- Clearing urgency on a Jira/workspace-backed task restores its source-derived category rather than forcing low priority.
- The override survives refresh and daemon restart.
- Done tasks remain done and muted tasks remain hidden.
- Explicit urgency wins over configured deprioritization.
- The manual toggle does not generate an OS notification.

## Testing strategy

Each phase should land with focused unit and integration tests.

### Phase 1

- Config defaulting and validation tests.
- Case-insensitive Jira status matching tests.
- Unmapped fallback tests.
- Per-issue mixed-status collection tests.
- Strongest-linked-signal and Done-category regression tests.

### Phase 2

- State creation, persistence, revision, and no-source projection tests.
- Done/reopen and retention tests.
- Full/local refresh reconciliation tests proving manual records survive.
- Jira attachment and duplicate-record merge tests.
- Protocol/server command tests.
- Bubble Tea tests for title entry and done/reopen interactions.

### Phase 3

- Priority persistence and projection-precedence tests.
- Jira/manual/GitHub natural-category restoration tests.
- Mute/deprioritize interaction tests.
- Notification suppression tests.
- Bubble Tea tests for the `p` toggle and selection behavior.

Run the full suite after every phase:

```sh
make test
```

## Explicit non-goals

These changes do not introduce:

- A separate inbox, backlog, planned, or todo category.
- Priorities, due dates, checklists, or a general-purpose project-management system.
- A synthetic manual-source integration.
- Jira issue creation from Radar.
- Notion synchronization.
- Manual completion overrides for active remote Jira/GitHub work.
- Backwards-compatibility shims for an incompatible persisted state schema.

Free-form links, Jira creation, and splitting one fuzzy task into several Jira tasks can be considered later after the core workflow is proven.
