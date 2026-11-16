# Pi integration

[Documentation](../README.md) · [Workspaces](../workspaces.md)

## Install

First-run setup requires and offers to install `pi-radar` in Pi's user configuration (Pi 0.85.1 or newer, Node.js 24+). For sandboxed workspaces, also install `pi-sbx`. To install the packages yourself:

```sh
pi install npm:@christianmoesl/pi-radar
# Only for sandboxed workspaces:
pi install npm:@christianmoesl/pi-sbx
```

Pi runs on the host even for sandboxed workspaces. Install packages in that host
Pi profile; for a custom profile, prefix commands with
`PI_CODING_AGENT_DIR=/path/to/profile`. Restart Pi after installation.

`pi-radar` provides Radar workspace tools and context; `pi-sbx` provides sandbox tool routing. **`pi-sbx` >=0.7.0 is required for early sandboxed launch.** Installing the SBX CLI alone does not install either Pi package.

## Package scope and switching sources

The `pi-radar` npm package contains only the Pi extension; it does not install the Radar CLI, and extension users do not need pnpm. If switching from a Git installation, first remove its source with `pi remove git:github.com/ChristianMoesl/radar` (use the exact source from `pi list` if it is pinned to a tag). Then install the npm package and restart Pi. Git and npm sources have different package identities, so keeping both can load the extension twice.

## Starting Pi in a workspace

Keep the `radar` binary on PATH. The installed package checks Radar's registry at Pi startup and activates only inside a registered workspace anchor or one of its members. Outside those workspaces it adds no Radar tools, commands, instructions, skills, or activity reporting. A missing or failing Radar binary leaves the extension inactive; run `radar workspace-context --registration-only --json` to diagnose discovery, then restart Pi or use `/reload`.

You can start Pi yourself in a new tmux window; Radar does not need to launch it:

```sh
cd /path/to/radar/workspace
pi       # Start a new conversation with Radar integration
pi -c    # Continue the latest conversation for this directory
```

Use the workspace anchor to share its conversation history; member directories have their own Pi session history. Manual launches use normal Pi model and session defaults. Radar's own launcher still supplies its explicit model, thinking, name, session ID, fork and initial-prompt arguments when applicable.

Sandbox routing remains entirely owned by the separately installed `pi-sbx` extension. `pi-radar` neither selects sandboxes nor overrides Pi's shell or filesystem tools.

## Remote service connections

Use configured MCP tools in the host Pi session for Jira, Datadog, Terraform and
GitHub service requests. Workspace files, local Git, builds and tests keep their
normal local/SBX routing. Install and authenticate the chosen MCP client extension
in that host Pi profile; `pi-radar` provides workspace integration, not the service
client. It does not transfer credentials into SBX. Radar's collector CLI sessions
and the durable-agent MCP client have separate authentication.

The default Radar agent instructions describe this boundary. Existing user
`AGENTS.md` files are never overwritten; add this guidance manually when upgrading.
See [host MCP setup](../agent-mcp.md) for official server endpoints and the
durable-agent connection API.

## Ask Pi about Radar

In a registered workspace, ask questions such as “What can Radar do?” or
“How do I configure my workspaces?”. Pi uses the read-only
`radar_documentation` tool to retrieve an index or a specific Markdown topic
from the **installed host CLI**. No network, man-page reader, source checkout,
extra sandbox mounts, configuration file or credentials are needed.

The tool returns the CLI version/commit and canonical documentation source paths.
Pi should consult relevant topics, cite their sources and distinguish documented
defaults from your actual configuration. Relative Markdown links resolve against
the returned source path; request the resulting source path without its anchor.
The loaded Pi extension can have a different version: these docs describe the
CLI, not a promise that every extension capability is already loaded. Update the
CLI if the documentation command is unavailable; reload Pi after extension updates.
Documentation access does not authorize configuration changes. Outside a
registered workspace the extension remains inactive; use `man radar` or
`man radar-config` for the terminal reference.

## Feedback diagnostics

In an [Improve Radar workspace](../workspaces.md#improve-radar), `pi-radar`
restores feedback-specific guidance on every turn. The exact `linear` MCP server
must expose a reachable issue creation tool in that session; configured-but-not-
connected servers and hidden tools do not qualify. Direct, codemode and deferred
exposure are supported when the corresponding calling mechanism is available.
Pi owns MCP connections, authentication and tool execution; no MCP configuration
or credentials are copied to a feedback workspace.

The restored guidance leads from the initial description to a suggested bug/feature
classification, targeted follow-ups and an explicit readiness announcement. Bug
reports do not require a proven root cause; feature requests develop scope,
acceptance criteria and a grounded plan. Existing conversation and `notes.md`
prevent repeated classification/checklists, and assumptions remain separate from
confirmed decisions. The assistant searches related issues, presents the exact
title/body/destination and proceeds when the user clearly requests submission,
without an Improve Radar-specific MCP write dialog or mandatory “are you sure?”
question. Unclear intent still needs targeted clarification; readiness alone is
not permission to create an issue. This applies to direct, codemode and deferred
calls, without changing Pi/MCP authentication, tool access or approval behavior
outside feedback. Lost MCP availability still blocks feedback calls, and actual
host-command and private-log approvals remain unchanged. Only confirmed creation
yields an identifier/link for the user and `notes.md`; filing does not implement
the request or close the workspace.

`radar_diagnostics` is a typed host tool available only in enabled feedback
sessions. The current delivery is **observation only**: the overview advertises
its capabilities explicitly. Explicit registered-runtime observations require
fresh interactive approval. Full provider cleanup eligibility, operation history
and maintenance adapters are not yet exposed; unsupported
requests fail instead of running arbitrary commands or changing state.

The default `overview` returns the installed CLI version, loaded extension
version, cached daemon/provider status and a typed `settings` policy projection.
`feedback_sandbox_registered` separately describes the feedback registration;
it is not runtime presence or an effective sandbox-permission check.

### Configuration policy and origins

Overview `settings` are **CLI-process global defaults**, not the running daemon's
environment, repository/workspace-effective configuration, current provider access
or execution permission. The native defaults, YAML decoder and validation are
shared with production. Values and declarations come from the same bounded read
of the configured `config.yaml`; no second file is used to guess origins.

The finite ten-setting allowlist contains:
- `workspace.auto_confirm`: `true`/`false`.
- `sbx.enabled`, `github.enabled`, `jira.enabled`, `datadog.enabled`,
  `terraform.enabled`: `automatic`, `required` or `disabled`. `required` expresses
  configuration intent, not usable tools/authentication or an enabled runtime.
- `workspace.cleanup.disposable_entries` and `sbx.additional_mounts`: counts only,
  not names, paths, accepted deletion targets or permission to delete anything.
- `sbx.env_file` and `sbx.ready_command`: configured presence only; paths,
  command/argument values and referenced file contents are never read or returned.

Each setting identifies `origin` (`compiled_default` or `configuration_file`) and
`declaration` (`absent`, `null` or `value`). Explicit values equal to defaults still
have file provenance; native null/default behavior, aliases and YAML merges are
preserved. `location_origin` identifies only `xdg_config_home` or `user_home`, never
the path or an environment dump. Model/thinking strings and arbitrary old settings
maps are no longer returned. The revision hashes only these allowlisted values
and declarations, not private configuration bytes. `captured_at` is read assembly
time, not evidence that a daemon loaded this configuration.

At most 64 KiB is read from the fixed native configuration file. Leaf symlinks
(including legitimate user-managed configuration symlinks), FIFOs, nonregular,
oversized, invalid, replaced or incomplete reads are unavailable, not valid
policy or authorization. An actually absent file yields compiled defaults without
creating it. Raw parser/validation errors are withheld. A prior workspace
registration lookup still uses its native configuration-loading rules; this
bounded projection does not change normal configuration or startup behavior.
Provider credentials, secret/environment files, tools, network access, repository
configuration, daemon environment and maintenance are never inspected or invoked
by this projection. No persisted configuration or policy was changed.

The originating workspace is projected from its registration only: it does not
run SBX, tmux, Git, remote probes or workspace startup. A registered resource is
not proof that it exists or is running. Missing daemon/origin evidence is
unavailable, not healthy, empty, repaired or enrolled.

### Cached integration evidence

Use `section: "integrations"` for the complete paginated union of registered
integration capabilities, cached installation-wide provider statuses and retained
reference providers (including no-longer-registered ones). `id` selects an opaque
stable integration ID. Installation scope includes
all these rows; origin/feedback scopes select only providers with directly related
retained references, including inactive and dashboard-hidden records. There is
no inference that an unrelated provider is unused, healthy or unavailable for a
workspace. Parent and task-state selectors are rejected rather than ignored.

Capabilities are static registered interfaces, not effective configuration,
tool availability, authorization or permission to execute an operation. Cached
statuses are explicitly installation-wide even in workspace scope; status alone
is not workspace health. Only `ok`, `partial`, `error` and `disabled` are projected;
missing or unrecognized evidence stays `unknown`. Duplicate registration/status
identities have explicit ambiguity evidence and do not invent capabilities or
health. Raw provider errors/details are withheld behind `details_available`.
Installation-reported observation counts are omitted in workspace scope. Native
zero/missing counts are also omitted: the persisted status model does not
distinguish them (including skipped collection), so they cannot prove none.

Active/inactive reference counts and latest valid retained-reference observation
timestamps describe only the selected scope. Invalid/missing timestamps are
counted as unknown. Such timestamps never establish the last collection attempt,
last successful collection, provider cache freshness or authentication. Those
histories are not recorded in the existing native status model and remain
explicitly unknown; no new telemetry or persisted schema is introduced.

Registration and cached status/reference snapshots are projected without calling
provider `Status`, `Collect`, authentication, rate-limit or runtime methods,
looking up/running tools, reading provider credentials/configuration or refreshing any
provider. Native references and statuses share one atomic cache revision; cursors
also bind selected scope, registration/capability changes and pending publication.
IDs hash full native provider identity before clipping display text. CLI, daemon
and extension must all support schema 12; older versions fail closed.

### Explicit registered-runtime observations

Use `section: "runtime_probe", parent_id: "<registered workspace ID>"` only for
requested live runtime evidence. The usual origin (default), feedback and
installation scopes still apply; even installation scope requires one exact
registered workspace. Task-state filters are rejected. Every request/page needs
fresh interactive confirmation naming its parent, scope and optional runtime ID.
Headless, declined or aborted confirmation never starts a probe. The explicit
CLI invocation is `radar feedback-diagnostics --workspace <feedback workspace>
--section runtime_probe --parent-id <registered workspace ID> --json`; the normal
cached diagnostics RPC rejects probes, with no older-daemon fallback.

The provider-owned adapters run only native `sbx ls --json` and
`tmux list-sessions` (including native SBX/WSL executable selection, environment
and listing path normalization). One request has a shared five-second deadline;
stdout is capped at 20 MiB per command, stderr/provider errors are withheld, and
cancellation terminates command process groups. No collection, pane content,
startup, login, Git/network refresh, provider cleanup preview, ownership/lease
acquisition or maintenance runs. A runtime CLI may contact its own backend;
this is not a cached or offline observation.

Each item reports registered name, provider, bounded presence/state, opaque
runtime-lifetime ID and comparison against active cached native identities for
that exact workspace/name. Full native identities are compared before hashing
or clipping; reuse of a name with a different lifetime is explicit. A complete
identifiable listing may establish absence; malformed, ambiguous, unsupported,
failed or timed-out evidence is **unknown**, never absence. Shared registered
names are not probed. Changed registration/cache evidence rejects the result;
changed observations invalidate pagination cursors, and every page repeats the
bounded listing. The observation timestamp is not a guarantee of future state.

Runtime matches, absence or mismatches establish neither registration ownership,
lease safety, orphan absence nor cleanup eligibility/consent. Unrelated listing
rows, native lifetime IDs, mounts, environment, pane contents and raw errors are
not returned. Unregistered runtimes are not classified. Cached inventories never
start these probes implicitly; use existing retained references for cached
history, not as proof of liveness.

### Retained inventories (diagnostics schema 12)

Use `section: "tasks"` or `"workspaces"` to inspect records omitted from the
dashboard. `scope` is `"origin"` by default, `"feedback"` for the current feedback
workspace, or `"installation"` for explicitly requested installation-wide
inspection. The response identifies the feedback and origin independently.
An absent/unregistered origin never falls back to installation-wide access.

Inventories support `id` (stable record/workspace ID, or numeric task ID), `state`
(exact task state), `limit` (1–50, default 20), and `cursor`. For example:

```json
{"section":"workspaces","scope":"installation","state":"done","limit":20}
```

The equivalent fixed facade is:

```sh
radar feedback-diagnostics --workspace /registered/feedback --section workspaces --scope installation --state done --limit 20 --json
```

Follow `inventory.next_cursor` with the same selectors until
`pagination_complete` is true; `total` covers all matching top-level records.
A changed daemon revision, registration projection or refresh-pending state
invalidates the cursor. Restart at the first page when
`reason: "snapshot_changed_restart_pagination"` is returned. No long-lived
snapshot is persisted. Items are sorted by stable ID, pages are capped at
256 KiB, nested lists at 16 entries, and text fields at 512 bytes. Inspect each
item's `truncated` and the page's `limitations`; complete pagination is not a
claim of complete nested evidence or current runtime health.

### Identity and nested drill-down

Use `section: "source_refs", parent_id: "<task record or numeric ID>"` to page
all retained source refs rather than stopping at the task summary's 16 entries.
Each ref has an opaque stable `id` derived from the complete retained identity
(never a clipped path). It distinguishes that ID from the bounded `provider_id`, identifies its
owning record/numeric task, and keeps inactive history explicitly marked.
Omit `parent_id` for the scoped source-ref inventory, or select an exact retained
ref with `id`. A scoped unowned ref is matched only against itself, never an
unrelated unowned record. `state` still filters the owning task lifecycle.

Use `section: "bindings", parent_id: "<retained source-ref ID>"` to page authored
binding intent. Entries expose provider/kind/target identity, concrete-lifetime
key and work-item intent, never arbitrary metadata or raw provider errors.
Stable binding IDs hash the owning retained ref and the binding's identity;
identical repeated entries have one ID with an `occurrences` count. Neither a
binding nor its presence proves that the remote entity currently exists.
`parent_id` on unrelated sections is rejected, not ignored.

Use `section: "workspace_resources", parent_id: "<registered workspace ID>"`
to page every registered member, mount, port and retained task/registered-agent
owner beyond a workspace summary's 16 entries. Omit `parent_id` for the scoped
inventory or use `id` to select an exact resource relationship. `state` is
rejected for this section, not applied to only some resource kinds. All three
origin/feedback/installation scopes retain the same registered boundaries.

Each `workspace_resource` identifies its workspace, `kind`, evidence and one
allowlisted `member`, `mount`, `port`, `task` or `agent_id`. `task_owner` includes
the recorded state/DoneAt/retention gate and a task `record_id` for further task
drill-down. Stable IDs hash full relationship identities before text clipping;
identical relationships share an ID with an `occurrences` count. Changing a
task's state/retention does not change its relationship ID. Summary
`member_count`, `mount_count`, `port_count`, `task_count` and `agent_owner_count`
cover full lists (including duplicate occurrences), not just shown entries.
Task counts respect a workspace inventory's `state` filter.

Agent ownership uses the exact production removal-owner lookup, including
stopped registrations and backing workspaces inside a group. On lookup failure,
`agent_owner_count` is omitted, summary ownership is `unavailable`, and page
evidence explicitly ends in `agent_ownership_unavailable`; zero returned owner
rows then must not be read as no owners. Members/mounts/ports/agent owners are
fresh registration evidence; task ownership includes cached inactive history.
Neither is a runtime probe or full cleanup eligibility. No commands, secret-file
contents, environment values, note bodies or conversation/event payloads are
read or exposed.

Task `authored_identity_count`, `linking_component_records` and `linking_blocked`
reuse production's active connected components and the exact authored-identity
merge guard. Inactive authored history still protects its numeric identity. A
blocked bridge between distinct authored notes is explained, never merged or
repaired by inspection. Counts reveal related ownership without leaking other
tasks' note contents. A zero component count means no current authoritative
linking evidence, not proven absence of external links.

### Lifecycle explanations

Task `state`, `done_at` and `review` remain recorded evidence. The separate
`lifecycle` object evaluates the **same production transition** on all retained
active authoritative inputs, including entries beyond the summary limit. It
never applies that transition or reconstructs historical manual intent.
`source_state` is the prospective `active`/`done` result, or `unchanged` when
there are no selected work-item inputs; it can differ from recorded state.

The bounded `decision` is `no_work_item_inputs`, `work_item_not_done`,
`activity_or_review_pending`, `durable_review_pending` or
`source_completion_ready`. Counts report selected/done work items, inputs
using cached task attention as their signal fallback, authoritative refs and
activity/review holds. Primary work items take precedence over secondary work
items, exactly as in production. `recorded_review_pending` is the stored hold;
`next_review_pending` includes production's authored-completion/review handoff.
With no work-item inputs, production preserves state and the durable hold;
zero evaluated holds then means no transition was evaluated, not no activity.

Evidence is `production_lifecycle_evaluation_on_cached_inputs`, not a live
provider refresh, provenance of a manual change, cleanup eligibility or user
consent. Free-form reasons, source status text and metadata are withheld.
`source_completion_ready` never updates state or starts maintenance.

### Cleanup-retention and cached GC-plan explanations

Task views and each workspace retention entry include `gc_retention`, using
exactly the production GC gate and its active-ref selection. Its decision is
`task_not_done`, `completion_time_missing_or_invalid`, `retention_pending`,
`agent_work_or_review_pending` or `retention_gate_passed`. `ready_at` is the
normal retention boundary, not a deletion deadline or authorization. The
registered workspace's separate `expiry_at` still uses the existing expiry
policy; inspection changes neither deadline.

Evidence is explicitly `production_gc_retention_gate_only`. A passing gate
never establishes cleanup execution eligibility: location, registered-agent
ownership, provider safety, leases, drift and confirmation checks still apply.
A stopped registered owner can block execution after retention has passed.
Activity/review checks use all production-selected active refs, not just the
16 shown in a task summary. No provider preview, runtime probe, cleanup planner
or executor is run to obtain this evidence.

Use `section: "gc_plan", parent_id: "<task record or numeric ID>"` for the
complete production candidate/skip plan beyond that first gate. Omit `parent_id`
for all scoped retained tasks, including UI-hidden work, or use `id` for an exact
opaque GC-plan entry. `state`, `limit` and `cursor` work as for other inventories.
Scope selects the owning task and its complete related resource plan, including
resources related to that task outside the matching workspace. It does not grant
mutation authority. No task title, free-form reason, provider error, command or
executable operation map is returned.

The plan uses one immutable retained-state snapshot, the workspace manager's
production configured root, and the **same pure planner used by automatic GC**.
It retains active-ref selection, anchor-first registered-workspace deduplication,
normal retention/activity/review gates and lexical root exclusions. A row is a
`candidate`, `outside_workspace_root`, a retention-gate decision, or
`no_workspace_candidates` when the passing record has no planner candidates.
Inactive history may select an owning task's scope but does not create a current
candidate. Duplicate standalone candidate identities retain an `occurrences`
count. Unknown/unavailable workspace configuration fails explicitly, not as an
apparently complete empty plan.

`next_stage` reuses production's `safe_cleanup` (standalone),
`runtime_retirement` (registered workspace before expiry), or `expired_cleanup`
choice. Registered `expiry_at` is the existing production deadline, not consent
or proof of data presence. Candidate IDs hash full record/resource identity
before text clipping; decision, age and next-stage changes preserve identity
while invalidating pagination snapshots. A changed resource-refresh-pending bit
also invalidates cursors; pending state remains visible, not a refresh request.

Evidence is `production_gc_plan_on_cached_inputs`. `execution_eligibility`
remains `unknown_provider_ownership_lease_checks_not_run`, even for a candidate:
provider preview/preflight, actual resource presence, agent ownership, leases,
fresh drift checks and user consent are **not established**. This is not an
executable cleanup preview, a full list of provider deletion targets or evidence
of a completed cleanup/retirement. It performs no filesystem existence checks,
runtime/network probes, provider refreshes, lease acquisitions or mutations.
Explicit provider previews and apply adapters remain separate future operations.

### Durable-agent registration and checkpoint evidence

Use `section: "agents"` with the same origin/feedback/installation scopes and
revision-bound pagination, or select an exact agent registration with `id`.
The scope uses the production workspace-ownership relationship, including agents
whose backing workspace is inside the selected workspace. `state` is a retained
task-lifecycle filter, not a runner-state filter; it and `parent_id` are rejected
for this section rather than silently ignored.

Each item distinguishes registered desired state from observed runner state.
An intentionally stopped registration still owns and protects its workspace;
absence from a scoped inventory is not permission to delete resources. Reads
never contact a runner socket, inspect/start a runtime, acquire a lease or replay
an outbox. `runner_evidence` and `lease_evidence` remain explicitly unknown.

A compatible persisted runner snapshot provides only definition/presentation
revision, pending count, attention and a problem flag. `snapshot_saved_at` is the
snapshot file's persistence time, **not** a fresh runner observation or proof of
liveness. A missing, invalid, oversized or unsupported snapshot is unavailable
evidence, not zero work or a healthy/stopped runner. Snapshot reads are limited
to 1 MiB and reject symlinks and non-regular files.

Production-owned collection/schedule readers expose pending counts, delivery
problem flags, configured calendar metadata, derived next due times and bounded
admission timestamps/task IDs. Admission is not execution or business success.
Schedule due calculations reuse production scheduling; cached checkpoint errors
do not reconstruct in-memory supervision errors. At most 16 schedule summaries
are returned per agent with the full `schedule_count` and explicit `truncated`;
follow `section: "agent_schedules", parent_id: "<agent registration ID>"` to
page all persisted schedules with the same scope and selectors. Omit `parent_id`
for the scoped schedule inventory, or use `id` for an exact opaque schedule ID.
Native bounded schedule IDs are separate from selectable opaque IDs, which hash
the complete agent/native schedule identity before text clipping. State and due
time changes do not change that identity. Task `state` filters are rejected.

Schedule inventories and summaries share one projection, production checkpoint
validation and production due calculation. Diagnostic schedule reads are capped
at 1 MiB, no-follow/nonblocking and regular-file checked. Missing schedule files
mean no configured persisted schedules, exactly as in production, not a stopped
or healthy runner. Invalid/oversized/unsafe checkpoints make summaries unavailable
with no schedule count; the schedule inventory fails explicitly, never returning
an apparently complete empty/partial list. Scope and parent filters are applied
before checkpoint reads, so an unrelated unavailable checkpoint can be excluded.

Each `agent_schedule` identifies its owning agent and allowlisted calendar,
checkpoint/admission metadata and delivery-problem flag. Evidence is
`fresh_schedule_checkpoints_runtime_errors_unknown`: in-memory supervision errors,
runner liveness, leases, execution and business outcomes remain unknown. Private
event/payload bodies, admission event IDs and free-form provider errors stay
withheld. Reads never tick a schedule, admit/replay events or write checkpoints.
Changes in shown checkpoint evidence, including beyond summary truncation,
invalidate cursors; complete pagination proves only this scoped projection.
Definition commands, tokens, raw errors, titles, conversation references, task
and event bodies, action payloads and schedule payloads are never returned.

Task views include retained lifecycle state, `DoneAt`, source observation times,
active/inactive refs, ownership identifiers, acknowledgement/review/activity
facts and binding/problem counts. `lifecycle_input` uses the exact production
selection of active authoritative work items (including primary precedence),
not a second lifecycle policy. Free-form titles/reasons and private payloads are withheld; authored binding
identities are available only through the bounded `bindings` inventory; historical manual intent is not reconstructed.

Workspace views include registration paths/members/source repositories, note
association (never its contents), desired SBX/tmux names, mounts/ports/shared
directory, cached task history, completion-based expiry from production
`workspacegc.ExpiryAt`, and fresh durable-agent ownership registrations. A
stopped agent registration still blocks removal; unavailable owner inventory
is **unknown**, never no owners. Runtime evidence remains `unknown_not_probed`,
and without a cleanup planner result the tool cannot assert eligibility or
propose safe deletion. Historical task ownership does not establish a current
owner. Canonical notes, primary repositories and mount targets are not deletion
recommendations.

`observed_at` is the diagnostic assembly time, not the collection time of cached
facts. `revision`, `snapshot`, `evidence`, source `observed_at`,
`resource_refresh_pending` and `limitations` distinguish fresh registration
reads from cached observations. Reads never start the daemon, refresh providers,
export raw state, read note/conversation contents or serialize credentials,
environment files, startup commands or full configuration. CLI, daemon and
extension must support diagnostics schema 12; version mismatches fail closed
without a legacy command/state-export fallback. Update the CLI and the actual
Pi extension source together, then reload Pi; `make install` alone does not
update a separately installed npm extension.

The `logs` section requires **fresh interactive approval for every exact query/page**.
It scans at most the latest 1 MiB of the single configured installation-wide native
Tint daemon log, and returns typed redacted metadata only: at most 100 entries
(default 20) and 32 KiB per page. It no longer returns a raw log tail. Free-form
messages/errors, arbitrary attributes, source locations, paths, credentials and
raw identities are withheld; unknown messages remain unclassified rather than
being interpreted as success or failure. No log format, collection policy,
persisted schema or telemetry/history service changes.

Use `since`/`until` as inclusive `YYYY-MM-DD HH:MM:SS` **native daemon wall times**,
not UTC/RFC3339 instants: native logs have second precision and no timezone.
No historical timezone or DST disambiguation is inferred. `severity` is an exact
`debug`, `info`, `warn` or `error` match. `provider`, `entity` and `operation`
accept only opaque IDs from returned `identities`, never substrings/names/paths.
Entry IDs bind native file lifetime, absolute byte offset and full line, staying
stable across filters/appends while distinguishing rotated files. Identity IDs
hash the complete native attribute value under its field name: they do not
prove cross-field identity, ownership or operation correlation. Only attributes
following known static native event messages are interpreted; apparent fields
inside free-form messages/errors are never searched. Missing correlation and
unrecognized formats remain unknown.

Pass `limit` (1–100) and `cursor=logs.next_cursor` to page the same bounded window.
Each page repeats the bounded read and fresh query-specific approval. Changed
file contents/size/mtime, filters or page size reject the cursor; restart rather
than combining snapshots. Drift returns `available: false`,
`reason: "snapshot_changed_restart_pagination"`, without an empty log page.
`pagination_complete` only covers matching metadata
inside that window, **not all historical operations**. `window_truncated`,
`partial_tail_withheld`, `unparsed_lines`, `query_unknown_lines`, scanned bounds
and limitations disclose missing evidence. Unlinked identity-filter rows are
conservatively counted as unknown, even if other identities are present.
First partial records and unfinished
last lines are withheld; symlinks, FIFOs and nonregular files fail closed.
Rotated files are not followed. A zero-match result is not absence or health.

Allowlisted phase durations (including lock waits) describe a completed timing
measurement, **never successful business execution**. Every row explicitly marks
outcome `unknown_not_recorded`; even a reported deletion/failure is only a log
event, not independently verified resource state or a complete target outcome.
Failed/remaining targets, retries, live locks and full operation outcomes remain
unknown unless separately observed. Log approval permits local inspection, not
publication or automatic Linear attachment; review proposed evidence separately.
Denial, missing UI, cancellation or MCP loss make no log read. Log filters are
rejected outside `logs`; inventory scope/parent/task-state selectors are rejected
for logs, which remain explicitly installation-wide. The tool accepts no arbitrary
path, executable, SQL or shell input. Source reads and experiments stay inside the
normal workspace boundary; no live state/config/socket mounts are needed.

The dashboard learns MCP creation availability through the existing per-session
loaded-version reports, refreshed every five seconds and removed on shutdown.
Reports contain availability metadata, not MCP configuration, tokens or tool
arguments. The dashboard ignores stale/dead-process reports and other Pi profiles.
The session revalidates against its live tool registry before diagnostics or
Linear writes; missing MCP availability never enables a direct API fallback.

## Approval-gated host tools

In registered workspaces, `radar_clone_repository` clones missing GitHub source checkouts under the host `repository_dirs` returned by workspace context. It always confirms the exact initial clone, independently of `workspace.auto_confirm`. Verified matching checkouts are reused without mutation or approval. After cloning, use normal repository refs and workspace reconciliation; no worktree attachment or setup happens implicitly.

`radar_open_resource` accepts a nonempty `resource_ids` batch (maximum 16) from `radar_workspace_context.openable_resources`. GitHub advertises PR overview, Changes and repository-home targets. **Every opening batch requires a new explicit confirmation** showing all generated URLs. Auto-confirm, a prior batch's approval, or an explicit request in the conversation does not bypass this prompt.

Both tools preview through fixed host Radar commands, then apply the accepted plan. They fail closed on unavailable UI, denial, cancellation, invalid responses, or plan drift; there is no generic shell/browser fallback. Headless sessions can inspect/reuse existing repositories but cannot approve a new clone or opening batch. RPC clients need to support Pi's confirmation dialog. Failed/partial results must be inspected rather than blindly retried.

The tools do not accept executables, cwd/environment overrides, arbitrary URL input, or approval flags. Clone traffic and host-browser traffic intentionally occur outside sandbox network enforcement. Browser results contain launch status only, not remote content. See [Git cloning](git.md#host-source-cloning) and [GitHub opening](github.md#provider-owned-browser-opening).

These tools require matching CLI and extension updates. `make install` updates the CLI, not a separately installed npm extension; reload Pi only after updating the extension source you actually use.

## Workspace attachment notice

Each interactive Pi session that starts or resumes inside a registered Radar
workspace prints one short line, for example:

> Radar workspace attached. Press Ctrl+B, then r to open the Radar task dashboard.

The shortcut uses the current tmux session's actual prefix, and appears only when
its prefix+r binding opens Radar's popup. Without a usable popup binding (or
outside tmux), the line suggests running `radar` in another terminal instead.
Reloads, resource refreshes, and headless/RPC sessions do not repeat the notice;
outside registered Radar workspaces the extension remains inactive.

There is no introduction command, prompt, model call, or saved dismissal state.
Old introduction markers are unused and left untouched; no cleanup or migration
is required. Updating `pi-radar` supplies this change; `make install` updates the
CLI, not a separately installed npm extension.

## Upgrading from the injected extension

Update the Radar binary and install this package together, then restart existing Pi processes. Radar no longer materializes or passes `--extension` for its integration. Remove any manually configured reference to the old `$XDG_DATA_HOME/radar/pi/radar.ts` (normally `~/.local/share/radar/pi/radar.ts`) so only the installed package loads. The old file is unused and can be removed after old sessions have stopped. No workspace registry or conversation migration is needed.

## Package updates

Update unpinned Pi packages in the same host Pi profile:

```sh
pi update npm:@christianmoesl/pi-radar
pi update npm:@christianmoesl/pi-sbx
```

Restart Pi afterwards. `pi update` without a package source updates Pi itself, not these packages. To pin either extension, use `pi install npm:@christianmoesl/pi-radar@<version>` or `pi install npm:@christianmoesl/pi-sbx@<version>`; choose `pi-sbx` >=0.7.0 for early sandboxed launch. Versioned npm sources are pinned and skipped by package updates, so move to a new pinned release by installing its version explicitly. The Radar CLI and `pi-radar` npm package share the same release version (the npm version omits the tag's leading `v`); `pi-sbx` is versioned separately.

When a stable Radar release starts/restarts its daemon, it synchronizes both
installed, unpinned user-level npm packages automatically in the background,
without a separate extension confirmation. This also works after Homebrew
upgrades. Successful updates are remembered per Radar release and Pi profile;
normal restarts do not repeat them. Failures are logged (`radar logs`) and retried
on the next `radar restart`. Pins, filtered/disabled declarations and custom/project
installations remain untouched.

The profile is selected when the daemon starts: `PI_CODING_AGENT_DIR`, or
`~/.pi/agent` when unset. Use `PI_CODING_AGENT_DIR=/path/to/profile radar restart`
for a custom profile; other profiles are not scanned. Development/prerelease
Radar builds leave package updates manual. See [Pi coordination](../releases.md#pi-coordination).

In an interactive Radar workspace, `pi-radar` checks its locally installed
version every 30 seconds. If it is newer than the version loaded by the session,
a red statusbar notice reads **pi-radar updated · /reload to apply**. Otherwise
nothing is shown. A successful `/reload` clears the notice by loading the new
version. There is no corresponding notice for `pi-sbx`, no automatic reload, and
no idle/subagent detection; choose a safe moment to reload.

## Installation notices

Radar-launched interactive Pi sessions show one combined, non-blocking recommendation per Pi profile for missing packages. It explains each package's benefits and installation command; `/radar-dismiss-install-hint` hides it. The notice never installs packages or changes Pi settings. Install scripts provision required CLI tools; foreground onboarding installs Pi extensions after permission, while the daemon synchronizes eligible installed unpinned packages after a stable Radar release starts, without an extra extension confirmation. The notice checks the running Pi's Radar tools and personal/project declarations (including `PI_CODING_AGENT_DIR`), suppresses advice independently for each known configured or explicitly disabled package, and does not appear in RPC/print mode. Having `pi-radar` installed does not hide relevant missing `pi-sbx` advice. That advice requires an effectively sandbox-enabled workspace and a usable SBX CLI, established by bounded read-only probes; absent/disabled/uncertain sandboxing stays quiet. This helper gives installation advice, not version enforcement. Pi runs on the host even for sandboxed workspaces, so install both packages in that host Pi profile. For a custom agent directory, prefix each command with `PI_CODING_AGENT_DIR=/path/to/profile`; the suggested commands include the matching directory. Restart Pi after installation.

All launches load a small **notice-only helper**, not the integration; early sandboxed launches additionally load the required [pi-sbx prerequisite guard](sbx.md#early-pi-launch-and-recovery). Radar caches these helpers under `$XDG_CACHE_HOME/radar/pi/` (or the platform's user cache directory) and records that the notice was shown in `<Pi agent directory>/radar/install-hint-seen`. Removing that marker allows the recommendation to appear again. Existing settings and workspaces are not rewritten; unreadable settings or unavailable notice storage simply skip the advice.
