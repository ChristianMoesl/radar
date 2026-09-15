# tmux integration

tmux supplies local interactive-session facts and multiplexer operations.

## Capabilities

`Source`, `StatusReporter`, `LocalSource`, `MultiplexerProvider`, `ActivityPublisher`, and `CleanupProvider`.

## Configuration

The `tmux.windows` session-layout schema uses generic window, pane, command, and layout settings. Exactly one pane command contains `$RADAR_PI_ARGS`.

## Collection and refs

Local refreshes emit stable session refs based on tmux server and session identity. Session names and paths contribute mark/workspace linking keys. Attached sessions set generic `InUse`, which blocks automatic cleanup without core metadata parsing.

The multiplexer capability owns current-client detection, task target selection, switching, session/window creation, and matching. Cleanup removes the opaque session resource ID.

## Activity

`radar activity <idle|busy|waiting>` publishes generic runtime activity. Only this integration resolves `TMUX_PANE` and writes its `@radar_activity` option (`busy` or `waiting`), unsetting it for idle. Publication without a pane is a no-op. Invalid states are rejected. Collection ignores dead panes and reduces live pane states per session with `waiting > busy > idle`. Empty/unset state is idle. Malformed pane state reports a collection error rather than inventing activity.

Source refs expose typed `Activity`; task projection applies the same precedence across active authoritative refs without interpreting provider metadata. Done tasks suppress activity. The TUI shows amber `! waiting` instead of `● busy`, and details show `Activity: waiting`. Navigation and permission decisions remain in the existing session/agent workflow.

The Pi adapter uses native `ui_prompt_start` / `ui_prompt_end` hooks (Pi >= 0.85.1), which span extension `confirm`, `select`, `input`, `editor`, and `custom` calls. Pi coalesces nested/overlapping prompts into a single span. The adapter tracks running and prompt-open independently, serializes and deduplicates writes, and forces idle on startup/shutdown. Late events from a shutting-down instance cannot restore activity. It publishes only state, never prompt text or answers. Failed commands remain best-effort and may retry on subsequent lifecycle events. Each CLI invocation requests a local refresh with a one-second socket deadline after publishing; a stopped/slow daemon does not fail the publication, and the regular local refresh remains available.

### Limits

- Arbitrary shell stdin, external browser approvals, conversational questions, and built-in dialogs outside extension UI are not inferred.
- Pi's `custom` lifetime can represent a loader or overlay that completes automatically; it is still reported as waiting. There is no reliable semantic distinction in the current prompt API, and Radar does not special-case tool names.
- Normal shutdown/reload and dead panes clear or suppress activity. A hard-killed producer that leaves a live shell pane may leave stale activity until reset by the next producer startup. No lease/heartbeat is introduced; a genuine prompt does not expire solely because it is old.
- Existing navigation chooses a task's session, not necessarily the exact waiting pane when a task spans multiple sessions.

### Upgrade and existing local data

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
