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
2. Run a fresh workspace bundle preflight before any provider removes a resource. Capture exact registered owners and pinned anchor handles under a short note transaction, then scan each affected anchor once outside that transaction, including unrelated members and unknown entries, for protected or unidentified Git checkouts and protected paths. Before returning, recheck target bindings, exact owners, configuration, protected references and pinned filesystem identities under a fresh note transaction. Direct preflight is read-only and does not assume Service's provider-bound admission exists.
3. Immediately before removing a Git member, reload registry/configuration protections and validate the actual repository, branch, Git registration, lock state, canonical notes, mounts, and ownership. Traverse only that member for nested Git checkouts, from the pinned `os.Root` anchor. Prune unrelated trees below the anchor rather than opening a member path as the walk root.
4. Before final anchor removal, run the existing full validation of all remaining entries and rooted deletion checks again.

The bundle preflight does not replace last-moment checks or confer deletion authority. Its rooted classification/traversal no longer holds the global note callback, so unrelated registered missing-runtime recovery may proceed during that phase; Service retains the existing full resource admissions. Owner/configuration/reference drift or anchor replacement during scanning fails before provider effects, without changing registrations. Registered SBX runtime cleanup also uses the bounded metadata/provider split described below. Git member work, name-only managed SBX fallback and note archival retain their separate existing note scopes; these bounded changes do not establish whole-cleanup overlap or remove the blanket UI activation gate. A checkout inserted into a later member after preflight is rejected when that member is reached. Changes to canonical-note placement, configuration, branch/repository ownership, or symlink targets also block removal. Safe cleanup keeps dirty-worktree and publication protections. Explicit confirmation/expiry keep their existing discard rules. Cancellation is checked before preflight and each resource removal.

### Registered SBX provider boundary

Registered sandbox removal and retirement keep the complete canonical workspace-path and reusable-name deletion admission throughout provider lookup, effects and outcome checks. Cleanup Service binds only its exact admitted targets and mode, reusing its full bundle rather than reacquiring exclusive leases. Direct registered callers obtain the same actual-owner path/name admission and durable-agent/update protection themselves; a conflict or cancellation releases every partial acquisition. Name-only and standalone cleanup also acquire the matching reusable-name lease, so those entries cannot bypass a paused registered cleanup. The legacy managed-name fallback retains its existing note callback and fresh owner check.

Only short callbacks capture and freshly validate the complete owner, configured root and root/anchor filesystem identities. Actual `sbx ls`, removal and retirement commands run outside the global note callback. Drift after lookup refuses further effects; drift or failure after removal remains an error and keeps the registration/canonical note for inspection. Removal still addresses only the previewed runtime ID and requires a complete final listing proving absence; an appeared or replacement runtime is never adopted. Retirement retains its existing name-addressed `stop` under the reusable-name lease, never removes the sandbox, and is not a guarantee against an external provider replacing a name during a command. Returned retirement metadata does not mutate the caller's preview.

Public Source and actual Service tests pause real fixture subprocess commands during lookup, removal, post-removal absence and retirement. Both registered recovery entries complete for unrelated sandboxless/SBX workspaces before cleanup resumes; same-owner, physical-alias and reusable-name conflicts refuse effects. Separate cases retain owner/configuration/replacement/cancellation/provider-error/partial-outcome checks and unrelated registry changes. These fake-provider tests share the existing test-only platform seam: they do not execute native macOS, WSL, KVM or real SBX lifecycle work. Git/member, normal managed-name fallback and Obsidian reference/relocation transactions still require separate review before selective UI acceptance.

### Registered tmux retirement boundary

Registered tmux retirement retains exclusive reusable-name/incarnation admission across identity verification, listing and ID-addressed kill. Direct callers also retain the actual owner's canonical path deletion admission and durable-agent/update protection; Service reuses its complete path bundle only for the exact admitted targets and mode. Short note callbacks capture and freshly revalidate the complete owner, configured root and root/anchor filesystem identities, while native commands run outside the global note callback.

Owner/configuration/filesystem drift, cancellation, unowned session paths and changed native lifetimes refuse further effects. Post-kill failures remain honest partial failures, retaining registration and canonical notes; retirement result metadata does not mutate the preview. Missing runtimes remain recoverable, but a no-server observation cannot hide cancellation or ownership drift. Commands still address only the admitted numeric ID; native leases protect cooperating Radar callers, not an external server restart during a command.

Public direct/Service fixture subprocess tests pause actual retirement verification, listing and kill while both registered recovery entries complete for unrelated sandboxless/SBX workspaces. Matching owner/physical-alias/native-name conflicts, durable-agent ownership, exact binding, provider failures, replacement and lease release are covered separately. The existing fake-platform seam is test-only; native macOS/WSL execution is not claimed locally. Normal managed-name tmux cleanup, Git/member work, managed-name SBX fallback and Obsidian atomic transactions remain separate scopes; the blanket UI activation gate is unchanged.

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

Run `TestWorkspaceLivePerformance` only in that explicitly disposable qualification environment. Ordinary tests skip it. No image, kit, startup or mount behavior was changed based on guessed provider costs. The note-lock critical section remains unchanged; its new measurements support a reservation/ownership design only if contention warrants one.

### Completed live qualification

[Run 41192847275](https://github.com/ChristianMoesl/radar/actions/runs/41192847275) passed on 2026-10-31: twenty new VMs were created, became ready, and were completely removed with their four-member workspaces. Every canonical note survived. The fixture had 80,817 actual entries and 62 discoverable repositories. The runner was Linux amd64, kernel `7.0.0-1017-azure`, Go `1.26.4`, with SBX client/server `v0.50.0` at `30d023cbbf9af1204c91b1605b2a7a49b931d2bf`. Baseline source was `21e4af9`; optimized source was `58c2a71a8ef875c22528aeb7a2a651beaa2cc6e2`.

The image was `docker.io/christianmoesl/radar-sandbox:2026.10.20-3ed5104a8b82-39452053689-1`. [The raw JSON](performance/workspace-51-live.json) preserves all 1,120 timing observations and command counts from the sanitized job log; GitHub's Docker Hub namespace mask is retained in its image field. Fixture files were newly written, with warm metadata and no cache eviction. Read comparisons alternated before/after order on the same healthy VM. These Linux results are separate from the issue's original macOS measurements.

All read rows have twenty samples per version. Values are median / nearest-rank p95 / maximum in milliseconds; tails include every observation.

| Live operation | Before | After | Median change |
| --- | ---: | ---: | ---: |
| Full context | 265.34 / 292.72 / 306.37 | 182.61 / 211.83 / 474.68 | -31.2% |
| Zero-change preview | 336.12 / 391.67 / 395.81 | 272.03 / 474.49 / 642.36 | -19.1% |
| Lifecycle resources | — | 85.91 / 140.04 / 162.23 | After only |

Every paired full-context result was equal between versions. Every optimized context used one list and one port read, versus two lists and one port read before. Every optimized zero-change preview used one version, one list and one port read, versus version, two lists and ports before. Every lifecycle read used one list and no port read. The context maximum and preview p95/maximum worsened despite lower medians; this run establishes neither a tail-latency guarantee nor a portable macOS target.

The following measurements are optimized-source observations, without a before/after total-operation speedup claim. Times are milliseconds. Per-member rows have eighty samples (four members in each of twenty bundles); all other rows have twenty. Nested phase timings overlap.

| Phase/operation | Median | p95 | Maximum |
| --- | ---: | ---: | ---: |
| Complete creation, including first provision | 4,721.39 | 5,537.13 | 33,698.48 |
| `sbx_create` | 2,712.16 | 3,614.49 | 31,694.74 |
| Readiness after create | 146.10 | 176.71 | 185.90 |
| Four worktree creations | 58.26 | 60.92 | 62.55 |
| Note-lock wait, uncontended fixture | 0.010 | 0.035 | 0.048 |
| Setup, prepare-only fixture without user hooks | 0.899 | 0.970 | 1.055 |
| Fresh cleanup preview | 261.11 | 276.72 | 279.82 |
| Publication verification, per member | 3.20 | 10.69 | 11.71 |
| Complete cleanup execution | 1,591.10 | 1,739.01 | 3,417.71 |
| Bundle protection preflight | 165.30 | 169.87 | 173.36 |
| Last-moment member scan, per member | 42.65 | 45.50 | 81.27 |
| Actual Git worktree removal, per member | 240.18 | 256.48 | 304.94 |
| Runtime removal | 174.95 | 202.00 | 1,679.74 |
| Final anchor removal | 1.46 | 2.22 | 8.91 |
| Pending marker and snapshot, in process | 0.008 | 0.009 | 0.010 |
| Subsequent local collection | 95.63 | 104.62 | 130.37 |

Cleanup execution includes the actual VM, Git worktree/branch and anchor removals, leases and final validations. The pending-marker measurement excludes RPC/CLI overhead and runs after deletion; the separate blocked-collection server regression verifies that acknowledgement does not wait for collection. Local origins keep publication verification deterministic and do not represent GitHub/network latency. Each fresh preview fetched the shared origin exactly once; collection's separate observation cache did not authorize it.

The first provision took 33.70 seconds, of which 31.69 seconds were inside `sbx create`. The subsequent nineteen warm-image/new-VM creations took 4.71 seconds median and 5.54 seconds maximum; their `sbx create` phase took 2.69 seconds median and 3.61 seconds maximum. This attributes the remaining creation cost to the provider call rather than Git worktrees or the readiness gate. The evidence-backed next investigation is to split image transfer/unpack and VM boot inside SBX under the same pinned image and readiness contract before proposing an image/kit change. The uncontended note-lock measurements provide no evidence for narrowing its safety-critical section.

For cleanup, the sum of the four actual Git removals was 963.98 ms median per bundle, versus 171.90 ms for the four member scans and 165.30 ms for bundle preflight. The longest runtime removal was 1.68 seconds, contributing to the 3.42-second cleanup maximum. Further total-cleanup work should therefore profile Git deletion and provider removal; the synthetic before/after traversal gains above must not be presented as total deletion gains.

## Validation

The Go suite and Linux installer checks pass with the test fixtures' expected `022` umask. Pi type checking and all 314 Pi tests pass; the resource/startup subprocess tests were run serially to avoid cloud process/CPU contention. Durable-agent type checking and tests pass with a built Radar binary (27 passed, three live-provider cases skipped locally). Race checks cover workspace, Git cleanup, bundle cleanup, and timing diagnostics. Darwin arm64 and Linux arm64 CLI cross-builds pass; this does not claim native execution on those platforms.

The completed [CI run for `58c2a71`](https://github.com/ChristianMoesl/radar/actions/runs/41192841028) additionally passed native macOS Go tests, notifier/archive builds, provider CLI contracts on macOS arm64 and Linux amd64/arm64, both Linux Homebrew installs, rootless installers on Debian/Ubuntu/Fedora/Arch, Pi, durable-agent recovery, and a durable agent in real SBX. That existing agent job uses SBX v0.48.0; the performance qualification above separately pins v0.50.0.

New tests cover normalized legacy results, command budgets, deterministic results/error ordering, bounded concurrency/deadlines, unavailable and missing runtimes, live port changes, fresh apply ownership, phase privacy, setup preflight count/failure behavior, fetch failures/cancellation, nested checkouts, symlink changes, canonical notes, and changed ownership/configuration between preflight and removal. Existing stale revision/plan, dirty-worktree, lease, Pi startup/routing, and durable-agent contracts remain exercised by the existing suites.

## Cleanup and activation lock-scope review

**Scope review and workspace-writer admission, not a note-lock narrowing.**
The caller-owned native-session admission work at `b952dab` (test isolation
correction `8ac0011`) closes constructor-to-activation handoff gaps. The matching
canonical bundle admission described below covers `Create(existing)` and the
workspace-side mutation entries. Short-lived exclusive mutation admission is
separate from long-lived agents' shared deletion admission. None of these
changes makes registered missing-runtime recovery independent of the global
note transaction or removes the dashboard's blanket activation gate.
This review records the remaining boundary before changing that critical section.

### Existing authority and lock scopes

| Protection | Current scope and purpose | What it does not establish |
| --- | --- | --- |
| `operationlock.Acquire(false)` | Shared, nonblocking accepted-mutation protection against a managed macOS update; retained through cleanup or activation. Other platforms currently return a no-op. | Mutual exclusion between two ordinary mutations, or native macOS execution from a Linux test. |
| Canonical workspace leases | Cleanup acquires the complete exclusive path set before any provider. Workspace activation and writers retain shared anchor/member leases; reconciliation and preparation include current and desired members. Physical aliases, including missing leaves, resolve to the same identity. | Shared deletion admission alone does not exclude another writer. Different task IDs are not independent resource identities. |
| Workspace mutation leases | Short-lived exclusive canonical path and reusable SBX-name leases serialize workspace-side writers and activation, independently of agents' shared deletion leases. Canonical aliases in a bundle are deduplicated before acquisition. | A replacement for note relocation/reference transactions, or admission for writers outside these workspace entries. |
| Protected-reference admission | Workspace writers hold shared deletion leases for current/desired anchors, members, canonical notes, primary repositories, shared directories and additional/effective mounts, including lexical and physical ancestors. Standalone sandbox creation admits its planned mounts before directory creation. Ordinary note creation/path association and relocation also admit their affected paths before effects. | Exclusive mutation authority over shared references, protection against external filesystem changes, or independence from the remaining note transaction. |
| Sandbox name admission | Cleanup's exclusive reusable-name lease matches activation's retained shared lease, including pathless or differently rooted targets. | Permission to adopt a replacement runtime or weaken provider ownership checks. |
| Native tmux admission | Retained exact missing name and selected/emitted server/session incarnation, with numeric targets and fresh revalidation before later effects. | Note/registry consistency, or permission to release and reacquire a reusable name between phases. |
| `workspacegroup.WithNoteLock` | Process-global `noteMu`, then root-local blocking `.radar-notes.lock` flock; serializes workspace creation and note lifecycle/relocation transactions. Direct authored note creation instead retains path admission. | Cancellation-aware waiting, per-workspace independence, or a lease held only for one note. Changing only the file-lock key would leave process-global contention. |
| Registry lock | Process-global registry mutex and root-local shared/exclusive file lock around each atomic load/update. It is separate so a note callback can use the registry. | A snapshot pinned across provider work, or a transaction spanning multiple registry calls. |

These are distinct contracts, not interchangeable locks. In particular, atomic
registry updates prevent lost whole-file writes, but do not by themselves stop
an operation from acting on an obsolete workspace description between updates.

### Call sites that a scope change must cover

- `workspace.go`: `OpenRegisteredWorkspace` obtains operation, canonical bundle
  and native admission, rechecks registration, and bypasses the note lock only
  for an already-live session. Missing-session recovery holds the note lock
  across `openRegisteredWorkspaceAt`, preparation, provisioning, pane release
  and final switching.
- `create.go`: `Create` takes the note lock before planning and keeps it through
  creation **and opening an existing registration**. At the reviewed `8ac0011`
  baseline, this second route omitted `acquireActivationLeases`: the note lock
  did not exclude cleanup's earlier runtime/member providers. The bounded fix
  adds matching bundle admission through runtime return and registration
  revalidation before and after native probing, without narrowing the note
  transaction. Planning's read-only SBX observation remains before admission;
  no recovery, provisioning or native effect gains authority from that read.
- `runtime.go`: preparation validates the canonical note and may repair the
  managed `notes.md` link and shared directory. Sandbox lookup, creation and
  readiness can also run inside the caller's note transaction. These phases
  must not be treated as read-only just because no new registration is created.
- `reconcile.go` and `recreate_sandboxes.go`: their public mutation entries hold
  the note lock while planning, changing registered members/note/runtime data,
  provisioning, setup and publication. They do not obtain the matching canonical
  bundle leases at the original review baseline. They now retain shared cleanup
  admission plus exclusive mutation admission through effects and return, with
  registration checks after admission and read-only preflight. Reconciliation
  protects the union of old and desired members; recreation handles conflicts
  per target without discarding independent results. The note span is unchanged.
- `anchor_cleanup.go`, `shared_directory.go` and `source.go`: cleanup's fresh
  protected-path/ownership preflight still scans under the note lock. Final
  anchor and recorded shared-directory scans/deletion now use pinned directory
  handles outside that transaction, retaining matching resource admission.
  Short metadata callbacks freshly check owner, configuration, canonical shared
  identity and protected references before further effects; final unregister
  compares the expected owner in one atomic registry update. Parent/child
  replacement, partial deletion and cancellation retain the final reference.
  Actual paused anchor and shared-directory filesystem tests separately cover
  unrelated registered open/create-existing recovery, with/without SBX. These
  bounded splits do not remove the preflight, Git, SBX or note-archive spans or
  establish whole-cleanup independence from the note transaction.
- Obsidian `delete.go`, `archive.go`, `details.go`, `source.go`, `mute.go`,
  `rollout.go` and state migration: note identity, binding, lifecycle, recovery
  and relocation operations depend on the same serialization. Archive,
  restore, delete and reassociation check workspace references before moving
  notes. That reference check must remain atomic with any competing registration
  or final-reference removal; it is not a substitute for the transaction.
- Workspace collection's `RefreshWorkspaceNote` can repair a link and update
  registration after an external rename without this note callback. Repairs now
  acquire update protection and the matching cleanup/mutation bundle, then
  recheck the captured registration before writing. A correct link stays a
  read-only observation without mutation admission. A global note lock alone
  still does not exclude external filesystem changes.

The acquisition sequences also differ: explicit registered open acquires native
admission before waiting for the note lock, whereas creation/reconciliation can
acquire it from inside the note callback. Preserve nonblocking resource
admission and review every new acquisition order rather than assuming one
existing total lock order. Do not hold a registry callback over provider work or
call a registry loader recursively from its update callback.

### Workspace-writer admission increment

`acquireWorkspaceLeases` retains shared cleanup admission and a distinct
exclusive mutation lease for each canonical anchor/member and reusable sandbox
name. The mutation namespace lives alongside the existing transient resource
locks; it changes no registry or note format. An agent retaining its shared
workspace lease does not prevent reconciliation. No deletion lease is upgraded.
Current and desired bundles are canonicalized, deduplicated and sorted before
nonblocking acquisition; a conflict or cancellation releases the entire partial
set in reverse order. No registry callback or provider is used as a lock waiter.

Explicit registered open, create-existing, new creation, preparation retry,
reconciliation, sandbox recreation and standalone activation all participate.
Creation acquires admission before anchor or initial registration effects.
Reconciliation retains removed members and absent additions, not just the final
membership. Recreation revalidates both registration and the confirmed runtime
before deletion, retaining admission through readiness and state restoration.
Collection repairs reject stale/removal snapshots instead of recreating links
or overwriting concurrent membership; already-correct links remain inspectable.

Regression evidence includes actual cleanup `Service` admission at paused native,
Git member-add/remove and SBX removal boundaries; canonical and missing-leaf
aliases; different anchors sharing members or SBX names; agent compatibility;
partial acquisition release; errors/cancellation; stale collection snapshots;
and independent reconciliation/live-navigation completion in both orders.
The service fixtures do not delete data. Canonical identity and mutation
namespace tests are separate from native-incarnation ownership qualification.

This is a workspace-side prerequisite, not proof that every note-lifecycle
writer has per-target admission. Obsidian relocation/reference transactions and
external changes still require the scope review below. Global note spans,
cleanup's final-anchor transaction and the blanket UI gate are unchanged. In
particular, live navigation during reconciliation is not missing-runtime
recovery during a global-note-held cleanup.

### Protected-reference admission increment

Final anchor cleanup's protected-path decision includes canonical notes, primary
repositories and external mounts nested anywhere beneath the deletion target.
Different anchors or sandbox names alone do not make a new reference safe after
that decision. Workspace writers therefore also retain shared deletion admission
for both current and desired references and their ancestors. Resolve physical
paths, including missing suffixes, and lexical ancestors before deduplicated,
sorted, nonblocking acquisition. Keep lexical views as well as physical targets:
a link inside an anchor can lose its view even when its target is elsewhere.
Invalid resolution, conflicts and cancellation release every partial admission.

These ancestor leases are **shared**, never exclusive mutation leases. Otherwise
unrelated writers would collide at common roots, and shared repositories/mounts
or long-lived agents would block normal workspace work. Only the actual mutable
anchor/member/SBX bundle retains exclusive mutation admission. Reference
admission uses the existing transient deletion namespace; no persisted format,
new configuration or alternate activation path is introduced.

Standalone mount planning previously created configured directories while
assembling mount arguments. It is now read-only. Before any directory creation
or SBX provisioning, creation admits the full planned mount set, including a
linked worktree's external Git common directory, and retains it through readiness
and final switching. Read-only mount suffixes remain SBX arguments, not literal
directory names or different reference identities. Existing env-file validation,
native ownership, rollback and recoverable-runtime rules remain unchanged.

Tests pause the actual cleanup `Service` before trying canonical-note,
primary-repository and additional/effective-mount references through physical and
alias paths. Both registered entries reject conflicts before effects. Reverse
admission covers current/desired references, lexical and physical ancestors,
missing paths, partial conflicts/cancellation, shared reference/agent
compatibility and unrelated deletion. Separate standalone regressions reproduce
actual pre-admission mount/provisioning effects at the previous baseline, then
verify rejection and retained references through runtime success/error/cancel.
Fixture providers do not delete data. These are prerequisite admission proofs,
not successful missing-runtime overlap under cleanup's note transaction.

At this boundary, note filesystem writers still needed qualification before
recursive anchor deletion could leave the shared note transaction. The following
increment admits ordinary path effects and qualifies existing state ownership;
neither increment narrows that transaction or changes the blanket UI gate.

### Ordinary note-path admission increment

Direct authored note creation does not take the note mutex. It must exclude
cleanup even before any broader transaction is shortened. Creation now retains
shared reference/ancestor admission from durable identity publication through
private-directory and content creation. Workspace authors' existing shared
reference leases remain compatible. A stopped creation retains its durable
association so a retry uses the same identity rather than overwriting content.

Archive and restore admit both source and destination before preparing
directories, journaling the no-replace move and committing its association.
Trash admits its source, canonical note and trash root before preparation, then
its unique destination before publishing a move intent. Reassociation admits
old and new paths before changing authority. Existing note transactions,
retained-workspace-reference checks, no-overwrite behavior, attachment/relative
link refusal and recoverable move intents remain intact. Done still commits
before archival: a resource conflict leaves a completed note in place and
returns the archival error, not a claim that completion was rolled back.

Configuration validation previously always called `MkdirAll(Tasks)`, including
from collection/status. Existing roots now have a read-only fast path; missing
root preparation requires matching admission. This is not a claim that all
setup or external configuration changes were note-serialized. Pure task
metadata updates do not acquire unnecessary filesystem writer leases.

Same-state offline migration already takes the daemon's exclusive state lock.
The daemon retains that lock and finishes startup migration/move recovery before
initializing or exposing cleanup. CLI regressions exercise apply, resume and
rollback rejection under that owner, alongside the actual daemon startup gate.
There is no new permanent recovery lock or on-disk format. Independently
configured state/socket namespaces and external filesystem mutations are not
covered by this same-state guarantee.

Nondeleting cleanup-Service fixtures qualify physical/alias source, destination,
missing-root and association conflicts before effects, honest partial lifecycle
state, no leaked move intents/admission, nested shared admission, cancellation
and durable creation retry. Unrelated cleanup permits note operations; existing
root reads remain usable without writer admission. Only direct creation is
qualified while the fixture holds the real note transaction. Other lifecycle
cases qualify the prerequisite resource boundary, not a claim that existing
note-serialized operations bypassed that mutex. Actual missing-runtime overlap
and selective UI admission still require the cleanup metadata/resource split.

### Qualification result and limits

A review-only overlay probe at `8ac0011` runs the actual cleanup `Service` with
an unrelated exclusive path lease. Its fixture provider pauses while holding
both the real process note mutex and filesystem flock. For both
`OpenRegisteredWorkspace` and `Create(existing)`, the requirement that unrelated
missing-runtime recovery finish before cleanup resumes fails. Both recoveries
complete after the note transaction is released. This is an expected-red
qualification probe, not a passing product acceptance test, throughput
measurement, timeout root-cause diagnosis or production incident.

The probe performs no deletion and uses fake runtime commands. It establishes
this lock-scope dependency, not successful native removal or real SBX recovery.
Existing live-navigation overlap, canonical/alias conflicts, native identity
handoff and sandbox admission tests remain separate evidence. A raw unrelated
lease acquisition or a fake cleanup provider without the note lock does not
qualify the missing-runtime case above.

### Recommended implementation boundary

Keep the global note/registry transaction for identity and relocation decisions;
move slow provider work outside it **only after the affected writers share
matching resource exclusion**. Do not introduce a second activation API or a
compatibility path to bypass the transaction.

1. Establish canonical per-target mutation ownership for every writer whose
   state a recovering operation relies on. Cover reconciliation, membership,
   note association, recreation and final cleanup, not only activation versus
   cleanup. Retain existing native/SBX admission through the outer operation.
   Admission must precede effects and must release the whole acquired set on
   any conflict, error or cancellation.
2. Separate short metadata validation/commit phases from provisioning,
   readiness, setup and potentially long filesystem/provider work. Reload and
   validate the selected registration at phase boundaries. An obsolete preview,
   note link or runtime identity must never become permission for an effect.
   New creation and existing-registration recovery need separate proof.
3. Review cleanup's corresponding long note-held phase before narrowing it.
   Keep the note referenced until workspace removal actually succeeds, retain
   rooted deletion/protected-path checks and fresh sandbox absence validation,
   and preserve correct partial-failure results. Do not publish final-reference
   removal or archive a note while live workspace state still depends on it.
4. Qualify actual paused cleanup against unrelated missing-runtime recovery
   through both registered entries, with sandbox-less and sandbox-backed
   preparation, errors, cancellation and reversed completion. Keep same-target,
   physical aliases, shared members, native incarnations and sandbox names
   protected. Exercise competing note relocation, reconciliation and registry
   drift under races, not merely task-ID inequality. Distinguish a short active
   metadata transaction from long resource work; neither may be silently
   declared independent without a test proving its invariants.
5. Only then selectively relax UI admission. The dashboard already has separate
   cleanup/activation slots, request IDs, phase/stale-result rejection and
   reversed-completion tests in `operation_overlap_test.go`. Preserve and
   integrate those with genuine user-driven overlap and per-operation error
   routing instead of reimplementing their existing state model.

No note/registry lock removal or identity change, persisted-format change,
new runtime worker or UI unlock is implemented here. Workspace-side mutation
admission is implemented; reviewed note-lifecycle coverage, shorter metadata
transactions, actual missing-runtime overlap and UI acceptance remain open.
