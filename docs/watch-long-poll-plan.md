# Watch / long-poll update plan

## Goal

Make the TUI update automatically when the daemon state changes, without adding a streaming protocol or aggressive polling.

Use a `watch` socket method implemented with long-polling:

```text
TUI sends watch:<revision>
daemon responds when state revision changes, or when a timeout expires
TUI updates if response contains changed data, then starts the next watch
```

## Current behavior

The TUI only updates when it explicitly requests data:

- startup calls `tasks`
- `r` calls `refresh`
- `R` calls `reset`
- some actions request `refresh` after completion

The daemon already refreshes in the background, but the TUI does not subscribe to state changes. If background refresh updates `tasks.json`, an open TUI will not redraw until it asks again.

## Proposed behavior

1. TUI starts and calls `tasks`.
2. The response includes a monotonically increasing `revision`.
3. TUI starts `watch:<revision>` in the background.
4. Daemon handles `watch:<revision>`:
   - if current revision is newer, return `tasks` immediately
   - otherwise wait until revision changes or timeout
5. TUI receives the watch response:
   - if it contains tasks/sources/summary, update UI
   - regardless of timeout or change, start another watch with the latest revision
6. Manual refresh remains available and still forces collection immediately.

## Protocol changes

Add `revision` to `protocol.Response`:

```go
type Response struct {
    OK       bool           `json:"ok"`
    Error    string         `json:"error,omitempty"`
    Revision int64          `json:"revision,omitempty"`
    Summary  *Summary       `json:"summary,omitempty"`
    Tasks    []Task         `json:"tasks,omitempty"`
    Sources  []SourceStatus `json:"sources,omitempty"`
}
```

Add socket method:

```text
watch:<revision>
```

Response on change:

```json
{
  "ok": true,
  "revision": 43,
  "summary": { ... },
  "tasks": [ ... ],
  "sources": [ ... ]
}
```

Response on timeout/no change:

```json
{
  "ok": true,
  "revision": 42
}
```

## Store changes

Add revision tracking to `state.Store`:

- `revision int64`
- increment revision whenever visible state can change:
  - `SetTasks`
  - `SetLocalTasks`
  - `SetSources`
  - `Acknowledge`
  - `Reset`
- provide `Revision() int64`
- provide `WaitForRevision(ctx, after int64) int64` or similar

Implementation option:

- keep a `chan struct{}` notification channel in the store
- on mutation:
  - increment revision
  - close old channel
  - replace with new channel
- `WaitForRevision` checks current revision first, then waits on the channel or context timeout

This avoids condition-variable complexity and works well with request-scoped contexts.

## Server changes

For normal read methods (`summary`, `tasks`, `refresh`, `reset`, `ack`):

- include current revision in every response

For `watch:<revision>`:

1. parse requested revision
2. wait for newer revision with timeout, e.g. 30s
3. if revision changed:
   - return same shape as `tasks`
4. if timeout:
   - return `{ ok: true, revision: currentRevision }`

Important: one long-poll request should not block unrelated socket clients. Each connection is already handled independently; keep that behavior.

## TUI changes

Add model field:

```go
revision int64
watching bool
```

Startup:

- `Init()` still fetches `tasks`
- after receiving a successful response with revision, start a `watch:<revision>` command

On `watch` response:

- if response has tasks/sources/summary, update model like a normal fetch
- update stored revision
- start the next watch command

On manual refresh:

- keep existing behavior
- refresh response updates revision
- watch loop should continue using latest revision

Avoid duplicate concurrent watches:

- track `watching` or have a dedicated `watchMsg`
- only start one watch at a time
- when a normal fetch returns newer revision, the outstanding old watch may return immediately/no-op; handle safely

## Timeout and errors

Recommended timeout: 30 seconds.

On watch timeout:

- daemon returns `ok: true` with revision and no tasks
- TUI immediately starts another watch

On watch error:

- TUI should show a subtle error or reuse existing error handling
- retry after a short delay, e.g. 2 seconds, to avoid tight failure loops

## Tests

Add tests for:

1. protocol marshals `revision`
2. store revision increments on mutations
3. `WaitForRevision` returns immediately if already newer
4. `WaitForRevision` unblocks on mutation
5. server `watch:<old>` returns tasks immediately
6. server `watch:<current>` times out with no tasks
7. TUI handles watch responses without resetting selection unexpectedly

## Non-goals

- No websocket/event-stream protocol.
- No client-side aggressive polling.
- No separate user-facing command required.
- No persisted revision in `tasks.json`; revision is daemon-process state for change detection.

## Open questions

- Should `SetSources` always increment revision, or only when source statuses actually change?
- Should watch responses be filtered exactly like `tasks` responses? Recommendation: yes.
- Should revision increment on no-op reconciliations? Recommendation: start simple and increment on setter calls; optimize later only if needed.
