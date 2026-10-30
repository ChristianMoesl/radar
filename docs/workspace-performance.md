# Workspace performance

This implements the bounded optimizations in [issue #51](https://github.com/ChristianMoesl/radar/issues/51). Full context, planning, setup scheduling, and cleanup retain their existing lifecycle, confirmation, and synchronous completion contracts. No persisted schema, command, configuration option, general cache, daemon lifecycle, or sandbox image changes are introduced.

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
| `collection_lock_wait`, `local_collection`, `post_mutation_collection` | Collection mutex wait, actual refresh, and caller wait for refresh |

Nested timings overlap and must not be added together. SBX retries produce one timing per attempt. Interactive setup timing ends after scheduling; it does not claim that the setup commands finished. Creation currently has no CLI post-operation collection; reconciliation and explicit cleanup retain their existing synchronous refresh behavior.

## Measurements

Measurements were collected on 2026-10-30 in a Linux `6.18.57` amd64 cloud container, five visible CPUs (Intel Xeon Platinum 8573C), Go `1.26.4`, overlay filesystem. Baseline source: `21e4af9`. Optimized source: this change on `c4eae81`; intervening upstream commits did not change workspace code. SBX is not installed in this environment. The issue's macOS arm64 / SBX v0.50.0 measurements remain a separate real-provider baseline.

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

## Explicitly deferred decisions

### Live provider creation/readiness and image selection

This environment cannot reproduce SBX v0.50.0 provisioning. No disposable live sandbox was created or deleted, and no image, kit, mount, startup, readiness, or environment behavior was changed. A separate provider run must name an approved disposable sandbox, record host/provider/image/kit versions and warm/cold conditions, and gather repeated create/remove/readiness/port/setup samples using the new phase logs. Never collect private startup output or environment contents as timing data. Choose a follow-up based on the dominant measured phase. The issue's estimated one-second local context goal is unverified here; synthetic timings are not a substitute.

### Lighter Pi lifecycle inspection (item 4)

The evaluated path is a fresh registry/member-resource/routing read for `resources_discover`, reload, and `before_agent_start`, while keeping full live context for explicit inspection/reconciliation. This could omit repository discovery, dirty Git status, and port reads in hooks. It would need to retain project-trust skill loading, member precedence, missing-runtime errors, shared-directory mount readiness, fail-closed tool routing, and the existing previous-resource-set behavior on failed reloads.

That changes what hooks observe and when resources become active. It is separately scoped and deferred pending explicit approval of those semantics, as required by the epic. Current hooks continue to obtain full fresh context; there is no cross-run context cache. A future approved design must reread registration on every hook, refresh member instruction/skill content on reload and before a run, invalidate routing on sandbox/registration/shared-mount changes, and never pass a lighter result to reconciliation as a revision-bearing full context.

### Targeted post-mutation publication (item 5)

The evaluated path is publication of the affected workspace's local resource observations through the existing task-service revision barrier after mutation. Correct implementation needs an atomic way to remove departed worktrees, update retained anchor/runtime observations, preserve task linking and authored fields, and reject observations collected before a concurrent mutation. A targeted implementation must also define which remote publication proofs are refreshed.

An alternative early acknowledgement would require an explicit pending-refresh result and a follow-up publication contract; current JSON has no such state. Both choices change response/consistency behavior and are separately scoped and deferred pending approval. Current cleanup/reconciliation still wait for global local-source collection and keep their existing warnings on refresh failure. Logs now distinguish resource removal, mutex waiting, and final collection, so unrelated refresh latency can be attributed without silently labeling stale resources fresh.

The global note-lock critical section is unchanged. Narrowing it requires an ownership/reservation design and evidence from the new wait timings. These deferred decisions and real-provider qualification mean the epic should remain open.

## Validation

The Go suite and Linux installer checks pass with the test fixtures' expected `022` umask. Pi type checking and all 312 Pi tests pass; the resource/startup subprocess tests were run serially to avoid cloud process/CPU contention. Durable-agent type checking and tests pass (25 passed, five credential-dependent cases skipped). Race checks cover workspace, Git cleanup, bundle cleanup, and timing diagnostics. Darwin arm64 and Linux arm64 CLI cross-builds pass; this does not claim native execution on those platforms.

New tests cover normalized legacy results, command budgets, deterministic results/error ordering, bounded concurrency/deadlines, unavailable and missing runtimes, live port changes, fresh apply ownership, phase privacy, setup preflight count/failure behavior, fetch failures/cancellation, nested checkouts, symlink changes, canonical notes, and changed ownership/configuration between preflight and removal. Existing stale revision/plan, dirty-worktree, lease, Pi startup/routing, and durable-agent contracts remain exercised by the existing suites.
