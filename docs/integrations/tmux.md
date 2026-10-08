# tmux integration

tmux supplies local interactive-session facts and multiplexer operations.

## Capabilities

`Source`, `StatusReporter`, `LocalSource`, `MultiplexerProvider`, `ActivityPublisher`, and `CleanupProvider`.

## Configuration

The `tmux.windows` session-layout schema uses generic window, pane, command, and layout settings. Exactly one pane command contains `$RADAR_PI_ARGS`.

## Workspace layout

By default, workspace sessions use one `pi` window with a single pane running `pi $RADAR_PI_ARGS`; Neovim is not required. This default also applies when `tmux.windows` is omitted or empty. Existing explicit layouts are preserved. Configure additional workspace windows, panes, layouts, and commands in the user config, for example to add an optional Neovim pane:

```yaml
tmux:
  windows:
    - name: workspace
      layout: horizontal
      panes:
        - command: pi $RADAR_PI_ARGS
        - command: nvim .
```

Every window requires a unique `name` and at least one pane command. Commands run from the workspace directory. The configuration must contain `$RADAR_PI_ARGS` exactly once; Radar replaces it with shell-quoted model, thinking, session, and optional fork arguments before starting tmux. A task-created workspace derives Pi's session identity from the stable task linking key while keeping the readable workspace name as Pi's display name. Renaming the task therefore does not move its Pi history to another task, and different tasks using the same branch do not share a Pi session. Supported layouts are `horizontal`, `vertical`, `main-horizontal`, `main-vertical`, and `tiled`. Omitting `layout` leaves tmux's initial pane layout unchanged. The pane containing `$RADAR_PI_ARGS` is focused after creation.

When run inside tmux, Radar switches to the new session.

## Collection and refs

Radar collects tmux sessions from the local tmux server and attaches them to matching tasks when their name contains a configured linking mark, or when the session working directory matches a Git worktree path. Sessions without matches are shown as standalone in-progress tasks.

Pi sessions inside registered Radar workspaces publish generic activity through the installed `pi-radar` extension (Pi 0.85.1 or newer). The task row shows `● busy` while the agent works and amber `! waiting` instead while an extension UI prompt is open. Closing the prompt restores busy or idle; idle has no badge. Parent sessions and in-process subagents that activate the extension share one activity publisher: the pane stays busy until all their runs settle, and one session's startup or shutdown cannot clear another's activity. Waiting takes precedence across those sessions, other panes, and linked sessions. Task details expose the same activity, and existing workspace navigation takes you back to the agent to respond.

Activity is independent of task attention: it does not change categorization, sorting, acknowledgements, notifications, or approval policy. Prompt titles and answers are not published. Transitions request a bounded local refresh rather than waiting for the regular 15-second poll. Native Pi prompt hooks cover confirmations, selections, input, editors, and custom UI across extensions—not arbitrary shell stdin, external browser approvals, or conversational questions. Custom UI may also include automatically completing loaders. See [activity details](#activity) for lifecycle limits and coordinated upgrade steps.

Tmux session refs use `#{session_id}` for stable identity, so renaming a tmux session does not create a new Radar task. Selecting a tmux-backed task switches to the stable session target.

Local refreshes emit stable session refs based on tmux server and session identity. Session names and paths contribute mark/workspace linking keys. Attached sessions do not block cleanup of eligible completed workspaces; removing a session terminates its running shells and commands. See [cleanup safety](../workspace-cleanup.md).

The multiplexer capability owns current-client detection, task target selection, switching, session/window creation, and matching. Cleanup removes the opaque session resource ID.

## Activity

`radar activity <idle|busy|waiting>` publishes generic runtime activity. Only this integration resolves `TMUX_PANE` and writes its `@radar_activity` option (`busy` or `waiting`), unsetting it for idle. Publication without a pane is a no-op. Invalid states are rejected. Collection ignores dead panes and reduces live pane states per session with `waiting > busy > idle`. Empty/unset state is idle. Malformed pane state reports a collection error rather than inventing activity.

Source refs expose typed `Activity`; task projection applies the same precedence across active authoritative refs without interpreting provider metadata. Done tasks suppress activity. The TUI shows amber `! waiting` instead of `● busy`, and details show `Activity: waiting`. Navigation and permission decisions remain in the existing session/agent workflow.

The Pi adapter uses native `ui_prompt_start` / `ui_prompt_end` hooks (Pi >= 0.85.1), which span extension `confirm`, `select`, `input`, `editor`, and `custom` calls. Pi coalesces nested/overlapping prompts into a single span. The adapter tracks running and prompt-open independently per Pi session. Parent and in-process subagent extension instances share a process-wide activity map, publication queue, and deduplication state, including when Pi loads separate module instances. Contributions reduce with `waiting > busy > idle` before publication. Startup adds an idle contribution; shutdown removes only that session's contribution and republishes the aggregate. Neither can clear a busy or waiting parent/sibling. The pane is idle only when no contributing session is busy or waiting. Late events from a shut-down instance cannot restore activity. It publishes only state, never prompt text or answers. Failed commands remain best-effort and may retry on subsequent lifecycle events. Each CLI invocation requests a local refresh with a one-second socket deadline after publishing; a stopped/slow daemon does not fail the publication, and the regular local refresh remains available.

### Limits

- Arbitrary shell stdin, external browser approvals, conversational questions, and built-in dialogs outside extension UI are not inferred.
- Pi's `custom` lifetime can represent a loader or overlay that completes automatically; it is still reported as waiting. There is no reliable semantic distinction in the current prompt API, and Radar does not special-case tool names.
- Normal shutdown/reload and dead panes clear or suppress activity. A hard-killed producer that leaves a live shell pane may leave stale activity until reset by the next producer startup. No lease/heartbeat is introduced; a genuine prompt does not expire solely because it is old.
- In-process aggregation requires each contributing session to load `pi-radar` and activate inside a registered workspace. It does not observe queued/unbound children, children that exclude the extension, or coordinate independent Pi processes writing to the same pane. There is no dependency on a particular subagent package or tool name.
- Existing navigation chooses a task's session, not necessarily the exact waiting pane when a task spans multiple sessions.

### Upgrade and existing local data

The in-process aggregation fix is an extension-only update: CLI/socket JSON, pane options, and persisted formats are unchanged, so it needs no data migration. Update the installed `pi-radar` package and reload or restart the parent Pi session after its subagents finish; already loaded copies keep their old behavior. `make install` updates the Radar binary, not the separately installed Pi package. The steps below concern the original busy-to-activity model rollout.

Coordinate the Radar binary/daemon and installed Pi package upgrade; reload each registered Pi session while idle. The adapter requires Pi 0.85.1 or newer. Older running producers and old consumers must not be mixed with the new activity model.

The `busy` boolean is replaced by `activity` in CLI/socket JSON and in task/source snapshots. Cache record structure/version, task IDs, acknowledgements, workspace registry, and authored notes remain unchanged. Old transient busy values are intentionally not read: absent activity starts idle and is recollected after producers reload. Do not reset the full task cache. The old `@radar_busy` pane option is no longer read or written; it can be explicitly removed after all producers are upgraded. There is no compatibility shim or dual publication.

Before installing against existing local data, perform a **read-only preflight** and obtain agreement on dropping/recollecting transient state:

1. Locate the configured task cache (`RADAR_STATE`, otherwise `$XDG_STATE_HOME/radar/tasks.json` or `~/.local/state/radar/tasks.json`); validate JSON/version and inventory task/source snapshot activity fields. Record task IDs and acknowledgement counts without logging prompt content.
2. Inventory pane state with `tmux list-panes -a -F '#{pane_id} #{pane_dead} #{@radar_busy} #{@radar_activity}'` and identify still-running producers. Do not modify panes during preflight.
3. Preserve the existing cache/registry/notes; agree on coordinated daemon and idle Pi-session reloads. Retained transient state is reconstructed, not migrated. Do not install if a different cache version or malformed local data needs a separate handling decision.
4. After the coordinated upgrade, check busy → waiting → busy/idle in a real reconciliation or sandbox approval and verify task IDs/acknowledgements are unchanged.

Code tests do not substitute for this host rollout check.

## Validation

```sh
go test ./internal/integration/tmux ./internal/integration/tmux/layout
```
