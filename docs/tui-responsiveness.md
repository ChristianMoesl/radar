# Native TUI responsiveness qualification

This extends the [event-first baseline](reactivity-baseline.md) from direct model calls to the actual Bubble Tea input parser, command loop, native Unix revision watch and default 60 FPS renderer. Production baseline: `7af9d17f95be`, measured 2026-11-20. **No production rendering, transport, authority, action gate or snapshot-coalescing behavior is changed.** These controlled measurements do not establish OS keyboard-to-terminal paint latency or a production p95 guarantee.

## Fixture and boundaries

`internal/tui/responsiveness_test.go` constructs a 200-task, one-workspace-reference-per-task graph (199 synthetic anchors plus the registered baseline anchor), a 120×40 viewport and a warm native state/socket fixture. It seeds the model from the actual server projection: record IDs are not inferred from input IDs or row indexes. HOME/config/SQLite are isolated; no live runtime, network provider or user state is used.

An actual workspace provider collection is channel-stalled after observation. A correlated cleanup command for task A is independently channel-stalled; it is a test command, not a real cleanup. Navigation starts well down the list. Each of 20 windows sends managed-resource invalidations five milliseconds apart through `MarkResourceRefreshPending`, while bytes for navigation/search pass through an `io.Pipe` into Bubble Tea. The producer continues until that window’s publication frame is observed, with a 1024-notification safety bound; input and publication-to-output are both measured under the burst. Native responses carry the full task graph. Input and output are not injected as preconstructed model key/watch messages. The same unmodified `View` and default renderer produce the observed bytes.

While the same burst is still running, publication samples open Inspect on unrelated task B, call the native store's volatile `BeginOperation`, and observe its unique daemon operation ID in actual renderer output. Native failed outcomes are retained across subsequent revisions; the fixture checks all 20 identities/outcomes, local operation identity, correlated cleanup failure retention, ordered native revisions and non-starved input. These API-level operation publications do not execute a provider command or authorize cleanup. No polling tick is needed. The existing global Enter restriction during cleanup is measured only as blocked feedback, **not** claimed to permit unrelated activation.

Input acknowledgement is pipe-write entry through actual Update return; queue/decoder delivery and Update are separate raw columns. Output includes queueing, View and frame pacing until the expected new visible text is written. Escape only has an acknowledgement measurement: the selected row can remain unchanged, so its text is not valid proof of an Escape frame. Publication timing begins before the native in-memory publication call, then separates watch arrival, snapshot application and the first View of that revision. Its total is a conservative call-entry-to-output measurement, not pure notification time. A unique ID already written before the output observer is armed is recovered from bounded captured output; that timestamp can slightly overestimate the first matching write.

An earlier persistent-source-status burst probe stalled inside SQLite `fdatasync`, while the Bubble Tea loop and revision waiter were idle. That measured durable source persistence, not an Update/View lock or keyboard response. The retained loop uses the intended volatile managed-publication path instead; no database durability setting, provider code, timeout budget or safety gate was weakened. Provider execution and durable persistence remain separate costs.

## Retained local measurements

Linux amd64 shared QEMU sandbox, Go 1.26.10; the baseline model benchmark reported two execution threads. CPU/heap profiling was enabled for the retained native run, so its overhead is included. Fixture creation, initial persistent seeding and teardown are excluded from the individual timing intervals. One warmed program supplies 20 samples per main input/publication case; this is not 20 fresh processes or a production workload distribution. No cache eviction was performed.

All 161 timing observations, including tails, are retained in [tui-responsiveness.csv](performance/tui-responsiveness.csv). Median / nearest-rank p95 / maximum below are **milliseconds**; phase percentiles are not additive.

| Input | Samples | Acknowledgement | Expected output boundary |
| --- | ---: | --- | --- |
| Navigate down | 20 | 12.159 / 24.005 / 35.600 | 22.757 / 40.237 / 49.550 |
| Navigate up | 20 | 6.769 / 16.569 / 32.147 | 16.922 / 33.742 / 49.601 |
| Open search | 20 | 9.029 / 19.096 / 19.843 | 17.461 / 33.790 / 34.790 |
| Type search character | 20 | 9.879 / 20.658 / 21.563 | 16.498 / 33.772 / 36.980 |
| Open Inspect | 20 | 9.640 / 19.076 / 20.481 | 23.232 / 27.310 / 40.560 |
| Leave search / Inspect | 40 | 5.446 / 15.087 / 17.827 | Not measured |
| Blocked Enter (one smoke sample) | 1 | 12.247 / 12.247 / 12.247 | 30.882 / 30.882 / 30.882 |

| Publication stage (20 samples) | Median / p95 / maximum |
| --- | --- |
| Native in-memory BeginOperation call | 0.006 / 0.014 / 0.021 |
| Publication call entry to watch Update entry | 7.474 / 18.427 / 45.178 |
| Applying the native watch reply | 2.876 / 5.571 / 5.807 |
| First View for that publication revision | 1.136 / 2.359 / 4.287 |
| Publication call entry to renderer output | 16.989 / 33.155 / 49.446 |

The measured output fixture meets the provisional 100ms input-feedback and 250ms publication-to-output p95 budgets. It leaves useful headroom, not a reason to manufacture a rendering rewrite. The 100-iteration unchanged-production microprofile averaged roughly 5.32ms for navigation Update+View, 5.61ms for typing, 1.10ms for Inspect and 2.73ms for blocked Enter. Overview layout was about 31% cumulative CPU in that microprofile; Unicode/style rendering was also material. Those are averages/profile shares, not end-to-end percentiles or demonstrated performance defects. No snapshot dropping/coalescing or layout cache is introduced.

## Regression and reproduction

`TestResponsivenessSnapshotBurstPreservesFocusAndFailures` applies 200 ordered snapshots, preserving selected identity, stable-layout scroll, a correlated failure and another active operation. Removing the selected item preserves pinned unavailable Inspect and chooses the adjacent overview fallback. A delayed older snapshot cannot resurrect that item, certify the pending graph as fresh, erase native/local errors or finish another operation. The native loop additionally checks actual keyboard decoding, renderer progress, operation history and watch ordering under bursts.

```sh
# Real parser/command-loop/renderer plus deterministic state contracts.
go test -v ./internal/tui -run '^TestResponsiveness' -count=1 -timeout=90s

# Existing model-only cost/profile comparison; no percentile assertion.
go test ./internal/tui -run '^$' -bench '^BenchmarkReactivityInput$' -benchtime=100x -count=1
```

For CPU/heap profiles add `-cpuprofile`, `-memprofile` and `-o` paths outside the checkout to the first command, then inspect them with `go tool pprof`. Functional CI gates ordering, liveness and state/focus/error contracts, not flaky percentile thresholds. The existing native matrix includes these tests on Linux amd64, Linux arm64 and macOS arm64. Local cross-compilation is not native macOS execution; exact CI evidence belongs with the delivered revision.

## Remaining acceptance limits

A pipe/output writer is not a physical keyboard, PTY/terminal emulator, tmux paint or OS scheduling trace. The current fixture cannot rule out a real user's terminal/input problem. It does not qualify replacement-version checksum handling, all payload sizes, all viewport shapes or production source/provider latency. Full acceptance still needs final measurement with the unfinished unrelated-workspace activation behavior integrated and the shared input-to-visible-render contract; this report must not be used to declare that behavior or the overall reactive workflow complete.
