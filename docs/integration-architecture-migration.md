# Integration Architecture Migration Guide

## Goal

Build a stable Radar core with clearly separated, source-compiled integrations.

This is **not** a plugin-system migration. Radar should remain one coherent Go binary with explicit integrations compiled into the project. The goal is to make the boundaries mature enough that another Radar developer can add integrations such as Zellij, GitLab, Linear, Dev Containers, or another sandbox/runtime provider without needing to understand or modify task state, linking, projection, TUI internals, or daemon protocol code.

## Product principles

- Keep Radar streamlined: one clear way to do each job.
- Prefer source-compiled integrations over dynamic plugins.
- Do not add plugin discovery, plugin manifests for external loading, Go `plugin`, subprocess plugin protocols, or compatibility shims.
- Integration boundaries should improve maintainability and testability, not turn Radar into an extension platform.
- Radar core owns product behavior.
- Integrations own source-specific facts and tool-specific actions.

## Target architecture

```text
Radar core
├── config loading and validation
├── daemon/socket protocol
├── state persistence
├── source-ref reconciliation
├── task record lifecycle
├── task projection
├── filtering
├── TUI/CLI presentation
├── workspace orchestration
└── integration orchestration

Source-compiled integrations
├── git
├── tmux
├── github
├── jira
├── sbx
└── future: zellij, gitlab, linear, devcontainer, etc.
```

Radar core should depend on small capability interfaces. Integrations implement those interfaces.

## Capability model

Model integrations by capabilities, not by plugin type or package name.

An integration can implement one or more capabilities. For example:

- Git: source collection + workspace provider
- Tmux: source collection + multiplexer provider
- Sbx: source collection + runtime actions + deletion
- GitHub: source collection + code review reconciliation
- Jira: source collection + work tracker reconciliation
- Future Zellij: source collection + multiplexer provider
- Future GitLab: source collection + code review reconciliation

### Core source capability

Every integration that contributes facts to Radar implements a source collector.

```go
type Source interface {
    Name() string
    Collect(ctx context.Context, req CollectRequest) CollectResult
}
```

This is a rename/reframe of the current `ingestion.Source` concept. The desired language is integration-oriented rather than ingestion-oriented.

### Optional capabilities

Keep optional capabilities as small interfaces:

```go
type StatusReporter interface {
    Status(ctx context.Context, logger *slog.Logger) StatusResult
}

type LocalSource interface {
    Local() bool
}

type Reconciler interface {
    ReconcileDone(ctx context.Context, req ReconcileRequest) []Transition
}

type ActionProvider interface {
    Actions(ctx context.Context, req ActionRequest) []Action
    RunAction(ctx context.Context, req RunActionRequest) (ActionResult, error)
}

type DeleteProvider interface {
    PreviewDelete(ctx context.Context, req DeletePreviewRequest) (DeletePreview, bool, error)
    Delete(ctx context.Context, preview DeletePreview) (DeleteResult, error)
}
```

The exact names can evolve, but the shape should stay small and capability-based.

### Workspace provider capability

A workspace provider owns local code workspace lifecycle.

Today this is Git worktrees. A future provider could support another VCS/workspace model, but Radar should not design for multiple providers until there is a real second implementation.

```go
type WorkspaceProvider interface {
    Source

    Current(ctx context.Context, cwd string) (Workspace, bool, error)
    Create(ctx context.Context, req CreateWorkspaceRequest) (Workspace, error)
    Delete(ctx context.Context, req DeleteWorkspaceRequest) error
}
```

Current mapping:

```text
git -> WorkspaceProvider + Source
```

### Multiplexer provider capability

Radar should not depend specifically on tmux. Radar depends on an interactive session/multiplexer capability.

Today tmux is the only implementation. A future Zellij integration should implement the same capability.

```go
type MultiplexerProvider interface {
    Source

    Current(ctx context.Context) (SessionContext, bool, error)
    EnsureSession(ctx context.Context, req EnsureSessionRequest) (Session, error)
    OpenWindow(ctx context.Context, req OpenWindowRequest) error
    Switch(ctx context.Context, target SessionTarget) error
    DeleteSession(ctx context.Context, target SessionTarget) error
}
```

Current mapping:

```text
tmux -> MultiplexerProvider + Source
```

Future mapping:

```text
zellij -> MultiplexerProvider + Source
```

There should be exactly one active multiplexer provider. Do not add a tmux/zellij config switch until Zellij actually exists. Until then, wire tmux directly as the active provider.

### Runtime/sandbox capability

Runtime integrations provide execution environments or sandboxes.

```go
type RuntimeProvider interface {
    Source
    ActionProvider
    DeleteProvider
}
```

Current mapping:

```text
sbx -> RuntimeProvider + Source
```

Sbx should not own session orchestration. It should ask the active multiplexer provider to open or reuse an interactive session.

### Code review capability

Code review integrations emit refs and attention/in-progress signals for pull requests, merge requests, changes, or reviews.

```go
type CodeReviewProvider interface {
    Source
    Reconciler
}
```

Current mapping:

```text
github -> CodeReviewProvider
```

Future mapping:

```text
gitlab -> CodeReviewProvider
```

### Work tracker capability

Work tracker integrations emit issue/ticket refs and done transitions.

```go
type WorkTracker interface {
    Source
    Reconciler
}
```

Current mapping:

```text
jira -> WorkTracker
```

Future mapping:

```text
linear -> WorkTracker
```

## Source facts, not tasks

The most important long-term boundary is this:

```text
Integrations produce source facts and tool actions.
Radar core produces tasks.
```

Today `ingestion.Result` contains both `Tasks` and `SourceRefs`:

```go
type Result struct {
    Tasks      []protocol.Task
    SourceRefs []protocol.SourceRef
    Complete   bool
}
```

The target model should eliminate integration-owned task projection. Integrations should emit observations/source refs plus simple work signals. Radar core should own linking, task state, task projection, filtering, and UI categories.

Target shape:

```go
type CollectResult struct {
    Observations []Observation
    Complete     bool
}

type Observation struct {
    Ref    protocol.SourceRef
    Signal WorkSignal
    Reason string
}

type WorkSignal string

const (
    SignalInProgress WorkSignal = "in_progress"
    SignalAttention  WorkSignal = "attention"
    SignalImmediate  WorkSignal = "immediate"
    SignalDone       WorkSignal = "done"
)
```

Examples:

- Git worktree emits a `git:worktree:<path>` ref with `in_progress`.
- Tmux session emits a `tmux:session:<id>` ref with `in_progress`.
- Sbx sandbox emits a `sbx:sandbox:<name>` ref with `in_progress`.
- Jira assigned issue emits a `jira:issue:<KEY>` ref.
- GitHub review request emits a `github:pull_request:<repo>:<number>` ref with `attention`.
- GitHub authored PR emits the same kind of ref with `in_progress`.

The current architecture already has a good `SourceRef` foundation. Keep it as the stable fact boundary.

## SourceRef contract

Every integration that emits refs must follow these rules:

1. `SourceRef.ID` is globally stable and source-owned.
2. `SourceRef.Source` is the integration name, for example `github`, `jira`, `git`, `tmux`, `sbx`.
3. `SourceRef.Kind` is source-owned, for example `pull_request`, `issue`, `worktree`, `session`, `sandbox`.
4. `SourceRef.CanonicalKey` is the source-owned fallback identity for standalone tasks.
5. `SourceRef.LinkingKeys` are source-owned join hints.
6. Radar state must not parse source-specific IDs, URLs, branch formats, or metadata.
7. Integrations must not assign Radar task IDs.
8. Integrations must not mutate task state directly.
9. Integrations must not apply final task filters.
10. `Metadata` is opaque to core unless a core feature explicitly documents a key.

Examples:

```text
GitHub PR
ID:            github:pull_request:owner/repo:123
CanonicalKey:  github:pull_request:owner/repo:123
LinkingKeys:   ticket:ABC-123, branch:owner/repo:feature-abc-123

Jira issue
ID:            jira:issue:ABC-123
CanonicalKey:  jira:issue:ABC-123
LinkingKeys:   ticket:ABC-123

Git worktree
ID:            git:worktree:/Users/me/workspaces/repo/feature
CanonicalKey:  workspace:/Users/me/workspaces/repo/feature
LinkingKeys:   ticket:ABC-123, workspace:/Users/me/workspaces/repo/feature

Tmux session
ID:            tmux:session:$1
CanonicalKey:  tmux:session:$1
LinkingKeys:   ticket:ABC-123, workspace:/Users/me/workspaces/repo/feature

Sbx sandbox
ID:            sbx:sandbox:repo-feature
CanonicalKey:  workspace:/Users/me/workspaces/repo/feature
LinkingKeys:   ticket:ABC-123, workspace:/Users/me/workspaces/repo/feature
```

## Package layout

Prefer a modest package shift rather than a large rewrite.

Target layout:

```text
internal/integration/
  source.go
  collect.go
  status.go
  reconcile.go
  actions.go
  delete.go
  workspace.go
  multiplexer.go
  runtime.go

internal/git/
internal/tmux/
internal/github/
internal/jira/
internal/sbx/
```

Do not move all integration packages at once. Keeping `internal/git`, `internal/tmux`, etc. avoids unnecessary churn. The key is that they depend on `internal/integration` capability interfaces rather than on collector-specific details.

## Orchestration model

Current collection is hardcoded in `internal/collector`:

```go
func Sources() []ingestion.Source {
    return []ingestion.Source{
        github.NewSource(),
        jira.NewSource(),
        gitcollector.NewSource(),
        tmux.NewSource(),
        sbx.NewSource(),
    }
}
```

Keep explicit source-compiled wiring, but move the list to an app assembly layer or an integration set:

```go
type Set struct {
    Sources     []integration.Source
    Workspace   integration.WorkspaceProvider
    Multiplexer integration.MultiplexerProvider
}
```

Initial wiring can still be explicit:

```go
func DefaultSet() integration.Set {
    gitSource := git.NewSource()
    tmuxSource := tmux.NewSource()

    return integration.Set{
        Sources: []integration.Source{
            github.NewSource(),
            jira.NewSource(),
            gitSource,
            tmuxSource,
            sbx.NewSource(),
        },
        Workspace:   gitSource,
        Multiplexer: tmuxSource,
    }
}
```

This is not plugin discovery. It is just explicit dependency assembly.

## Migration phases

### Phase 1: Document and freeze the boundary

Goal: establish the integration vocabulary without changing behavior.

Steps:

1. Add `internal/integration` with interfaces equivalent to the current `internal/ingestion` interfaces.
2. Keep `internal/ingestion` temporarily if needed, but avoid adding new concepts there.
3. Add package docs that define source refs, linking keys, canonical keys, and integration responsibilities.
4. Add compile-time assertions for every integration:

```go
var _ integration.Source = Source{}
var _ integration.StatusReporter = Source{}
```

5. Add tests around the contract for each existing source:
   - emitted source refs have non-empty stable IDs
   - source name matches IDs
   - refs have either `CanonicalKey` or linking keys where appropriate
   - URLs are set only when openable
   - metadata-dependent behavior is tested in the source package, not state

Expected production behavior change: none.

### Phase 2: Rename collection concepts from ingestion to integration

Goal: make the code match the architecture.

Steps:

1. Move or alias `internal/ingestion` types into `internal/integration`.
2. Update `internal/collector` imports to use `internal/integration`.
3. Rename request/result types carefully:
   - `ingestion.Request` -> `integration.CollectRequest`
   - `ingestion.Result` -> `integration.CollectResult`
   - `ingestion.Source` -> `integration.Source`
4. Do not change wire protocol.
5. Do not change state file format.
6. Do not change source ref IDs.

Tests:

- Existing collector tests should pass unchanged or with import/name updates only.
- Add a small integration contract test helper that can be reused by each source test.

Expected production behavior change: none.

### Phase 3: Introduce explicit integration assembly

Goal: remove hardcoded source construction from collector logic.

Steps:

1. Add an integration set or app assembly package.
2. Move the source list out of collection execution.
3. Make collector accept `[]integration.Source` or an `integration.Set`.
4. Keep the default set exactly equivalent to the current source order.
5. Keep local refresh behavior by selecting sources that implement `LocalSource`.

Tests:

- Add a collector test with fake sources to verify source order, status handling, local-only collection, and skipped sources.
- Ensure full refresh and local refresh still reconcile source status correctly.

Expected production behavior change: none.

### Phase 4: Make multiplexer an explicit capability

Goal: decouple Radar workflow code from tmux as a concrete dependency.

Steps:

1. Define `integration.MultiplexerProvider`.
2. Adapt `internal/tmux` to implement it.
3. Update workspace creation/session switching code to depend on the interface where practical.
4. Keep tmux as the only active provider.
5. Do not add a `multiplexer` config option yet.
6. Keep tmux source refs exactly as they are.

Tests:

- Add fake multiplexer tests for workspace creation orchestration.
- Verify session creation, window creation, and switch behavior through the interface.
- Keep tmux command-format tests in `internal/tmux`.

Expected production behavior change: none.

### Phase 5: Make workspace provider an explicit capability

Goal: decouple workspace orchestration from git worktree implementation details.

Steps:

1. Define `integration.WorkspaceProvider`.
2. Adapt `internal/git`/`internal/workspace` boundaries carefully.
3. Keep Git as the only workspace provider.
4. Avoid inventing support for non-Git workspaces until there is a real use case.

Tests:

- Add fake workspace provider tests for create/delete flows.
- Keep git-specific dirty worktree and main-worktree protections in git/workspace tests.

Expected production behavior change: none.

### Phase 6: Move source actions behind integration capabilities

Goal: stop hardcoding sbx-specific action dispatch in core action code.

Current action dispatch has source-specific logic in `internal/sourceactions`.

Steps:

1. Define `integration.ActionProvider`.
2. Make sbx implement it.
3. Change action discovery to ask active integrations for actions matching a source ref.
4. Change action execution to route by integration/action ID instead of a source-specific switch.
5. Keep the user-facing key and label stable: `o` then `s` for sbx.

Tests:

- Add fake action provider tests for action listing and dispatch.
- Keep sbx open-shell tests in `internal/sbx`.
- Add a regression test that unknown action IDs fail clearly.

Expected production behavior change: none, except the core no longer imports sbx for action dispatch.

### Phase 7: Move deletion behind integration capabilities consistently

Goal: make delete preview/delete dispatch source-agnostic.

Steps:

1. Ensure git, tmux, and sbx deletion all implement `integration.DeleteProvider`.
2. Make server delete preview iterate active delete providers.
3. Make delete execution route to the provider selected during preview.
4. Keep confirmation text and safety behavior unchanged.

Tests:

- Add server tests with fake delete providers.
- Preserve existing e2e tests for worktree delete, session delete, and sbx delete.
- Add a test for provider precedence when a task contains multiple deletable refs.

Expected production behavior change: none.

### Phase 8: Convert integrations from task output to observations

Goal: complete the source-facts boundary.

This is the largest semantic migration and should be done one integration at a time.

Steps:

1. Add `integration.Observation` while keeping existing `Tasks` and `SourceRefs` fields temporarily.
2. Teach collector/core to project observations into the same tasks currently produced by integrations.
3. Convert one integration at a time.
4. Suggested order:
   - Jira: already source-ref oriented
   - Sbx: already source-ref oriented
   - Git: already source-ref oriented
   - Tmux: already source-ref oriented
   - GitHub: currently task-oriented, likely largest change
5. After all integrations emit observations/source refs, remove integration-owned `protocol.Task` output.

Tests:

- Add golden projection tests: observations in, projected tasks out.
- Add GitHub regression tests to ensure review requests, authored PRs, and activity items preserve attention/reason behavior.
- Add reconciliation tests to ensure done transitions preserve existing task IDs.
- Ensure state tests do not gain source-specific parsing.

Expected production behavior change: should be none, but this phase requires the most regression coverage.

### Phase 9: Add a developer guide for new integrations

Goal: make adding Zellij/GitLab straightforward for Radar developers.

Create a short guide with this checklist:

1. Choose capabilities.
2. Add package under `internal/<name>`.
3. Implement `Source` first.
4. Emit stable `SourceRef.ID` values.
5. Emit `CanonicalKey` and `LinkingKeys` in the source package.
6. Add contract tests using shared helpers.
7. Add provider-specific unit tests around command/API parsing.
8. Register explicitly in the default integration set.
9. Add only the minimum config needed.
10. Avoid adding fallback behavior or duplicate command paths.

Example for Zellij:

```text
Capabilities:
- Source
- MultiplexerProvider

Source refs:
- zellij:session:<stable-session-id>

Linking keys:
- ticket:<KEY> from session name/path when present
- workspace:<path> when current/session path is known
```

Example for GitLab:

```text
Capabilities:
- Source
- Reconciler
- CodeReviewProvider

Source refs:
- gitlab:merge_request:<host>/<group>/<project>:<iid>

Linking keys:
- ticket:<KEY> from title/branch
- branch:<host>/<group>/<project>:<branch>
```

## Testing strategy

The migration should increase testability by allowing core code to use fake integrations.

### Add shared contract tests

Create test helpers for source refs:

```go
func AssertValidSourceRefs(t *testing.T, source string, refs []protocol.SourceRef)
func AssertStableIDs(t *testing.T, collect func() []protocol.SourceRef)
func AssertNoCoreSpecificParsing(t *testing.T, refs []protocol.SourceRef)
```

Validate:

- `ID` is non-empty.
- `Source` matches the integration name.
- `Kind` is non-empty.
- IDs are unique per collection result.
- Standalone refs have `CanonicalKey` when they can become tasks.
- Linking keys have documented prefixes.

### Add fake integration tests for core

Core collector/server/workspace orchestration should have tests using fake implementations:

- fake source
- fake local source
- fake status reporter
- fake reconciler
- fake delete provider
- fake action provider
- fake multiplexer
- fake workspace provider

This lets core tests verify orchestration without shelling out to `git`, `tmux`, `gh`, `jira`, or `sbx`.

### Keep provider-specific command/API tests local

Integration packages should own parsing and command behavior tests:

- GitHub: API response parsing, rate limit handling, PR state resolution
- Jira: issue response parsing, status filtering, auth/config status
- Git: worktree parsing, dirty checks, main worktree protection
- Tmux: session parsing, current context, command construction
- Sbx: sandbox parsing, open-shell command construction, deletion

### Add projection golden tests

Once observations exist, add tests at the core projection boundary:

```text
observations + previous state -> task records + projected tasks
```

This protects the most important product behavior while allowing integrations to evolve independently.

### Preserve e2e coverage

Keep existing end-to-end tests for:

- collector reconciliation
- server delete flow
- workspace creation
- workspace deletion
- TUI behavior

Where e2e tests currently depend on concrete tmux/git behavior, consider adding interface-level fakes first, then keep a smaller number of concrete command tests in integration packages.

## Non-goals

Do not implement these as part of this migration:

- Dynamic plugin loading
- External plugin SDK
- Plugin discovery directories
- Runtime-loaded provider manifests
- Multiple active multiplexers
- Multiple active workspace providers
- Backwards-compatible state readers for renamed integration fields
- Config switches before a second real implementation exists
- Generic fallback chains between tools

## Acceptance criteria

The migration is complete when:

1. Radar has an `internal/integration` boundary with small capability interfaces.
2. Collector orchestration depends on integration capabilities, not concrete packages.
3. Tmux is modeled as the active multiplexer provider, not as an implicit core dependency.
4. Git is modeled as the active workspace provider, not as scattered workspace implementation detail.
5. Sbx actions/deletion route through integration capabilities.
6. Source packages own all source-specific IDs, linking keys, canonical keys, URLs, and metadata.
7. Core state/linking/projection does not parse source-specific formats.
8. Integrations emit source facts/observations rather than projected Radar tasks.
9. Tests cover core orchestration with fakes and integration-specific behavior in integration packages.
10. Adding a source-compiled Zellij or GitLab integration is mostly local to a new package plus explicit registration.
