# Workspace performance

This implements the bounded optimizations in [issue #51](https://github.com/ChristianMoesl/radar/issues/51). Full context, planning, setup scheduling, and cleanup retain confirmation and destructive completion checks. Lifecycle hooks use a fresh narrower projection, and mutation acknowledgements explicitly mark pending resource collection. No persisted schema, command, configuration option, general cache, daemon lifecycle, or sandbox image changes are introduced.

## Inspection and planning

A registered workspace inspection uses one SBX list observation for ownership, mounts, shared-directory readiness, and port discovery. Ports are read only after the listed runtime passes the existing ownership check. Repository discovery, sandbox observation, and member Git statuses run through at most four read workers. Results and errors are assembled in their previous deterministic order. Member instructions, skills, desired state, and repository ordering remain the same.

The list and port reads each have a ten-second maximum budget and inherit shorter caller deadlines. An unavailable CLI, provider error, malformed list, failed ownership check, or port error does not become a successful empty-runtime observation. Context retains recorded desired ports on runtime errors; a missing runtime retains the existing absence diagnostic. A missing CLI now explicitly reports runtime unavailability. Healthy and stationary failure results are checked against normalized full-result digests captured from `21e4af9`.

Reconciliation planning reuses its sandbox observation instead of listing it again for mounts. Initial creation shares its planning preflight, and prepare-only retry planning shares its list/port observation. Session-existence checks run once per planning phase. Setup performs a fresh sandbox preflight once per setup batch, instead of once per member.

All reuse is scoped to one read-only phase or one setup batch. A new context call, apply-time plan, runtime preparation, and final revision read observe state again. No preview observation is used to authorize apply. Worktree changes still force subsequent runtime checks; runtime recreation, readiness gates, fresh port reconciliation, revision checks, and plan confirmation stay in place.

## Cleanup safety and scope

Cleanup shares successful origin fetches only within a fresh preview/planning verification phase. Each branch still receives its own ten-second verification budget and current local-ref, reachability, and exact GitHub merged-head checks. Failed fetches are retried using the existing policy and never cached as success. Apply-time automatic deletion performs fresh verification rather than reusing preview fetches. The existing collection-only remote observation cache is unchanged.

Discarding cleanup now follows this sequence:

1. Acquire the complete existing set of agent/workspace leases.
2. Run a fresh workspace bundle preflight before any provider removes a resource. Under the existing note lock, scan each affected anchor once, including unrelated members and unknown entries, for protected or unidentified Git checkouts and protected paths.
3. Immediately before removing a Git member, reload registry/configuration protections and validate the actual repository, branch, Git registration, lock state, canonical notes, mounts, and ownership. Traverse only that member for nested Git checkouts, from the pinned `os.Root` anchor. Prune unrelated trees below the anchor rather than opening a member path as the walk root.
4. Before final anchor removal, run the existing full validation of all remaining entries and rooted deletion checks again.

The bundle preflight does not replace last-moment checks. A checkout inserted into a later member after preflight is rejected when that member is reached. Changes to canonical-note placement, configuration, branch/repository ownership, or symlink targets also block removal. Safe cleanup keeps dirty-worktree and publication protections. Explicit confirmation/expiry keep their existing discard rules. Cancellation is checked before preflight and each resource removal.

## Phase diagnostics

Existing Radar logs contain `operation phase completed` records with a fixed `phase` and `duration`. A duration describes time spent in that phase, including failed operations; it is not a success indicator. These diagnostics do not include argv, environment values, credentials, provider output, or startup-hook output.

| Phase | Meaning |
| --- | --- |
| `operation_lock_wait`, `note_lock_wait` | Workspace mutation lock acquisition, separately from provider work |
| `workspace_planning` | Fresh creation/reconciliation planning |
| `worktrees` | Worktree creation/removal and member publication |
| `sbx_version`, `sbx_ls`, `sbx_ports` | Individual provider preflight/observation commands |
| `sbx_rm`, `sbx_create` | Individual provider removal/creation attempts |
| `sandbox_readiness` | Optional readiness gate, separately from create |
| `port_reconciliation` | Port observation and publish/unpublish work |
| `workspace_setup` | Synchronous prepare-only setup or asynchronous interactive setup scheduling |
| `workspace_create`, `workspace_apply` | Total workspace operation before CLI post-operation collection |
| `cleanup_preview`, `branch_publication_verification` | Preview and per-branch publication verification |
| `cleanup_bundle_preflight`, `cleanup_protected_scan`, `cleanup_member_scan` | Bundle protection work and each target's last-moment scan |
| `cleanup_runtime`, `cleanup_worktree`, `cleanup_anchor` | Actual provider cleanup, distinct from collection |
| `cleanup_execution`, `cleanup_response` | Removal bundle and server response totals |
| `worktree_removal`, `managed_worktree_removal` | Git worktree lookup/removal and managed branch handling, excluding member-tree scanning |
| `post_mutation_publication` | Immediate pending-publication acknowledgement, separate from scheduled collection |
| `collection_lock_wait`, `local_collection` | Collection mutex wait and actual refresh |

Nested timings overlap and must not be added together. SBX retries produce one timing per attempt. Interactive setup timing ends after scheduling; it does not claim that the setup commands finished. Creation currently has no CLI post-operation collection; reconciliation and explicit cleanup return their explicit pending marker before scheduled collection.

## Measurements

Measurements were collected on 2026-10-30 in a Linux `6.18.57` amd64 cloud container, five visible CPUs (Intel Xeon Platinum 8573C), Go `1.26.4`, overlay filesystem. Baseline source: `21e4af9`. Synthetic optimized source: `c1992d9` on `c4eae81`; intervening upstream commits did not change workspace code. SBX is not installed in this environment. The issue's macOS arm64 / SBX v0.50.0 measurements remain a separate real-provider baseline.

Each row has 20 before and 20 after samples (`-benchtime=1x -count=20`). Fixture construction is outside the timer; files and directories were just created, so these are warm filesystem/metadata conditions, with no deliberate cold-cache runs. Go dependency download/build time is excluded. The shared cloud environment had other build/test activity; paired progressive scans also shared filesystem load. Percentiles are nearest-rank sample statistics, not production percentiles. The raw observations are in [workspace-51.csv](performance/workspace-51.csv).

| Fixture | Before median / p95 / max | After median / p95 / max | Median change |
| --- | ---: | ---: | ---: |
| Full context | 179.62 / 181.52 / 210.50 ms | 102.81 / 199.36 / 200.75 ms | -42.8% |
| Zero-change planning | 201.89 / 211.53 / 244.93 ms | 151.75 / 152.43 / 152.47 ms | -24.8% |
| Fixed-tree member validation | 540.96 / 886.11 / 1014.27 ms | 277.55 / 372.34 / 425.05 ms | -48.7% |
| Progressive member validation | 484.83 / 876.30 / 1439.44 ms | 428.41 / 835.71 / 1315.66 ms | -11.6% |

Median reductions do not establish a tail-latency improvement: context and progressive-scan p95s are noisier under concurrent cloud work. The raw maximums and p95s are retained rather than discarding slow samples. Repeat under isolated real-provider conditions before setting a latency guarantee.

The context fixture has four registered members and 62 discovered repositories. It injects 50 ms per SBX command, 20 ms for repository discovery, and 1 ms for Git probes. The planning fixture has no member changes and injects the same SBX latency. These measure actual optimized API scheduling with a fake provider, not SBX VM creation or live context latency. Context uses one list plus one dependent port read, versus two lists plus ports before. Zero-change planning uses one version, one list, and one port read, versus version, two lists, and ports before.

The cleanup fixture has four members, 800 child directories, and 80,000 files (80,804 entries including member directories). The fixed fixture times four last-moment checks without removing members: before, four complete scans; after, one bundle scan plus four target scans. The progressive fixture moves each member outside the anchor with the timer stopped, modeling the shrinking remaining workspace. Before it traverses 4 + 3 + 2 + 1 member trees; after it traverses one full bundle plus each removal target. Neither fixture times Git worktree deletion, provider removal, remote verification, final anchor deletion, or response/collection delay. These numbers must not be presented as total cleanup speedups.

Run the current fixtures with:

```sh
go test -p 2 ./internal/integration/workspace -run '^$' \
  -bench 'Benchmark(WorkspaceContext|WorkspacePlanning|CleanupMemberValidation|CleanupProgressiveValidation)$' \
  -benchtime=1x -count=20
```

To reproduce the before comparison, use the same fixture setup on `21e4af9`: the fixed cleanup loop calls `ValidateDiscardingWorkspace` for each member without a bundle preflight; the progressive loop does the same while moving each checked member out of the anchor. The optimized loops include their new bundle preflight. Registry/metadata protection checks are included in both. Legacy result digests were captured separately from that source using the same inspection fixture.

## Lifecycle resource inspection

The remaining lifecycle/publication work was requested on 2026-10-31 after the designs above had been documented. Both are implemented.

`workspace-context --resources-only` rereads the registry, direct member instruction/skill locations, canonical-note association, and one owned live sandbox observation. It omits repository discovery, Git status, ports and opening resources. Its dedicated response type has no revision or desired state, so it cannot authorize reconciliation. `--registration-only` remains a registry lookup; the explicit `radar_workspace_context` tool still obtains full live context.

Pi uses the resource projection for discovery, reload, before every agent run and after reconciliation. Member order, instruction precedence, project-trust gating, skill collision handling, and previous-resource retention on failed reload remain in effect. Instruction contents are read on each agent start. Every hook rereads registration and runtime mounts; there is no cross-hook cache or expiry interval. Removed members/skills, canonical-note changes and sandbox/shared-directory changes appear on the next hook. An unverified hook blocks ordinary tool/user-shell routing until a successful fresh read; repair tools remain available. A verified non-workspace session restores ordinary routing.

## Mutation acknowledgement and resource freshness

Cleanup, reconciliation and manual GC acknowledge completed mutations without waiting for unrelated global collection or its mutex. Cleanup still finishes the actual provider removals before returning. Partial failures remain errors and include the successfully removed prefix.

Responses carry `resource_refresh_pending: true` while their cached resource list awaits a fresh scheduled collection. The dashboard labels that condition, and readable cleanup/reconciliation output reports it. The existing local-refresh cadence (15 seconds) and explicit `refresh-local` request perform collection; no deletion is deferred. Disabled collection leaves the marker pending until an explicit refresh. Cached resource-opening and cleanup previews/apply are rejected while pending, including reuse of a previously confirmed cleanup plan; GC also requires a fresh collected graph. Explicit full inspection and reconciliation continue to read live state.

The task service increments a volatile resource epoch after mutation and wakes revision subscribers. In-flight collections captured before that epoch cannot publish at the completion barrier. A mutation arriving during publication still invalidates its freshness certificate. The resource snapshot captures the store revision and epoch so an older projection cannot carry a newer fresh marker. A complete collection started after the mutation clears the marker; partial, failed or canceled collection does not. Disabled sources stay explicitly disabled. The marker is unverified on daemon startup and is not persisted, and no on-disk format changes.

The CLI and dashboard mutation notifications have a one-second request budget. The mutation notification and pending snapshot do not wait for the authoring/publication lock, which may be reconciling unrelated work. The atomic epoch and published store revision preserve ordering. Authored fields, bindings, leases, confirmation and destructive checks continue through the existing paths. A failed reconciliation notification returns a warning and keeps its result marked pending.

## Real-provider qualification

The trusted-main `Workspace performance qualification` workflow installs checksum-pinned SBX v0.50.0 on a fresh Linux/KVM runner using the repository's existing registry credential path. It uses a pinned, previously published Radar image and an isolated kit without user hooks or external writable mounts. Each invocation owns twenty named disposable workspaces/VMs, four linked worktrees and a large file fixture. The provider's original XDG selectors are restored only for SBX; credentials are neither copied into fixtures nor mounted in VMs.

The harness alternates twenty before/after live context and zero-change previews against `21e4af9`, checks the one-list command budget, measures the lighter lifecycle projection, and records twenty complete creation/cleanup cycles. It separates lock wait, worktrees, provider create/remove, readiness, preview, fresh publication verification, protected scans, Git deletion, mutation acknowledgement and final collection. Raw command output, command arguments, environment values and startup output are excluded from the uploaded report. The first provision and subsequent warm-image/new-VM conditions are labeled separately. Canonical notes must survive every cleanup.

Run `TestWorkspaceLivePerformance` only in that explicitly disposable qualification environment. Ordinary tests skip it. Live results are recorded separately from the synthetic measurements above. No image, kit, startup or mount behavior was changed based on guessed provider costs. The note-lock critical section remains unchanged; its new measurements support a reservation/ownership design only if contention warrants one.

## Validation

The Go suite and Linux installer checks pass with the test fixtures' expected `022` umask. Pi type checking and all 314 Pi tests pass; the resource/startup subprocess tests were run serially to avoid cloud process/CPU contention. Durable-agent type checking and tests pass with a built Radar binary (27 passed, three live-provider cases skipped locally). Race checks cover workspace, Git cleanup, bundle cleanup, and timing diagnostics. Darwin arm64 and Linux arm64 CLI cross-builds pass; this does not claim native execution on those platforms.

New tests cover normalized legacy results, command budgets, deterministic results/error ordering, bounded concurrency/deadlines, unavailable and missing runtimes, live port changes, fresh apply ownership, phase privacy, setup preflight count/failure behavior, fetch failures/cancellation, nested checkouts, symlink changes, canonical notes, and changed ownership/configuration between preflight and removal. Existing stale revision/plan, dirty-worktree, lease, Pi startup/routing, and durable-agent contracts remain exercised by the existing suites.
