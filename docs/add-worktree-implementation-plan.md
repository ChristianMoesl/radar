# Add Worktree and Pi Tool Implementation Plan

## Goal

Allow a Radar-managed Pi session to add a Git worktree from another repository to its current Radar workspace.

The feature must:

- expose a model-callable `radar_add_worktree` Pi tool;
- use the same repository, branch, naming, copy-file, setup, and validation rules as the existing workspace creation flow;
- let the model choose all values that the create-workspace TUI currently asks the user to choose;
- require explicit user confirmation before the Pi tool applies changes;
- attach the new worktree to the current logical Radar workspace;
- make the worktree available in the workspace's existing SBX sandbox;
- remain retryable after partial failures rather than attempting complex rollback;
- preserve one shared tmux session, one Pi session, and at most one SBX sandbox for the logical workspace.

## Product decisions

### Terminology

A **Radar workspace** is a logical bundle of local resources for one piece of work. It may contain:

- one primary Git worktree;
- zero or more additional Git worktrees from other repositories;
- one tmux session;
- zero or one SBX sandbox;
- related Jira, GitHub, Obsidian, and other source references.

`radar_add_worktree` adds a Git worktree to this bundle. It does not register, clone, or otherwise add a repository as a new Radar concept.

### Tool inputs

The tool mirrors the two branch modes in the create-workspace TUI.

New branch:

```json
{
  "repository": "/path/to/repository",
  "branch_mode": "new",
  "name": "DPSCAP-123-update-cache",
  "base": "origin/main"
}
```

Existing branch:

```json
{
  "repository": "/path/to/repository",
  "branch_mode": "existing",
  "branch": "feature/DPSCAP-123-update-cache"
}
```

Rules:

- `repository` is required in both modes.
- `name` and `base` are required for `new`.
- `branch` is required for `existing`.
- A new branch name is sanitized with the same `workspace.BranchName` behavior used by `radar create`.
- The worktree directory name is derived with the same `workspace.WorktreeName` behavior used by `radar create`.
- An existing `origin/<branch>` is materialized as a tracking local branch exactly as it is today.
- A branch already checked out outside the configured Radar workspace root remains an error.
- The model chooses the values; Radar does not copy the primary worktree's branch automatically.

Use a discriminated TypeBox union so the model cannot submit a mixture of new- and existing-branch fields.

### Confirmation

The Pi tool must build and validate a plan before asking for confirmation. The confirmation must include:

- repository;
- branch mode;
- branch name;
- base, when applicable;
- destination worktree path;
- current logical workspace name;
- current tmux session, when present;
- matching SBX sandbox, when present;
- an explicit warning that recreating the sandbox interrupts processes running inside it.

The tool applies nothing until the user confirms.

If Pi has no interactive confirmation channel, the tool fails closed with a clear error. Direct invocation of the scriptable Radar CLI remains an explicit user action and does not need an additional interactive prompt.

### Idempotency instead of rollback

Do not attempt to restore a removed sandbox or roll back every completed step.

The apply operation behaves as an idempotent reconciliation:

1. Revalidate the plan.
2. Ensure the worktree exists with the requested repository and branch.
3. Persist the worktree as a member of the logical workspace.
4. Ensure the shared sandbox contains the desired mount set.
5. Schedule repository setup.

If sandbox recreation fails after the worktree is created:

- keep the worktree;
- keep its workspace membership;
- return a structured retryable error;
- allow the same tool request to be repeated;
- on retry, skip worktree creation and retry sandbox reconciliation.

Example partial result:

```json
{
  "ok": false,
  "worktree_created": true,
  "workspace_membership_saved": true,
  "sandbox_reconciled": false,
  "path": "/path/to/worktree",
  "error": "sandbox recreation failed",
  "retryable": true
}
```

Setup startup failures remain warnings, matching current workspace creation behavior.

### Tmux behavior

The feature reuses the current tmux and Pi sessions.

Initial scope:

- do not create another tmux session;
- do not start another Pi process;
- do not replay the complete configured tmux layout;
- use the existing session for the temporary setup window;
- do not create a new persistent editor or shell window automatically.

The model can access the added worktree by its returned absolute path once SBX reconciliation succeeds. A later product change may add an explicit tmux-window policy without coupling it to worktree creation.

## Pi integration

### Injection model

Radar should inject a small extension into every Pi process it starts for a workspace. Do not require users to install a separate Radar Pi package.

Add an embedded extension source under the existing Pi package, for example:

```text
internal/pi/extension/radar.ts
```

Use `go:embed` and materialize the extension atomically to a stable path such as:

```text
$XDG_DATA_HOME/radar/pi/radar.ts
```

with the usual `~/.local/share` fallback.

Before creating a tmux session, Radar must:

1. materialize or update the embedded extension;
2. determine the absolute path of the currently running Radar executable;
3. add `--extension <materialized-path>` to `$RADAR_PI_ARGS`;
4. put `RADAR_BINARY=<absolute-executable-path>` in the tmux session environment.

Apply this to every path that starts Pi:

- new workspace creation;
- reopening a workspace after its tmux session was removed;
- workspace forking;
- session creation helpers.

The extension is a runtime resource and is not persisted in Pi's session file, so Radar must inject it on every Pi process launch or resume.

### Tool implementation

The extension registers one tool initially:

```text
radar_add_worktree
```

Include:

- a precise description;
- a `promptSnippet` so the tool appears in Pi's available-tools section;
- `promptGuidelines` that name `radar_add_worktree` explicitly;
- the discriminated parameter union described above.

The extension should remain a thin adapter. It must not implement Git, workspace, tmux, SBX, or persistence rules itself.

It should:

1. read `RADAR_BINARY`, falling back to `radar` only when absent;
2. invoke Radar with `pi.exec(binary, args)` using an argument array, never shell interpolation;
3. pass Pi's absolute working directory as the current workspace context;
4. request a JSON plan from Radar;
5. render the plan in `ctx.ui.confirm`;
6. invoke Radar again to apply after approval;
7. parse and return structured JSON to the model;
8. mark command failures as tool errors with useful details.

Because the updated `pi-sbx` allows non-routed extension tools to execute on the host, `radar_add_worktree` runs in Pi's host process. The current local prerequisite is the `pi-sbx` commit:

```text
37d3bba feat: allow extension tools on host
```

Before implementation testing, verify that the active Pi package resolves to the checkout containing that commit.

## Radar CLI and application API

### Command contract

Add one scriptable command:

```sh
radar add-worktree --workspace <current-path> --repo <repo> --branch-mode new --name <name> --base <base> --preview
radar add-worktree --workspace <current-path> --repo <repo> --branch-mode new --name <name> --base <base>

radar add-worktree --workspace <current-path> --repo <repo> --branch-mode existing --branch <branch> --preview
radar add-worktree --workspace <current-path> --repo <repo> --branch-mode existing --branch <branch>
```

The Pi extension always passes `--workspace` explicitly using Pi's current working directory. For direct CLI use, default it to the process working directory.

Both preview and apply return JSON. Preview must not mutate Git, the workspace registry, tmux, or SBX.

Suggested result types:

```go
type AddWorktreePlan struct {
    WorkspaceID       string   `json:"workspace_id"`
    WorkspaceName     string   `json:"workspace_name"`
    PrimaryPath       string   `json:"primary_path"`
    Repository        string   `json:"repository"`
    BranchMode        string   `json:"branch_mode"`
    Branch            string   `json:"branch"`
    Base              string   `json:"base,omitempty"`
    Path              string   `json:"path"`
    SessionName       string   `json:"session_name,omitempty"`
    SandboxName       string   `json:"sandbox_name,omitempty"`
    RecreateSandbox   bool     `json:"recreate_sandbox"`
    SandboxMounts     []string `json:"sandbox_mounts,omitempty"`
    Warnings          []string `json:"warnings,omitempty"`
}

type AddWorktreeResult struct {
    OK                       bool   `json:"ok"`
    WorkspaceID              string `json:"workspace_id"`
    Path                     string `json:"path"`
    Branch                   string `json:"branch"`
    WorktreeCreated          bool   `json:"worktree_created"`
    WorkspaceMembershipSaved bool   `json:"workspace_membership_saved"`
    SandboxReconciled        bool   `json:"sandbox_reconciled"`
    Retryable                bool   `json:"retryable,omitempty"`
    Warning                  string `json:"warning,omitempty"`
    Error                    string `json:"error,omitempty"`
}
```

Keep preview and apply behind one application service so the CLI and future TUI entry points cannot diverge.

### Shared worktree creation primitive

Refactor `internal/workspace/workspace.go` so Git worktree creation is not inseparable from tmux/Pi/SBX creation.

Extract a primitive that owns:

- dependency validation;
- repository root resolution;
- repository-local `.radar.json` loading and validation;
- branch normalization;
- existing local and remote branch checks;
- destination path validation;
- `git worktree add`;
- configured file copying;
- idempotent recognition of an already-correct worktree.

The existing `workspace.Create` continues to call this primitive and then creates its sandbox and tmux session. The new add-worktree service calls the same primitive but attaches the result to existing resources.

Do not create a second implementation of branch rules in the CLI or extension.

### Setup handling

For a newly created member:

- copy configured files before sandbox reconciliation;
- schedule its `.radar.json` setup commands only after the desired sandbox exists;
- when sandboxed, run setup in the shared sandbox with the new worktree as `--workdir`;
- when not sandboxed, run setup on the host;
- use the existing tmux session's temporary setup window when available;
- include the repository name in the setup window name to avoid ambiguity;
- treat setup-start failure as a warning.

The workspace registry should record whether setup was successfully scheduled if retries would otherwise schedule it repeatedly. A simple `setup_scheduled` member flag is sufficient; setup command completion does not need durable tracking.

## Logical workspace registry

### Why it is required

Two worktrees cannot be linked reliably by directory name or ticket mark alone:

- unrelated workspaces may share a generic name such as `main`;
- a valid workspace may have no configured linking mark;
- added repositories may intentionally use different branch names;
- cleanup and sandbox reconciliation need an explicit member list.

Introduce a small authoritative registry at:

```text
<workspace_root>/.radar-workspaces.json
```

This is durable workspace metadata, not rebuildable task cache state. `radar reset` must not delete it.

### Suggested schema

```json
{
  "version": 1,
  "workspaces": [
    {
      "id": "stable-workspace-id",
      "name": "DPSCAP-123-update-cache",
      "primary_path": "/.../workspaces/radar/DPSCAP-123-update-cache",
      "session_name": "radar-DPSCAP-123-update-cache",
      "sandbox": {
        "name": "DPSCAP-123-update-cache-ab12cd34",
        "agent": "shell",
        "kit_path": "",
        "mounts": [
          "/.../workspaces/radar/DPSCAP-123-update-cache",
          "/.../radar/.git"
        ]
      },
      "members": [
        {
          "repository": "/.../radar",
          "path": "/.../workspaces/radar/DPSCAP-123-update-cache",
          "branch": "DPSCAP-123-update-cache",
          "primary": true,
          "setup_scheduled": true
        }
      ]
    }
  ]
}
```

Requirements:

- absolute, cleaned paths;
- unique workspace IDs;
- unique member paths;
- exactly one primary member;
- atomic write via temporary file, fsync, and rename;
- restrictive but user-readable file permissions;
- process-level locking around read-modify-write;
- explicit version rejection rather than silent interpretation;
- deterministic ordering for stable diffs and tests.

A stable workspace ID may be derived from the cleaned primary path with a cryptographic hash. It does not need to be user-facing.

### Enrolling existing workspaces

Do not migrate every existing worktree eagerly.

When `radar add-worktree` targets an unregistered current workspace:

1. verify the current Git top-level is exactly a two-level Radar worktree under the configured workspace root;
2. derive its workspace name from the path;
3. record it as the primary member;
4. discover its current tmux session from the supplied current context;
5. discover a matching SBX sandbox, if any;
6. snapshot the sandbox's current mount list and reconstruct its Radar-supported agent/kit settings;
7. include registry enrollment in the confirmed plan;
8. persist the new workspace entry during apply before sandbox recreation.

This is feature enrollment, not a general backwards-compatibility migration.

## Source linking and task projection

Update Git worktree collection so every registered member emits:

```text
workspace-group:<workspace-id>
```

as an additional source-owned linking key.

Keep existing keys:

- `workspace:<path>`;
- configured linking marks;
- repository/branch keys.

Expected projection:

- every member worktree joins one Radar task through the group key;
- the tmux session remains linked through the primary worktree path;
- the SBX sandbox remains linked through its primary workspace path;
- all resources become one task through transitive linking;
- this works without a Jira-style mark;
- task selection from any member worktree continues to work through `taskrefs.CurrentPathMatches`.

Add the workspace ID to Git source-ref metadata to support diagnostics and group-aware cleanup.

## SBX reconciliation

### Sandbox discovery

Given the current primary worktree:

1. prefer the sandbox recorded in the workspace registry;
2. otherwise inspect `sbx ls --json`;
3. find sandboxes whose workspace mounts contain the primary path;
4. use the same running-first, name-sorted selection behavior as `pi-sbx`;
5. report ambiguity in the plan when selection cannot be made safely.

If there is no matching sandbox, adding the worktree remains valid and no sandbox is created implicitly. The workspace's existing sandbox policy controls whether it has a shared sandbox.

### Desired mounts

For a sandboxed logical workspace, desired mounts are the deduplicated union of:

- mounts already recorded for the sandbox;
- every member worktree path;
- every member's writable Git common directory when it is outside the worktree path;
- global `sbx.additional_mounts`;
- each member repository's `sbx.additional_mounts`.

The primary workspace owns shared sandbox settings:

- sandbox enabled state;
- sandbox name;
- agent name;
- kit path.

An added repository may contribute additional mounts, but its local `sbx.enabled`, agent, or kit settings must not disable or replace the existing shared sandbox.

### Recreate under the same name

When desired mounts differ from actual mounts:

1. persist the desired registry state;
2. run `sbx rm --force <name>` and tolerate an already-missing sandbox;
3. run `sbx create --name <name> [--kit <path>] <agent> <mount>...`;
4. keep the same sandbox name.

Pi remains on the host. Recreating the sandbox terminates the current `pi-sbx` worker, but the next routed tool call should create a new worker for the same sandbox name. No Radar-specific communication with `pi-sbx` is required.

If recreation fails, return a retryable partial result and leave the registry's desired state intact.

### Already-correct state

A retry must not recreate a correct sandbox unnecessarily. Compare normalized desired and actual mount sets, ignoring ordering and duplicate entries. Preserve read-only mount suffixes where present.

## Cleanup and garbage collection

### Manual cleanup

Because all member Git refs share `workspace-group:<id>`, existing task cleanup will preview all member worktrees together with the one tmux session and one sandbox.

After each successful Git worktree removal:

- remove the member from the workspace registry;
- if the primary is removed as part of full cleanup, remove the complete workspace entry after all members are gone;
- preserve the registry entry after partial cleanup when members remain.

Manual cleanup must continue to warn for every dirty member before execution.

### Automatic garbage collection

Treat a registered multi-worktree workspace as one cleanup bundle:

- emit one GC candidate per workspace group, not one per member path;
- skip the whole group if any member is dirty;
- skip the whole group if its tmux session is attached;
- preview all group targets before executing any target;
- remove the shared tmux session and sandbox once;
- remove all clean member worktrees;
- delete the registry entry after successful completion.

Keep existing per-path behavior for unrelated worktrees that are not members of a registered group.

## Implementation sequence

### Phase 1: Domain and persistence

1. Add workspace-group registry types and path resolution under `internal/workspace` or a focused `internal/workspacegroup` package.
2. Implement validation, deterministic IDs, atomic persistence, locking, lookup by member path, and member updates.
3. Add enrollment of an existing current Radar worktree.
4. Add unit tests for malformed files, duplicate members, path normalization, atomic replacement, and lookup.

### Phase 2: Shared worktree primitive

1. Extract worktree preparation/creation from `workspace.Create`.
2. Preserve existing create behavior and tests.
3. Add idempotent recognition of the requested existing worktree.
4. Add unit and Git E2E tests for both branch modes and retries.

### Phase 3: Add-worktree application service and CLI

1. Add request, plan, result, preview, and apply types.
2. Implement pure preflight planning.
3. Implement idempotent apply through the shared worktree primitive.
4. Add `radar add-worktree` argument parsing and JSON output.
5. Add command help and validation tests.

### Phase 4: SBX reconciliation

1. Extract reusable SBX listing and normalized mount helpers from current workspace code.
2. Build desired mounts from the registry and repository configs.
3. Recreate only when actual and desired state differ.
4. Return retryable partial results without rollback.
5. Add fake-runner tests covering no sandbox, correct sandbox, recreation, failure after worktree creation, and successful retry.

### Phase 5: Source linking and cleanup

1. Add workspace-group linking keys to Git refs.
2. Verify state merges member refs without a configured linking mark.
3. Make registry membership update after cleanup.
4. Make GC group-aware and conservative.
5. Add collector, state, cleanup, and GC tests.

### Phase 6: Pi extension injection

1. Add and embed the Radar Pi extension.
2. Materialize it atomically under Radar's data directory.
3. Inject `--extension` and `RADAR_BINARY` into every Radar-created Pi process.
4. Register `radar_add_worktree` with the discriminated schema.
5. Implement preview, confirmation, apply, JSON parsing, and tool errors.
6. Add TypeScript-focused tests where practical and Go tests for generated Pi arguments and environment.
7. Run an interactive smoke test with the updated local `pi-sbx`.

### Phase 7: Documentation

Update:

- `README.md` workspace creation and scriptable command sections;
- `ARCHITECTURE.md` workspace model and Pi integration sections;
- CLI usage text;
- SBX behavior, including interruption during sandbox recreation;
- cleanup and retry semantics.

## Test matrix

### Worktree planning

- new branch with valid base;
- existing local branch;
- existing remote branch;
- missing base;
- missing existing branch;
- invalid or empty name;
- branch already checked out in source repository;
- destination path already occupied by unrelated content;
- current directory outside a Radar workspace;
- target repository already belongs to the workspace;
- repository basename/path collision.

### Idempotency

- exact repeated request returns the existing member;
- retry after worktree creation but before membership persistence;
- retry after membership persistence but before sandbox recreation;
- retry after sandbox removal but before successful creation;
- correct sandbox is not recreated;
- setup is not scheduled repeatedly after a successful retry.

### Registry and linking

- unregistered primary enrollment;
- lookup from primary and secondary paths;
- member worktrees share one task without a linking mark;
- tmux and SBX refs join through the primary path;
- different workspace groups with the same display name remain separate;
- registry survives `radar reset`.

### Confirmation

- plan is produced before confirmation;
- denial applies no changes;
- confirmation text contains branch, path, and sandbox interruption;
- no-UI invocation fails before apply;
- malformed preview JSON fails safely;
- apply is never invoked after denial.

### SBX

- workspace without sandbox;
- one matching sandbox;
- deterministic selection among matches;
- new worktree and Git common directory are mounted;
- existing and additional mounts are preserved;
- primary agent/kit settings remain authoritative;
- member agent/kit overrides are ignored;
- recreation failure returns retryable partial state;
- next `pi-sbx` routed tool reconnects after same-name recreation.

### Cleanup

- manual cleanup includes every member;
- dirty member is clearly shown;
- automatic GC skips an entirely dirty group;
- attached tmux session skips the whole group;
- successful cleanup removes one session, one sandbox, all members, and registry metadata;
- standalone unregistered worktree behavior remains unchanged.

## Acceptance criteria

The feature is complete when:

1. A Radar-created Pi session advertises `radar_add_worktree`.
2. The model can choose either branch mode and all corresponding values.
3. The user sees and approves an exact validated plan.
4. Approval creates or reuses the requested worktree using existing Radar rules.
5. The worktree appears as part of the same Radar task without requiring a linking mark.
6. The existing SBX sandbox is recreated under the same name with the new worktree and Git metadata mounted.
7. Pi continues running and its next sandboxed tool call can access the added path.
8. A sandbox failure leaves retryable desired state rather than triggering complex rollback.
9. Repeating the same tool call safely converges to the desired state.
10. Manual cleanup and automatic GC handle the logical workspace as one bundle.
11. All unit, E2E, typecheck, and smoke tests pass.
