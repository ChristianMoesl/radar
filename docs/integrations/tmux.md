# tmux integration

tmux supplies local interactive-session facts and multiplexer operations.

## Capabilities

`Source`, `StatusReporter`, `LocalSource`, `MultiplexerProvider`, `ActivityPublisher`, and `CleanupProvider`.

## Configuration

The `tmux.windows` session-layout schema uses generic window, pane, command, and layout settings. Exactly one pane command contains `$RADAR_PI_ARGS`.

## Collection and refs

Local refreshes emit stable session refs based on tmux server and session identity. Session names and paths contribute mark/workspace linking keys. Attached sessions set generic `InUse`, which blocks automatic cleanup without core metadata parsing.

The multiplexer capability owns current-client detection, task target selection, switching, session/window creation, and matching. Pi reports busy/idle through `radar activity`; only this integration translates that event to `@radar_busy` pane state. Cleanup removes the opaque session resource ID.

## Validation

```sh
go test ./internal/integration/tmux ./internal/integration/tmux/layout
```
