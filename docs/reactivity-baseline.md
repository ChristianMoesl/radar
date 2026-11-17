# Event-first responsiveness baseline

CHR-31 / part 1 of CHR-30. Runtime source audited at `4684de54054d` on 2026-11-17. This change adds fixtures and a contract, **not** operation concurrency, a watcher, a new transport, or a latency guarantee. The existing [workspace performance report](workspace-performance.md) separately measures provider execution and cleanup safety phases; those timings are not keyboard latency.

## Authority and trigger audit

| Displayed fact | Authority and current trigger | Gap / intended event coverage |
| --- | --- | --- |
| Cleanup result / survivors | `server.cleanup` waits for `cleanup.Service.Execute`; the response includes successful removals even on partial failure. It invalidates the resource epoch and wakes revision subscribers. Cached refs remain explicitly pending. | No immediate cleanup-specific collection request. Child 2 must request reconciliation on success **and** partial failure without delaying acknowledgement. A missing directory is never proof of cleanup success. |
| Registered workspace / usable resources | Workspace registry associates identity and members; workspace, Git, SBX and tmux providers observe their own facts. `ProvidesWorkspace` on a retained registry ref is not proof that its directory/runtime exists. | Local polling or explicit `refresh-local`. Child 3 must fresh-check registration, ownership and resources when activating/reopening B, not authorize from cached availability. |
| Authored title, lifecycle, priority, bindings | SQLite task state through the existing authoring/task-service mutation path; `MutateTask` refreshes the author projection and publishes the store revision. | Reuse this path for TUI/CLI/Pi convergence. Note contents are not task-state authority. Never use SQLite/WAL filesystem events as a mutation feed. |
| Agent activity | tmux pane options written by `radar activity`, followed by a bounded best-effort `refresh-local`; durable-agent manager status changes call the existing local-refresh callback (manager currently checks every two seconds). | Reuse notifications and periodic recovery. The activity request's one-second refresh budget is not a guarantee of collection completion. No invented provider-native subscriptions. |
| Externally missing/restored registered anchor | `workspace.Source.Collect` stats the registered path; missing path yields `workspace partial` plus `workspace anchor is missing`. The existing Sources summary and `[s]` detail display these facts. | **Selected child-4 target:** nonrecursive directory-presence invalidation followed by authoritative collection. See below. |

Source entry points: `cmd/radar/main.go` (`runDaemon`, `refreshLoop`, `localRefresher`, activity publication), `internal/taskservice/tasks.go`, `internal/server/server.go`, `internal/state/state.go`, `internal/tui/tui.go`, `internal/tui/operation.go`, `internal/integration/workspace/source.go`, `internal/integration/tmux/source.go`, `internal/agentservice/manager.go`.

The 15-second local and two-minute full cadences remain recovery/discovery paths, with separate workers. These are eligibility cadences, **not** worst-case convergence bounds: busy collectors, locks, unavailable providers and partial reads can delay completion. `watch:<revision>` uses a 30-second long-poll timeout, not a 30-second update interval. `WaitForRevision` wakes on publication; reconnect resets the TUI revision to zero and retries after two seconds. Older positive-revision replies are ignored.

## Reproducible scenarios

All new fixtures live in `internal/tui/reactivity_baseline_test.go`; they isolate HOME/config/SQLite, use a short temporary Unix socket, and never contact live providers or run cleanup. No installed/user state is read. There are no timing-percentile CI assertions.

```sh
# Real Bubble Tea command-loop stall/completion; deterministic gating and recovery
# (existing focused fixtures, deliberately retained rather than duplicated).
go test -p 2 ./internal/tui -run 'Test(CleanupSwitchRecoversAsynchronouslyWithTeatest|CleanupCompletionRestoresWorkspaceEnter|OperationAnimationAndNavigation|TaskFailureSurvivesRefreshAndClearsOnlyOnSuccessfulOperation)$' -count=1

# External directory -> real provider -> task service -> Unix revision watch -> model/view.
go test -p 2 ./internal/tui -run '^TestReactivityBaselineExternalWorkspace$' -count=1

# Samples, with fixture construction/builds excluded from the timer.
go test -p 2 ./internal/tui -run '^$' -bench '^BenchmarkReactivity' -benchtime=1x -count=20

# Existing safety/order and native watch contracts.
go test -p 2 ./internal/taskservice ./internal/server ./internal/tui
```

**Cleanup A while using B:** the channel-controlled cleanup command in the existing teatest fixture remains stalled until explicitly released. Navigation, search/inspection and watch updates do not move or finish A's operation. At this baseline, Enter on unrelated B is intentionally blocked with “Retry Enter when finished”; this includes both opening and recreating a registered workspace's closed tmux session, because the global gate runs before `activateSelected`. The generation only fences spinner ticks, not correlated daemon operation results. Completion restores activation; success, daemon/transport failure and disappearance of A retain the appropriate task context. This reproduces a known restriction, not evidence that child 3 is already implemented.

**Cleanup completion:** `TestCleanupResponseMarksItsRetainedSnapshotPending` and the task-service resource-publication regressions establish immediate pending acknowledgement, independent of unrelated collection/publication locks, and rejection of an in-flight pre-mutation collection. They do not establish prompt post-cleanup reconciliation: that remains child 2's work. Existing `cleanup_execution`, `sbx_rm`, `worktrees`, `cleanup_response`, `post_mutation_publication`, `collection_lock_wait` and `local_collection` phase records separate provider/lock/refresh work; nested phases overlap and cannot be added together. Phase logs contain fixed names and durations, not argv, provider output or environment values.

**External local change:** rename a known empty registered workspace directory away, explicitly collect, then restore the same directory and collect again. The new integration fixture checks `ok -> partial -> ok` through the real socket and existing Sources rendering. It also proves that the filesystem rename alone does not publish, the registry ref survives, and no runtime or cleanup success is inferred. The current pending marker fences known Radar resource mutations; an external partial observation does **not** itself advance that epoch. Source health must therefore remain independently visible, even when the marker is false.

## Measurements and boundaries

Twenty samples per row on 2026-11-17: Linux `7.0.0-43-generic`, amd64 sandbox, QEMU virtual CPU, two visible CPUs, Go `1.26.10`, workspace-backed ext4 fixture filesystem. Warm newly created metadata, no cache eviction or real SBX/tmux/network providers. Raw samples: [reactivity-baseline.csv](performance/reactivity-baseline.csv). Values below are median / nearest-rank p95 / maximum in milliseconds; all samples, including tails, are retained. Each `-benchtime=1x` external sample measures the missing-anchor direction; the functional test also covers restoration. These are shared-sandbox sample percentiles, not production percentiles or before/after speedups.

Input fixtures have 200 synthetic registered-workspace tasks, one ref each, a 120x40 viewport and cleanup A active. Each sample resets to the same model and measures one key plus a constructed `View` string. The command returned by a key is not executed. Real command-loop behavior is checked separately by teatest.

| Input | Update | View | Total benchmark |
| --- | --- | --- | --- |
| Navigate down | 1.43 / 2.06 / 2.19 | 2.33 / 4.56 / 5.66 | 3.97 / 5.96 / 7.16 |
| Type search character | 1.61 / 1.80 / 1.86 | 2.44 / 5.52 / 5.60 | 4.19 / 7.13 / 7.37 |
| Open Inspect | 0.20 / 0.34 / 0.37 | 1.05 / 3.98 / 5.88 | 1.32 / 4.32 / 6.12 |
| Blocked Enter on B | 0.004 / 0.005 / 0.013 | 2.48 / 5.51 / 5.61 | 2.48 / 5.51 / 5.61 |

| Stage / fixture | Median / p95 / max | What is included |
| --- | --- | --- |
| Filesystem detection / request scheduling | Not event-driven at baseline | Up to the next eligible local poll; no new event scheduler is measured. Explicit fixture refresh starts immediately. |
| Workspace provider `Collect`, one missing registered anchor | 0.37 / 1.18 / 1.94 | Actual filesystem/config/registry observation, no runtime command. |
| Remaining task-service refresh cost | 10.02 / 51.79 / 60.65 | Grouping, locks, collection-attempt recording, projection/persistence and publication; **not** pure publication time. |
| Complete explicit local refresh, one anchor | 10.40 / 52.92 / 62.60 | The preceding two stages together; their independently computed percentiles are not additive. |
| Pending-epoch publication, 200-task graph | 0.005 / 0.008 / 0.010 | `MarkResourceRefreshPending` and subscriber notification only. |
| Revision-watch catch-up, 200-task graph | 9.91 / 16.12 / 54.31 | Unix connect/request, native snapshot/filtering, JSON serialization/client decode. |
| Applying that watch reply | 6.63 / 15.26 / 15.76 | Actual `model.Update`, including projection/selection maintenance. |
| Constructing updated view | 11.03 / 20.07 / 21.88 | Actual `View`, no terminal paint. |
| Publication return to constructed updated view | 32.93 / 47.27 / 61.50 | Per-sample sum of watch catch-up, Update and View. |

The watch fixture issues the old-revision request **after** publication; it measures native catch-up, not a confirmed parked subscription. The existing server tests cover immediate old-revision delivery and current-revision timeout. Neither benchmark measures OS input delivery, Bubble Tea queueing/frame pacing, terminal rendering, large real-world ref payloads, stalled-provider duration, or busy-collector scheduling. No profiler or captured production keyboard trace establishes a cause of reported slow input. Low model-only timings do not resolve that report or rule out layout/queueing costs at other sizes. Child 5 must measure the real event loop and representative payloads before attributing or fixing keyboard latency.

Local validation: the new integration fixture, all 120 benchmark samples, native package regressions, `go vet ./...`, `make build` and Darwin arm64 test compilation passed. The CLI suite excludes the existing `TestForegroundAuthentication` and `TestDashboardOpensWorkspaceSessionsE2E` fixtures, which execute scripts in sandbox `/tmp` (mounted noexec). Installer tests use a private workspace temp parent and exclude `TestOneCommandBootstrap` and `TestBootstrapHeadlessWithReadyTools`, whose sanitized subprocesses still extract into `/tmp`; the remaining installer suite passes. No sandbox/installer safeguards were changed. Race detection was unavailable (cgo disabled, no C compiler). Unskipped ordinary CI evidence is recorded separately in Linear; a cross-compile is not native macOS execution.

## Shared operation/publication contract for children 2 and 3

* Keep the existing revision watch/snapshot transport. A daemon-instance-scoped monotonic operation ID correlates start/result/status; use existing stable task/source/workspace identity, never the cursor or spinner generation. This is volatile status, not a durable job log or task-identity redesign.
* Publish accepted/running and terminal outcomes (`succeeded`, `partial`, `failed`, `canceled`) without waiting for a periodic tick. Distinguish a cancellation request from confirmed provider termination. Unknown transport outcome remains unknown until a fresh snapshot/read; never silently replay a destructive command.
* Associate operation kind, initiating task, affected resources and result with that ID. Keep active status and a bounded recent terminal projection; do not let unrelated watch/fetch replies finish another operation or erase its failure. Bound retention in the child implementation and test eviction/reconnect rather than adding persistent history.
* Native provider results determine successful removals, survivors and failures. Retain authored tasks and diagnostic context. Pending observation is not completed cleanup or a fresh usable graph. Preserve the resource-epoch/publication barrier so old reads cannot visually resurrect removed resources or certify freshness.
* Completion/partial failure requests authoritative local reconciliation promptly but returns its pending acknowledgement independently. Coalesce requests with a bounded maximum deferral; do not queue one collector per event. Collection success, partial/error and cancellation remain distinct.
* Navigation/search/Inspect never wait for provider commands. Activation/reopening of B may proceed only after checking that its workspace/member/runtime/session resources do not conflict with A, then applying the existing fresh registration/ownership/lease checks. Reject shared or unprovable conflicts explicitly. No global gate removal that bypasses destructive or reopening safety.
* TUI, CLI and Pi use the same daemon publication/reconciliation path. Reconnect gets a current snapshot, restart creates a new operation-ID namespace and re-observes actual resources. No durable operation recovery or automatic command replay is implied.

## Child-4 watcher target and fallback

Evidence adjusts the initial candidate: **registered anchor directory presence, displayed through existing workspace source health**, not a newly invented per-row availability badge. The provider retains `ProvidesWorkspace` even when the anchor is missing; do not claim that flag becomes false. Missing member directories, Git dirty/index/branch state, arbitrary note contents and SBX/tmux runtime lifecycle are not qualified initial coverage.

Watch the configured workspace root's direct entries and its parent for root removal/recreation. Match known registered anchor names; do not recursively watch member trees. Add/rearm watches after rename, atomic replacement or parent/root recreation, then perform a fresh bounded local observation. Events are invalidations only: never synthesize authoritative refs, successful cleanup or ownership from an event. Registry/task changes continue through their native authority; configuration-root changes follow existing reload/restart semantics.

Use a bounded coalescer with at most one active local read plus a remembered dirty follow-up. A continuous event burst must not perpetually reset a trailing debounce. Overflow, a dropped registration or rearm failure requests a recovery read and leaves watcher health truthful; unavailable/unsupported watchers retain periodic local reconciliation. Do not change fallback polling cadences or add configuration choices in this work. SQLite/WAL, whole-repository recursion, remote webhooks and provider-native event platforms remain out of scope.

## Budgets for follow-on qualification

Retain the epic's **provisional** p95 targets: input/visible feedback <=100 ms and publication-to-visible-render <=250 ms on a documented representative end-to-end fixture. The model/catch-up measurements leave headroom but cannot confirm those end-to-end targets. Do not weaken or advertise a production guarantee from these samples.

For managed or supported filesystem invalidations, evaluate a **maximum 500 ms from invalidation receipt to reconciliation request**, including under continuous bursts, using a deterministic/fake clock. Measure queue wait, authoritative collection, publication, watch and actual render separately. The 500 ms budget excludes collector/provider execution and does not mean a busy collector finishes in 500 ms. Preserve 15-second local/two-minute full recovery cadences and report provider/lock failures separately. CI should gate ordering, request counts, maximum fake-clock deferral and stale-result rejection; repeated wall-clock measurements support the budget, not replace those checks.
