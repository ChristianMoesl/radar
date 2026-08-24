# Obsidian planning workspace plan

Status: proposed

This document describes a note-first Radar workspace. It is an implementation plan, not the current behavior. The permanent Obsidian and workspace documentation must replace the relevant parts of this plan when the feature ships.

## Summary

An Obsidian-authored task should be usable before a repository or branch is known. Activating the task creates one stable Radar workspace containing its note. Pi, tmux, nvim, and an optional SBX sandbox start in that workspace. The user and agent can plan there, then add Git worktrees as direct child directories without replacing the Pi session or choosing a primary repository.

The workspace starts like this:

```text
<workspace-root>/plan-authentication/
└── note.md -> <vault>/Tasks/Plan authentication--2c965c99/Plan authentication.md
```

After implementation starts:

```text
<workspace-root>/plan-authentication/
├── note.md -> <vault>/Tasks/Plan authentication--2c965c99/Plan authentication.md
├── frontend--feature-auth/
└── api--feature-auth/
```

`frontend--feature-auth/` and `api--feature-auth/` are real Git worktrees. They are not symlinks. Pi remains rooted at `plan-authentication/` for the lifetime of the task.

## Goals

- Let an Obsidian task open as a workspace without selecting a repository.
- Make the task note the only initial user-owned file visible in the workspace.
- Keep the note body empty when Radar creates it.
- Mount the note safely into SBX without exposing the complete vault or all task notes.
- Give the agent short, stable paths for the note and every worktree.
- Keep one Pi session across planning, repository discovery, implementation, and follow-up work.
- Allow a logical workspace to contain zero or more worktrees.
- Remove the concept of an immutable primary worktree from Radar-managed workspaces.
- Preserve repository-local instructions and skills even though Pi starts above the worktrees.
- Make every host mutation and resource refresh visible through Radar's existing confirmation and status flows.

## Non-goals

- Radar will not generate a planning template or headings in the note body.
- Radar will not copy or synchronize note contents between locations.
- Radar will not mount an entire Obsidian vault into SBX.
- Radar will not add FUSE, bindfs, or another filesystem projection dependency.
- Radar will not load repository-local Pi extensions or repository-local `.pi/settings.json` from member worktrees.
- Radar will not infer lifecycle state from Markdown checkboxes.
- This plan does not add automatic summarization, automatic task completion, or a separate plan mode.

## User experience

### Create and open a task

1. The user presses `n` or runs `radar task create --title "Plan authentication"`.
2. Radar creates a task directory and a Markdown note with managed frontmatter and an empty body.
3. The task appears in Radar as it does today.
4. The user presses `Enter` on the task.
5. Radar creates a stable workspace directory, adds `note.md`, creates tmux and Pi, and creates SBX when the resolved workspace configuration enables it.
6. Radar switches to the tmux session.

`Enter` must not open repository selection for an Obsidian-only task. Repository selection becomes a later workspace mutation.

The user sees:

```console
$ pwd
/Users/me/.local/share/radar/workspaces/plan-authentication

$ ls
note.md
```

Pi starts interactively without sending an automatic planning prompt. The embedded Radar extension tells the model that `note.md` is the canonical task note and that the body may be empty. The agent must not invent a template unless the user asks for one.

### Plan the work

The user can discuss the task with Pi and edit `note.md` from Pi, nvim, or Obsidian. Every edit reaches the same canonical file. There is no copy and no synchronization step.

The note can contain any structure the user or agent chooses. Radar owns only its frontmatter fields.

### Add code

When the plan identifies a repository, the user can ask Pi to add a worktree. The agent follows the existing typed flow:

1. Call `radar_workspace_context`.
2. Select a repository returned by Radar.
3. Call `radar_repository_refs` when branch or base information is needed.
4. Append a desired worktree.
5. Submit the complete desired state through `radar_reconcile_workspace`.
6. Confirm one plan covering worktree creation, SBX recreation when required, and resource refresh.

Radar creates the worktree directly below the workspace root:

```text
plan-authentication/
├── note.md
└── frontend--feature-auth/
```

Pi remains in `plan-authentication/`. The agent uses paths such as `frontend--feature-auth/src/...` or runs commands with the member directory as the command working directory.

Additional repositories appear as siblings:

```text
plan-authentication/
├── note.md
├── frontend--feature-auth/
└── api--feature-auth/
```

### Remove code and return to planning

Every clean member worktree can be removed through workspace reconciliation. Removing the last worktree is valid and returns the workspace to:

```text
plan-authentication/
└── note.md
```

Dirty-worktree checks, unpublished-commit warnings, protected-branch handling, and confirmation remain in force. No worktree is protected merely because it was added first.

### Finish and clean up

Completing the Obsidian note remains an explicit task mutation. Cleanup removes the tmux session, sandbox, managed worktrees, managed local branches when safe or confirmed, the `note.md` link, and the empty workspace directory. Cleanup never deletes the canonical Obsidian task directory or note.

Automatic garbage collection uses the same rule after the completed-task retention period.

## Filesystem layout

### Canonical task note

SBX accepts workspace directories and exposes them at the same absolute paths as the host. It does not support mounting one file or remapping a host path to a different sandbox path. Each task therefore needs a private directory that Radar can mount without exposing unrelated notes.

These constraints come from Docker's current documentation:

- [Multiple workspaces](https://docs.docker.com/ai/sandboxes/usage/#multiple-workspaces) defines the primary and additional inputs as directories and states that they appear at their absolute host paths.
- [Workspace mounting](https://docs.docker.com/ai/sandboxes/architecture/#workspace-mounting) describes the same-path filesystem passthrough.
- [Sandbox environment files](https://docs.docker.com/ai/sandboxes/configuration/environment-files/#additionalworkspaces) defines each additional workspace path as a directory and requires sandbox recreation for workspace changes.

Radar should store new tasks as:

```text
<Vault>/Tasks/<creation-title>--<short-radar-id>/<current-title>.md
```

Example:

```text
Work/
└── Tasks/
    └── Plan authentication--2c965c99/
        └── Plan authentication.md
```

Rules:

- The directory is created once and remains stable.
- The directory name uses the creation title plus the first eight hexadecimal characters of `radar-id`.
- Renaming the Markdown file renames the task but does not rename the containing directory.
- The task directory may contain attachments later, but Radar creates only the note.
- The note must remain in its task directory. Moving it elsewhere is invalid rather than silently changing the active SBX mount.
- Titles remain unique across task notes.
- The Obsidian open URI points to the nested note path.
- Collection scans exactly one directory level below `Tasks/` for Markdown task notes.

A new note contains managed frontmatter followed by a blank body:

```md
---
radar-id: 2c965c99-6a50-446e-834a-72656fbc056a
radar-state: open
radar-priority: normal
radar-created-at: 2026-08-25T10:30:00Z
radar-completed-at:
---
```

Radar should write the closing delimiter and a final newline, but no headings, prose, or checkboxes. "Empty note" means an empty user-owned body. Keeping identity and lifecycle in frontmatter preserves the current source-of-truth model.

Unknown frontmatter and the complete body remain user-owned. Existing atomic mutation guarantees remain unchanged.

### Workspace directory

A Radar-managed workspace has one stable anchor directory:

```text
<workspace-root>/<sanitized-workspace-name>/
```

The path is chosen at workspace creation and does not change when the task title or note filename changes. Name collisions receive the existing deterministic hash treatment.

For an Obsidian task, Radar creates an absolute symlink named `note.md`:

```text
note.md -> <absolute-canonical-note-path>
```

An absolute link is intentional. SBX exposes every managed workspace at its host absolute path, so the same link resolves on the host and in the sandbox when the canonical task directory is mounted.

Radar owns `note.md` and repairs it when the canonical filename changes. The link is not part of agent-requested additional mounts and cannot be removed through desired state.

### Worktree directories

Managed worktrees are direct children of the anchor. To support multiple branches from one repository without path renames, every directory includes repository and branch:

```text
<workspace>/<sanitized-repository>--<sanitized-branch>/
```

Long names use the existing deterministic truncation and hash rules. Radar reserves `note.md` and any internal names it introduces later.

A member record stores its actual path. Existing members never move because naming rules change or because another member is added.

## Workspace domain model

The durable registry must describe an anchor with optional note and worktree members rather than a primary worktree.

The target shape is conceptually:

```go
type Workspace struct {
    ID             string
    Name           string
    Path           string
    SessionName    string
    TaskLinkingKey string
    NotePath       string
    Sandbox        *Sandbox
    Members        []Member
}

type Member struct {
    Repository     string
    Path           string
    Branch         string
    SetupScheduled bool
}
```

Required changes:

- Rename `PrimaryPath` to `Path` or `WorkspacePath`.
- Remove `Member.Primary`.
- Permit an empty `Members` slice.
- Derive the workspace ID from the stable anchor path, not a member path.
- Keep repository-and-branch identity unique within and across registered workspaces.
- Persist `NotePath` for task-linked planning workspaces.
- Include note identity, member identity, sandbox intent, and observed ports in the compare-and-swap revision.
- Bump the registry version and update the model directly. Do not add legacy field aliases or fallback readers.

The primary path in tmux, SBX, Pi, workspace inspection, and cleanup becomes the anchor path.

## First-class workspace source

A note-only workspace has no Git ref that can publish workspace identity. The durable workspace registry therefore needs a local source that emits one authoritative workspace ref per registered anchor.

A workspace ref should have:

- source `workspace`
- stable ID `workspace:<workspace-id>`
- kind `workspace`
- lifecycle `workspace`
- lifecycle authority `none`
- absolute `Path` equal to the anchor
- `ProvidesWorkspace: true`
- linking keys `workspace:<clean-anchor-path>` and `workspace-group:<workspace-id>`
- the persisted task linking key when present
- the workspace name as its presentation workspace name

The ref emits no attention signal. Tmux and Pi activity continue to promote the task through resource refs.

Registered Git member refs link through `workspace-group:<workspace-id>` and the task linking key, but the anchor ref is the preferred logical workspace. Unmanaged external Git worktrees continue to provide their own workspace paths as they do today.

This source keeps a note-only anchor visible and linkable even when tmux or SBX is temporarily absent.

## SBX behavior

### Initial planning sandbox

When sandboxing is enabled, Radar creates SBX with the anchor as the primary workspace and the private task directory as a managed writable additional workspace:

```sh
sbx create --name <sandbox-name> <kit> \
  <workspace-anchor> \
  <canonical-task-directory>
```

Inside SBX:

```console
$ pwd
<workspace-anchor>

$ ls
note.md

$ readlink note.md
<canonical-task-directory>/<current-title>.md
```

The sandbox can write the note because the target directory is mounted writable. It cannot see sibling task directories or the rest of the vault.

Radar must validate this behavior with an SBX end-to-end test. In particular, the test must prove that an absolute link in the primary workspace resolves through another mounted workspace and that writes are visible on the host.

### Worktrees

Nested worktrees are already visible through the anchor mount. Radar must not add each worktree directory as another SBX workspace.

A linked Git worktree contains a `.git` pointer to its repository's external Git common directory. Radar must mount each distinct external common directory so Git works in the sandbox:

```sh
sbx create --name <sandbox-name> <kit> \
  <workspace-anchor> \
  <canonical-task-directory> \
  <repository-a-git-common-directory> \
  <repository-b-git-common-directory>
```

Managed mount calculation becomes:

1. workspace anchor
2. canonical task directory when present
3. each distinct external Git common directory needed by a member
4. global configured mounts
5. member repository configured mounts
6. agent-requested additional mounts

Parent-child and duplicate paths must be normalized so SBX receives the smallest correct set. The note directory is writable and Radar-managed. It does not appear in `desired.sandbox.additional_mounts`.

Adding the first member from a repository normally adds a Git common-directory mount and recreates SBX. Adding another branch from a repository whose common directory is already mounted does not require recreation because the new worktree appears below the existing anchor mount.

Changing the mount set keeps the existing confirmed remove-and-recreate behavior. The plan must state that recreation interrupts sandbox processes and discards private VM state.

### Sandbox configuration ownership

Workspace runtime settings are resolved once when the anchor is created and persisted with the workspace:

- A note-first workspace resolves model, thinking, tmux, SBX enablement, and SBX kit from user configuration because no repository exists yet.
- A Git-first `radar create` workspace resolves the initial repository overrides as it does today, then persists the result on the anchor.
- Adding later members does not change model, thinking, tmux layout, SBX enablement, or SBX kit.
- Member `copy_files`, `setup`, and configured additional mounts still apply to that member.

This avoids an implicit primary repository and conflicting kit precedence. Before implementation, confirm that ignoring later repository `enabled` and `kit` overrides is acceptable for note-first workspaces. If it is not, define an explicit workspace-level reconfiguration operation rather than deriving mutable runtime ownership from member order.

## Pi behavior

### Working directory and session

Radar starts Pi in the anchor and never changes its working directory when members are added or removed.

Reasons:

- The anchor exists during planning before any repository exists.
- Pi sessions are organized by working directory.
- No repository becomes privileged because it was added first.
- Note and member paths remain short and stable.
- The same session can return to planning after all worktrees are removed.

The task linking key continues to derive Pi's stable session identity. The readable workspace name remains the display name.

Pi starts without an automatic user message. Radar adds a compact system guideline explaining:

- `note.md` is the canonical Obsidian task note.
- The note body may be empty.
- The agent may structure the note only when the user asks or the work requires it.
- Member worktrees are direct child directories.
- Workspace changes must use Radar's typed tools.

### Multi-root context files

Pi normally discovers `AGENTS.md` and `CLAUDE.md` only from its working directory and parents. Member worktrees are children, so Radar must provide them as explicit resource roots.

For each member, discover the same root context choice Pi would make in that repository directory:

1. `AGENTS.override.md`
2. `AGENTS.md` or case variant
3. `CLAUDE.md` or case variant

Radar must retain each absolute source path. Pi already places context files in path-labelled `<project_instructions>` blocks. The Radar extension must add this scoping contract:

> Instructions from a member repository context file apply only to files under that repository. Nested context files apply to their containing subtree. The most specific applicable directory wins. Global and workspace instructions apply to every member.

Examples:

| Target | Applicable context |
| --- | --- |
| `frontend--feature-auth/src/app.ts` | global, workspace, frontend context |
| `api--feature-auth/internal/server.go` | global, workspace, API context |
| `frontend--feature-auth/packages/web/index.ts` | global, workspace, frontend context, applicable nested context when loaded |

Radar must not concatenate member instructions without paths or describe every member instruction as globally applicable.

The workspace context tool should report instruction files per member so behavior is inspectable:

```json
{
  "members": [
    {
      "repository": "/Users/me/source/frontend",
      "path": "/.../frontend--feature-auth",
      "branch": "feature-auth",
      "instruction_files": [
        "/.../frontend--feature-auth/AGENTS.md"
      ]
    }
  ]
}
```

### Repository-local skills

Pi also discovers `.pi/skills/` and `.agents/skills/` only from its working directory and parents. Radar must explicitly add skill roots from every member:

```text
<member>/.pi/skills/
<member>/.agents/skills/
```

Requirements:

- Refresh available skills after a member is added or removed.
- Keep each skill's source path and member repository visible in Pi's resource display.
- Remove skills from members that leave the workspace.
- Detect duplicate skill names and report both paths. Never silently choose one.
- Preserve Pi's project-trust behavior before loading skills from a newly added repository.
- Do not load member `.pi/extensions`, `.pi/settings.json`, prompts, or themes. Multiple repositories can contain executable or conflicting configuration, and the workspace root remains the Pi project.

The embedded extension should use Pi's supported resource discovery and reload mechanisms rather than copying skills into the anchor. Reconciliation remains successful if a Pi resource refresh fails, but its result must return a warning and keep the previous known-good resource set.

### Refresh timing

Resource state changes at these points:

- Pi session startup
- successful worktree addition
- successful worktree removal
- explicit Pi resource reload
- repository context or skill changes detected by an explicit reload

After reconciliation, the extension should refresh resources before the next agent turn. The user should receive a short notification listing added and removed context files and skills. A resource refresh must not restart the Pi session or lose conversation history.

## Workspace inspection and reconciliation

### Workspace resolution

`radar_workspace_context` and reconciliation currently begin with `git rev-parse --show-toplevel`. That fails in a note-only anchor.

Resolution must become registry-first:

1. Normalize the supplied current directory.
2. Find a registered workspace whose anchor contains that directory or whose member contains it.
3. Resolve to the anchor.
4. Use Git discovery only for unmanaged external worktrees and lazy enrollment paths that remain supported.

Calling the tools from the anchor, `note.md`, or any nested member must resolve the same workspace ID and desired state.

### Desired state

An empty worktree set is valid:

```json
{
  "worktrees": [],
  "sandbox": {
    "additional_mounts": [],
    "ports": []
  }
}
```

Changes required:

- Remove validation that requires at least one worktree.
- Remove the primary-worktree removal error.
- Treat every member omission as an ordinary guarded removal.
- Keep repository-and-branch uniqueness.
- Generate addition paths below the anchor, not directly below `workspace_root`.
- Keep complete replacement semantics for members, requested mounts, and ports.
- Keep dirty-removal failures and branch publication warnings.
- Keep sandbox attachment immutable through ordinary reconciliation.
- Include managed note and Git metadata mounts when deriving the effective sandbox mount set.

`radar_workspace_context` should return `workspace_path` instead of `primary_path`, remove `members[].primary`, and include note metadata without exposing note contents:

```json
{
  "workspace_path": "/.../plan-authentication",
  "note": {
    "path": "/.../Tasks/Plan authentication--2c965c99/Plan authentication.md",
    "workspace_path": "/.../plan-authentication/note.md"
  }
}
```

The desired state does not include the note. It is a source-managed workspace resource and cannot be removed by the agent.

### Creation paths

All Radar-created managed workspaces should use an anchor, including:

- Obsidian task activation with zero initial members
- `radar create`
- TUI `c` workspace creation
- GitHub pull request activation
- creation from another selected task

Git-first creation creates the anchor and its initial nested member in one confirmed operation. This keeps one managed workspace model. Merely observed external worktrees remain external and can still receive a direct tmux session without being moved.

Existing registered workspace data uses the old primary-worktree model. Bump the registry version and reject the old version clearly. Do not add compatibility aliases or implicit migrations unless a separate migration requirement is approved.

## Tmux and editor behavior

Tmux session paths and every configured pane start in the anchor. The default layout therefore runs:

```text
pi ...   # cwd: anchor
nvim .   # cwd: anchor
```

The editor sees the note and all current repositories in one tree.

Tmux collection must associate the anchor path with the workspace ref and task linking key. SBX collection must do the same using its primary workspace path and registered sandbox name.

`radar fork` currently assumes its current directory is a Git worktree. It must resolve an enclosing anchor first. To preserve the capability:

- With one member, use that member as the fork source.
- With multiple members, prompt the user to select the member to fork.
- Create a sibling anchor containing the forked member worktree.
- Fork the Pi session as today.
- Do not duplicate an Obsidian note or silently attach the sibling to the same authored task.

This behavior needs focused product and integration tests because the command can no longer derive its source with one `git rev-parse` call from tmux's root directory.

## Linking, lifecycle, and attention

The Obsidian note remains the primary work item and owns task title, open/done lifecycle, priority, and timestamps.

The workspace anchor owns only local workspace identity. It does not complete, reopen, or reprioritize a task.

Linking should work as follows:

```text
obsidian:task:<uuid>
        ↕ persisted task linking key
workspace:<workspace-id>
        ↕ workspace-group:<workspace-id>
Git members, tmux session, Pi activity, SBX sandbox
```

An open note-only task with no tmux or SBX remains low priority or immediate according to note priority. Opening its tmux session and sandbox promotes it through the existing supporting-resource behavior. Pi busy state remains independent of attention.

Renaming the note changes the task title and Obsidian URL. It does not change workspace ID, anchor path, tmux session identity, Pi session identity, or sandbox name.

## Cleanup and garbage collection

Cleanup order becomes:

1. tmux
2. SBX
3. Git members
4. workspace anchor

The workspace cleanup provider removes only Radar-owned anchor contents:

- `note.md`
- empty managed directories
- the anchor itself
- the registry record

It must refuse to remove an anchor containing unknown files. Manual cleanup should show those paths as a blocking reason rather than deleting user content. The canonical task directory and note are never cleanup targets.

Garbage collection treats the whole workspace as one bundle. It skips cleanup when any member is dirty, branch publication cannot be established, an attached session blocks removal, or the anchor contains unknown content.

A note-only completed workspace has no Git checks and becomes eligible after the normal retention period when its tmux session is detached and its anchor contains only managed entries.

## Failure behavior

### Note link failures

- Missing canonical note: mark the workspace source partial and show the missing path. Do not recreate or delete note content.
- Stale `note.md` target after a filename rename: repair the link atomically during local refresh.
- Occupied `note.md`: fail closed and report that Radar's managed path is occupied.
- Note moved outside its task directory: mark the Obsidian source partial and require the user to move it back.

### Worktree failures

Keep the existing partial-apply model. Completed additions or removals remain persisted. The result reports completed phases and asks the agent to inspect before retrying.

If worktree creation succeeds but link, registry, or sandbox work fails, the member remains a real child of the anchor and the next reconciliation must converge without creating a duplicate.

### SBX failures

Keep bounded recreation retries and structured retryable results. Never roll back note or worktree changes because sandbox recreation failed.

The note remains editable on the host while SBX is unavailable.

### Pi resource failures

A context or skill refresh cannot corrupt workspace state. Keep the previous resource set, notify the user, and expose the diagnostic. A later explicit reload retries discovery.

## Implementation work

### 1. Obsidian storage

Update `internal/integration/obsidian` and configuration helpers to:

- create one stable directory per task
- create an empty note body
- scan the new nested layout
- validate one task note per task directory
- keep title uniqueness across directories
- build nested Obsidian URIs
- expose canonical note and task-directory metadata
- preserve atomic frontmatter mutations
- update tests and fixtures

Update `docs/integrations/obsidian.md` only when behavior ships.

### 2. Workspace registry and source

Update `internal/workspacegroup` to:

- store anchor path and optional note path
- remove primary-member fields and validation
- permit zero members
- bump the registry version
- add anchor lookup by containing path
- validate that members are descendants of their anchor
- validate managed name collisions

Add the local workspace source and register it in `internal/app`. Update source capability tests, linking tests, the integration matrix, and architecture documentation after implementation.

### 3. Workspace creation

Refactor `internal/workspace` and the Git workspace provider to:

- create an anchor before creating members
- place all managed worktrees under the anchor
- create and repair `note.md`
- create note-only sessions
- resolve runtime configuration once
- run member copy/setup logic from each worktree
- create Git-first and PR workspaces through the same anchor model
- retain direct sessions for unmanaged observed worktrees

### 4. Inspection and reconciliation

Update:

- `internal/workspace/introspection.go`
- `internal/workspace/reconcile.go`
- `internal/workspace/sandbox_reconcile.go`
- CLI JSON contracts
- the embedded Radar Pi tool schemas and guidance

Implement registry-first resolution, empty desired worktrees, nested member destinations, no primary member, note metadata, and new revision fields.

### 5. SBX integration

Update sandbox creation and reconciliation to:

- accept a non-Git primary anchor
- mount the private task directory as a managed writable workspace
- mount only distinct external Git common directories for nested members
- avoid redundant member mounts
- keep requested mounts separate from managed mounts
- associate sandbox collection with the anchor
- add end-to-end coverage for note edits through the symlink

### 6. Pi resource integration

Extend the embedded Radar Pi extension to:

- discover member context files and skill roots
- preserve absolute source paths
- add repository scoping guidance
- enforce trust before loading member skills
- detect duplicate skills
- refresh after reconciliation
- unload removed resources
- report resource changes and diagnostics
- avoid loading member settings and executable extensions

Confirm the exact Pi extension API behavior against the installed Pi documentation and examples before coding. In particular, verify that an extension can refresh context and skills after a tool call without replacing the active session.

### 7. TUI and commands

Update activation and presentation to:

- open an Obsidian-only task immediately
- remove repository selection from that `Enter` path
- show the anchor, note, and members in task details
- keep `o` opening the canonical note in Obsidian
- start tmux and nvim at the anchor
- adapt cleanup plans
- adapt `radar fork`
- keep standalone `radar create` and PR activation coherent

### 8. Cleanup and garbage collection

Add a workspace-anchor cleanup target after Git cleanup. Update garbage collection to support zero-member workspaces and unknown anchor content checks.

### 9. Documentation

When implementation is complete:

- update `README.md`
- update `ARCHITECTURE.md`
- replace current behavior in `docs/integrations/obsidian.md`
- update `docs/workspace-reconciliation.md`
- update `docs/integrations.md`
- remove or mark this plan implemented

Use neutral identifiers such as `ABC-123` in examples and tests.

## Test plan

### Obsidian

- Creates a task directory with a stable readable name and short ID.
- Creates managed frontmatter and an empty body.
- Collects nested notes and rejects malformed task directories.
- Renaming the note changes title but not task or workspace identity.
- Mutations preserve an empty or arbitrary body.
- Open URI targets the nested note.

### Registry

- Accepts zero members.
- Rejects members outside the anchor.
- Rejects duplicate repository-and-branch members.
- Resolves anchor and member subdirectories to one workspace.
- Rejects old registry versions clearly.
- Persists deterministic ordering and revisions.

### Creation

- Activating a note creates anchor, note link, tmux, Pi, and optional SBX without repository selection.
- Git-first creation creates anchor plus one nested worktree.
- PR activation creates the same shape.
- Reopening a workspace reuses anchor and Pi session.

### Reconciliation

- Adds the first member to an empty workspace.
- Adds members from multiple repositories.
- Adds two branches from one repository without path collision.
- Removes any clean member, including the first one added.
- Removes the last member and leaves the note workspace valid.
- Blocks dirty removal.
- Preserves branch publication and protected-branch behavior.
- Resolves context from anchor and nested member directories.

### SBX

- Starts with a non-Git anchor.
- Resolves and edits `note.md` through the mounted task directory.
- Does not expose sibling task directories.
- Sees nested worktrees through the anchor mount.
- Runs Git successfully through mounted common directories.
- Deduplicates one common directory shared by multiple branches.
- Recreates only when the effective mount set changes.

### Pi

- Starts with anchor as cwd and stable task session ID.
- Does not send an automatic prompt.
- Loads `a/AGENTS.md` and `b/AGENTS.md` with distinct paths and scopes.
- Makes repository-local skills available after member addition.
- Removes those skills after member removal.
- Reports duplicate skill names.
- Does not load member `.pi/settings.json` or extensions.
- Keeps conversation history across resource refresh.

### Cleanup

- Cleans a note-only workspace without deleting the canonical note.
- Cleans a multi-member workspace in provider order.
- Refuses anchors with unknown files.
- Garbage-collects an eligible completed note-only workspace.

Run focused package tests throughout, then:

```sh
go test ./internal/integration/obsidian ./internal/workspacegroup ./internal/workspace ./internal/tui ./internal/cleanup ./internal/workspacegc ./internal/pi
make test
```

Run SBX end-to-end tests on a supported macOS host before calling the feature complete.

## Acceptance criteria

The feature is complete when all of the following are true:

1. A new Obsidian task has managed frontmatter and no generated body content.
2. Pressing `Enter` opens a Pi workspace without selecting a repository.
3. The initial workspace visibly contains only `note.md`.
4. Pi and nvim start in the stable anchor.
5. SBX can read and write the note without access to unrelated notes.
6. Reconciliation can move between zero and many nested worktrees.
7. Every worktree is a real child directory, not a symlink.
8. Git works inside SBX for every member.
9. Repository context files are path-scoped to their repository.
10. Repository-local skills load and unload with membership.
11. Member settings and extensions are not loaded.
12. Task, workspace, tmux, Pi, and SBX identity survive note renames.
13. Cleanup never deletes the canonical Obsidian note.
14. Existing safety checks remain in force for dirty work and unpublished branches.
15. Documentation describes the shipped behavior without relying on this plan.
