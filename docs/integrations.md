# Integration architecture and capabilities

Radar integrations are source-compiled Go packages. Radar does not load plugins dynamically: there is no plugin discovery, manifest format, Go `plugin`, or subprocess plugin protocol. Register each integration once in `internal/app.DefaultIntegrations`; the registry discovers its capabilities through interfaces.

## Documentation layout

This document is the canonical reference for the shared integration boundary, capability interfaces, source-ref contract, and current capability matrix.

Use these locations for integration documentation:

- `docs/integrations.md`: shared contracts, capability catalog, and integration index.
- `docs/integrations/<name>.md`: the authoritative behavior of one implemented integration, including configuration, credentials, emitted refs, authority, lifecycle, actions, mutations, failure handling, limits, and operational validation.
- `docs/<feature>-plan.md`: proposed or historical implementation plans. Plans describe change; they do not replace the permanent integration document after implementation.

Each implemented integration document should answer:

1. What source or tool does the integration represent?
2. Which capability interfaces does it implement?
3. How is it configured and authenticated?
4. What does it collect, and how often?
5. Which stable IDs, entity IDs, canonical keys, and linking keys does it emit?
6. Which refs affect title, attention, lifecycle, and completion?
7. Which mutations, actions, workspace operations, or cleanup operations does it own?
8. How are partial collection, deletion, reconciliation, limits, and rate limits handled?
9. Which user workflows and commands depend on it?
10. How can its behavior be tested and operationally validated?

Implemented integrations:

- [Obsidian task authoring](integrations/obsidian.md)
- [GitHub](integrations/github.md)
- [Jira](integrations/jira.md)
- [Datadog](integrations/datadog.md)
- [Git](integrations/git.md)
- [tmux](integrations/tmux.md)
- [SBX](integrations/sbx.md)
- [Workspace reconciliation](workspace-reconciliation.md)

## Integration boundary

Every integration implements `Integration` by returning a descriptor with its stable name, display label, and ordering. It then implements the smallest capability set it needs:

- `Source`: collects source facts as observations.
- `StatusReporter`: reports whether collection can run.
- `LocalSource`: marks sources that can be refreshed frequently without remote API calls.
- `Reconciler`: resolves disappeared remote refs into observations, including `done` signals.
- `ActionProvider`: exposes source-owned actions for source refs.
- `CleanupProvider`: previews and cleans up source-owned local resources through the shared cleanup service.
- `WorkspaceProvider`: owns local code workspace lifecycle. Git is the active provider.
- `WorkspaceManager`: owns managed-workspace discovery, sessions, inspection, and reconciliation. Workspace is the active provider.
- `WorkspaceSeedProvider`: translates a source ref into a provider-owned workspace creation seed. GitHub and Obsidian implement it.
- `MultiplexerProvider`: owns interactive sessions, current-client detection, task targeting, and session matching. tmux is the active provider.
- `RuntimeProvider`: resolves provider-owned runtime resource names. SBX is the active provider.
- `ActivityPublisher`: receives generic busy/idle events. tmux publishes those events as pane state.
- `TaskFilterProvider`: filters projected tasks using provider-owned configuration and semantics. GitHub implements it.
- `InteractiveAuthenticator`: performs provider-owned interactive startup authentication. SBX implements it.
- `RateLimitReporter`: exposes provider diagnostics without concrete CLI imports. GitHub implements it.
- `DevelopmentLinkResolver`: translates structured cross-system development links into provider-owned identities and linking keys. GitHub resolves Jira development links.
- `CodeReviewProvider`: combines source and reconciliation capabilities for a code-review system. GitHub is the active provider.
- `WorkTracker`: combines source and reconciliation capabilities for a work tracker. Jira is the active provider.
- Runtime integrations may combine source, action, cleanup, and resource-name capabilities for an executable local environment.
- `TaskAuthoringProvider`: creates source-owned tasks and changes their open/done lifecycle and normal/urgent priority. The registry requires exactly one provider; Obsidian is the active provider.

## Current capability matrix

| Integration | Source | Status | Local | Reconcile | Authoring | Actions | Cleanup | Workspace role | Other capabilities |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Obsidian | yes | yes | yes | no | yes | yes | no | seed | — |
| GitHub | yes | yes | no | yes | no | no | no | seed | filters, rate limits, development-link resolver |
| Jira | yes | yes | no | yes | no | no | no | no | work tracker |
| Datadog | yes | yes | no | yes | no | no | no | no | — |
| Workspace | yes | yes | yes | no | no | no | yes | manager | managed anchor |
| Git | yes | yes | yes | no | no | no | yes | code workspace | — |
| tmux | yes | yes | yes | no | no | no | yes | no | multiplexer, activity publisher |
| SBX | yes | yes | yes | no | no | yes | yes | no | runtime naming, authentication |

The matrix describes compiled capabilities, not whether a source can run in the current environment. `StatusReporter` reports missing tools, credentials, configuration, or API availability at runtime.

## Source facts

Integrations emit `integration.Observation` values. Radar core projects observations into tasks, links refs, filters tasks, owns task IDs, and persists state.

Every emitted `protocol.SourceRef` must have:

1. Stable source-owned `ID`.
2. Stable source-owned `EntityID`; different representations of one external entity share it.
3. `Source` equal to the integration name.
4. Non-empty source-owned `Kind`.
5. An explicit `Role`: normally `authoritative`, or intentionally `informational` for an inspect/open-only association.
6. An explicit lifecycle (`work_item`, `workspace`, or `resource`) for authoritative refs.
7. Explicit lifecycle authority: `primary`, `contributing`, or `none`.
8. `CanonicalKey` when an authoritative ref can become a standalone task.
9. `LinkingKeys` for authoritative joins such as `mark:<KEY>`, `workspace:<path>`, or `branch:<repo>:<branch>`.
10. `ProvidesWorkspace` when the represented entity owns a persistent local working directory. Such a ref must be authoritative, have a non-empty absolute `Path`, and include the cleaned `workspace:<path>` linking key.
11. `Busy` while the authoritative source is actively processing work. Busy is transient task activity and does not change attention or lifecycle.
12. `InUse` when an occupied local resource must block automatic cleanup.
13. `Authored` when the task-authoring provider owns mutations for the ref.
14. `WorkspaceEntry`, `WorkspaceID`, and `WorkspaceAnchorPath` when a provider owns workspace entry, grouping, or a canonical source artifact.
15. Generic presentation hints when the source owns title precedence or workspace naming.
16. `URL` only when it is directly openable.
17. `Acknowledgement` only when the provider exposes a cursor-based activity acknowledgement contract.
18. `RetainInactive` only when a provider's terminal source facts must remain in done-task history; local/deletable refs leave it false.

Informational refs cannot be busy because their facts do not participate in task projection. Workspace capability is independent of lifecycle and lifecycle authority. A registered Radar anchor provides the logical workspace, including when it has no Git members. Registered Git members link through `workspace-group:<id>` and the persisted task key but do not compete with the anchor as the preferred workspace. Unmanaged Git worktrees continue to provide their own workspace paths. Obsidian notes own work-item lifecycle, while tmux sessions and SBX sandboxes consume anchor paths as resources. Workspace capability does not emit a signal or change attention by itself.

The collector stamps source label and display order from the integration descriptor. Informational refs must not emit signals, busy activity, lifecycle authority, canonical keys, linking keys, or workspace capability. An observation may set `TargetTaskID` to associate such a ref with a stable existing Radar task without turning source metadata into task identity. Do not invent Radar task IDs in integrations or parse another source's IDs or metadata in core state. Keep source-specific behavior tested in the source package. Core packages consume only interfaces and generic protocol fields. `internal/integration/contracttest` rejects concrete provider imports and direct `gh`, `git`, `tmux`, or `sbx` command execution outside the integration boundary.

## Cleanup providers

`internal/cleanup.Service` asks registered providers for targets in registration order and executes a confirmed or automatically selected preview sequentially in that same order. Every target's `Source` must match the provider's source name. Providers also emit a user-facing `Description`, opaque `ResourceID`, generic workspace path/group traits, provider-owned `Operation` values, and typed `Safety` messages. Core cleanup and garbage collection never interpret session names, sandbox names, branch-publication fields, or provider metadata.

Providers receive an explicit `integration.CleanupRequest`:

```go
type CleanupRequest struct {
    Target protocol.CleanupTarget
    Force  bool
}
```

The provider owns removal of only its resource type. tmux removes sessions, SBX removes sandboxes, Git removes worktrees and eligible local branches, and Workspace removes an empty managed anchor. Merely observed worktrees, protected branches, remote branches, and canonical Obsidian notes are preserved. Manual cleanup passes `Force: true` after user confirmation. Automatic garbage collection passes `Force: false` and skips any target whose provider emitted a safety item with `BlocksAutomatic`.

The active provider order is tmux, SBX, Git, then Workspace. Processes stop first, members disappear before the anchor, and unknown anchor contents block removal. Do not orchestrate another integration's resources from a provider.

## Checklist

1. Choose capabilities.
2. Add `internal/integration/<name>`.
3. Implement `Source` first.
4. Emit stable source refs and observations.
5. Add source ref contract tests and provider-specific parser/API tests.
6. Register the package once in `internal/app.DefaultIntegrations`.
7. Add only the minimum config needed.
8. Add `docs/integrations/<name>.md` using the documentation questions above, and update the capability matrix.
9. Avoid fallback chains, aliases, or duplicate command paths.

## Examples

### Zellij

Capabilities:

- `Source`
- `MultiplexerProvider`

Source refs:

- `zellij:session:<stable-session-id>`

Linking keys:

- `mark:<KEY>` from session name/path when present
- `workspace:<path>` when current/session path is known

### GitLab

Capabilities:

- `Source`
- `Reconciler`
- code review behavior through the same source/reconcile interfaces GitHub uses

Source refs:

- `gitlab:merge_request:<host>/<group>/<project>:<iid>`

Linking keys:

- `mark:<KEY>` from title/branch
- `branch:<host>/<group>/<project>:<branch>`
