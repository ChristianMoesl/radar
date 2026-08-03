# Adding an integration

Radar integrations are source-compiled Go packages. Radar does not load plugins dynamically: there is no plugin discovery, manifest format, Go `plugin`, or subprocess plugin protocol. Register each integration once in `internal/app.DefaultIntegrations`; the registry discovers its capabilities through interfaces.

## Integration boundary

Every integration implements `Integration` by returning a descriptor with its stable name, display label, and ordering. It then implements the smallest capability set it needs:

- `Source`: collects source facts as observations.
- `StatusReporter`: reports whether collection can run.
- `LocalSource`: marks sources that can be refreshed frequently without remote API calls.
- `Reconciler`: resolves disappeared remote refs into observations, including `done` signals.
- `AssociationProvider`: validates and normalizes an explicit task association such as `attach-jira` into a generic durable association.
- `ActionProvider`: exposes source-owned actions for source refs.
- `CleanupProvider`: previews and cleans up source-owned local resources through the shared cleanup service.
- `WorkspaceProvider`: owns local code workspace lifecycle. Git is the active provider.
- `MultiplexerProvider`: owns interactive sessions. tmux is the active provider.

## Source facts

Integrations emit `integration.Observation` values. Radar core projects observations into tasks, links refs, filters tasks, owns task IDs, and persists state.

Every emitted `protocol.SourceRef` must have:

1. Stable source-owned `ID`.
2. Stable source-owned `EntityID`; different representations of one external entity share it.
3. `Source` equal to the integration name.
4. Non-empty source-owned `Kind`.
5. An explicit `Role`: normally `authoritative`, or intentionally `informational` for an inspect/open-only association.
6. An explicit lifecycle (`work_item`, `workspace`, or `resource`) for authoritative refs.
7. `CanonicalKey` when an authoritative ref can become a standalone task.
8. `LinkingKeys` for authoritative joins such as `mark:<KEY>`, `workspace:<path>`, or `branch:<repo>:<branch>`.
9. Generic presentation hints when the source owns title precedence or workspace naming.
10. `URL` only when it is directly openable.

The collector stamps source label and display order from the integration descriptor. Informational refs must not emit signals, lifecycle authority, canonical keys, or linking keys. An observation may set `TargetTaskID` to associate such a ref with a stable existing Radar task without turning source metadata into task identity. Do not invent Radar task IDs in integrations or parse another source's IDs or metadata in core state. Keep source-specific behavior tested in the source package.

## Cleanup providers

`internal/cleanup.Service` asks registered providers for targets in registration order and executes a confirmed or automatically selected preview sequentially in that same order. Every target's `Source` must match the provider's source name.

Providers receive an explicit `integration.CleanupRequest`:

```go
type CleanupRequest struct {
    Target protocol.CleanupTarget
    Force  bool
}
```

The provider owns removal of only its resource type. tmux removes sessions, SBX removes sandboxes, and Git removes worktrees while preserving branches. Manual cleanup passes `Force: true` after user confirmation; automatic workspace garbage collection passes `Force: false`. Providers that do not have a force concept ignore the option and should treat already-missing resources as cleaned.

The active provider order is tmux, SBX, then Git so processes stop before their sandbox and worktree are removed. Do not orchestrate another integration's resources from a provider or from `internal/workspace`.

## Checklist

1. Choose capabilities.
2. Add `internal/integration/<name>`.
3. Implement `Source` first.
4. Emit stable source refs and observations.
5. Add source ref contract tests and provider-specific parser/API tests.
6. Register the package once in `internal/app.DefaultIntegrations`.
7. Add only the minimum config needed.
8. Avoid fallback chains, aliases, or duplicate command paths.

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
