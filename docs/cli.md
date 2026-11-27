# Command-line reference

[Documentation](README.md) · [Dashboard shortcuts](dashboard.md#keyboard-shortcuts)

## Commands

```sh
radar                         # Dashboard
radar setup                   # Configure or reconfigure
radar setup notifications     # macOS launch/notification approval
radar update                  # Confirm a managed macOS release
radar version
radar documentation [--topic <source>] [--json]
radar feedback-diagnostics --workspace <feedback-workspace> --section overview|tasks|workspaces|source_refs|bindings|workspace_resources|agents|agent_schedules|gc_plan|runtime_probe|agent_probe|retirement_preview|cleanup_preview|integrations|operations|refresh|logs --json
radar feedback-diagnostics --workspace <feedback-workspace> --section cleanup_preview --parent-id <retained-task-id> --scope origin|feedback|installation [--action preview|apply] [--plan-id <exact-preview-id>] --json
radar feedback-diagnostics --workspace <feedback-workspace> --section cleanup_preview --parent-id <retained-task-id> --cleanup-mode expired --workspace-id <registered-workspace-id> --scope origin|feedback|installation --json
radar feedback-diagnostics --workspace <feedback-workspace> --section refresh --scope installation [--action preview|apply] [--plan-id <fresh-preview-id>] --json
radar feedback-diagnostics --workspace <feedback-workspace> --section logs [--since "YYYY-MM-DD HH:MM:SS"] [--until "YYYY-MM-DD HH:MM:SS"] [--severity debug|info|warn|error] [--provider <opaque-log-id>] [--entity <opaque-log-id>] [--operation <opaque-log-id>] [--limit 1-100] [--cursor <log-cursor>] --json
radar create                  # Interactive workspace creation
radar fork
radar task create --title <title>
radar task done <task-id>
radar task reopen <task-id>
radar task mute <task-id>
radar task unmute <task-id>
radar task delete <task-id>
radar task priority <task-id> low|normal|urgent
radar task title <task-id> <title>
radar task note <task-id> <absolute-note-path>
radar repair-task-links [--apply plan.json --backup /new/backup.db] [--json]
radar migrate-state [--apply|--resume|--rollback]
radar state-backup <destination.db>
radar state-export
radar status
radar tasks
radar reconcile-workspace --request <json> [--workspace <path>] [--preview]
radar recreate-sandboxes [--workspace <path>] [--preview] [--yes]
radar workspace-context [--workspace <path>] [--registration-only | --resources-only]
radar repository-refs --repo <repo>
radar clone-repository --request <json> [--workspace <path>] [--preview | --plan <id>]
radar open-resource --request <json> [--workspace <path>] [--preview | --plan <id>]
radar agent add <module> --id <id> [--workspace <registered-path>] [--config <json-file>] [--collection github] [--preview | --plan-id <id> --prepared-note <json>]
radar agent list
radar agent inspect|start|stop|restart|remove|logs|token <id>
radar agent schedule set <id> <name> --time HH:MM --weekdays mon,tue,wed,thu,fri --timezone <IANA-zone> --payload <json-file>
radar agent schedule pause|resume|remove <id> <name>
radar agent emit <id> --source <source> --event-id <id> # JSON payload on stdin
radar agent message <id> <text> --revision <revision> [--event-id <id>]
radar agent action <id> <action-id> [text] --revision <revision> [--event-id <id>]
radar agent conversation <id> <conversation-id>
radar agent serve [--listen 127.0.0.1:7433]
radar cleanup <task-id>
radar gc
radar refresh
radar reset
radar stop
radar restart
radar config-path
radar state-path
radar log-path
```

## Manual task priority

`radar task priority 7 low` defers the whole task to Low Priority without muting it,
acknowledging its sources, or changing its lifecycle. New actionable requests can
temporarily resurface it; addressing those requests returns it to Low Priority.
The retained preference survives refresh, restart and cache reset. Set `low` again
to defer currently outstanding requests, `normal` to restore the current
source-derived category, or `urgent` to select the existing urgent mode. Done
tasks cannot change priority; new activity never unmutes a task.

Source-only tasks reuse durable bound-note adoption without creating workspace
resources. Priority mutations themselves do not send OS notifications. In the
dashboard, `P` selects a priority, `p` keeps its urgent/normal shortcut, and Inspect
shows retained priority separately from the effective attention category. See
[attention policy](attention-algorithm.md#manual-low-priority).

Activity producers can use `radar activity --prompt-id <id> waiting` (repeat the
option for overlapping sessions). IDs identify actual prompt lifetimes: reuse
them while the same prompts remain open and omit them on `busy` or `idle`. Pi
publishes these automatically without prompt text. Waiting without an occurrence
ID remains an activity badge/completion veto, not evidence of a new Low request.

## Offline documentation

See the [durable-agent guide](durable-agents.md) for installation, automatic dedicated workspace/SBX provisioning, explicit bindings, execution, event receipts, retention and examples. Agent registrations and data survive `radar reset`. `agent start` and `agent restart` also accept `--preview` / `--plan-id` for managed resource recovery. A stopped registration still pins its workspace; `agent remove` releases the association and retains durable data.

Use **`man radar`** for this command reference and **`man radar-config`** for
configuration guidance. Both are generated from the same Markdown documentation
as the installed CLI's embedded manual. See [manual installation and discovery](installation.md#manual-pages).

The Pi extension reads the embedded Markdown through a non-interactive transport:

```sh
radar documentation --json
radar documentation --topic docs/configuration.md --json
```

Without a topic, the result contains `version`, `commit` and a `topics` list of
canonical `source` paths and `title` values. With a topic, it contains `version`,
`commit` and `document` (`source`, `title`, `content`). Only listed public docs
can be read; arbitrary host paths are rejected. Relative Markdown links resolve
against the source path; omit the `#anchor` when retrieving a document. Without
`--json`, the command prints the index or Markdown directly. It never starts a
daemon, prompts for setup, reads credentials or accesses the network.

## Approval-gated host resources

The two host operations require a registered workspace and strict JSON requests:

```sh
radar clone-repository --workspace /work/task --request '{"repository":"https://github.com/acme/app.git","destination":"/sources/app"}' --preview --json
radar open-resource --workspace /work/task --request '{"resource_ids":["github:open:pr:acme/app:123:changes"]}' --preview --json
```

Select a destination under `workspace-context.repository_dirs` and resource IDs from its `openable_resources`. Preview has no clone/browser effects. Without `--preview`, readable CLI mode displays the plan and asks for explicit approval. JSON apply is the trusted Pi adapter transport: after obtaining the required consent, it resubmits the same request with `--plan <accepted-plan-id> --json`. A plan ID detects drift; it is not proof of consent. `--json` or `workspace.auto_confirm` alone never approves either operation.

Initial clones always require consent; verified read-only reuse does not. Every opening batch requires one fresh confirmation showing all URLs. Unknown IDs, changed plans and unsafe destinations fail closed. Do not bypass missing targets or consent through shell/browser fallbacks. Opening uses the host default browser and returns launch status, not page content or proof of page load. Browser traffic is outside SBX policy.

Clone failure can leave a reported `partial_path`. Opening can succeed for some targets and fail for others. Scripts must inspect `ok`, clone `status`/`error` and per-target opening `results`; a returned structured partial/cancelled result is not success. There is no automatic destructive cleanup, rollback, or whole-batch retry.

See [Git cloning](integrations/git.md#host-source-cloning) and [GitHub opening](integrations/github.md#provider-owned-browser-opening).

## Task completion

Use `radar task done <task-id>` on any unfinished task, including tasks without authored notes. Use `radar task reopen <task-id>` to undo the local completion. Neither command changes external source state, creates a workspace, or runs cleanup immediately.

## Output for humans and scripts

Commands print readable tables, labeled details, or action summaries by default. The format does not change when stdout is piped. Add **`--json`** for machine-readable output, either before the command or after its arguments:

```sh
radar tasks
radar tasks --json | jq '.tasks // [] | .[] | {id, title}'
radar --json status | jq '.summary'
radar task done 42 --json
radar workspace-context --json | jq '.desired'
radar repository-refs --repo /path/to/repository --json
```

JSON retains the full result and its existing field names, without colors, table formatting, or progress text. In particular, `tasks` and `status` return the daemon response object; task mutations return the task itself. Human presentation is not a parsing contract.

- Results go to stdout; warnings, errors, authentication messages, and confirmation prompts go to stderr. JSON results retain structured warning fields. Runtime errors with `--json` are JSON objects on stderr; usage errors and help remain text.
- Exit status is `0` for a completed command, `1` for runtime errors, and `2` for invalid arguments. Reconciliation can return an incomplete/retryable plan result: scripts must also inspect `ok`, `retryable`, and `reconfirm_required` rather than treating a returned plan as successful convergence.
- `config-path`, `state-path`, and `log-path` keep their convenient bare-path default; with `--json` they return `{ "path": "..." }`. `version`, `stop`, `restart`, `rate-limit`, and `activity` also accept `--json`. Activity publication remains silent by default.
- `--json` selects output only: it does not approve destructive actions. Cleanup and deletion still read confirmation from stdin; cancellation produces no result. Authentication may still require interaction. Complete `radar setup` before JSON-mode creation; JSON mode never launches the setup wizard.
- The dashboard, interactive `create`, `fork`, `setup` (including `setup notifications`), `update`, and foreground `daemon` do not produce JSON results. Use `create --name <name> --json` for a creation result.

Radar's Pi extension and editor integrations must explicitly pass `--json` when decoding results. The CLI and Pi extension should be updated together.

## Local state

SQLite at `$XDG_STATE_HOME/radar/state.db` (or `~/.local/state/radar/state.db`) owns durable task identity, preferences, lifecycle decisions, note associations, bindings, acknowledgements and review safeguards. Notes contain only content. Override the database path with `RADAR_STATE=/absolute/path/state.db`.

`radar reset` clears collected observations and derived views while preserving durable records. Unknown schema versions and corrupt data fail closed instead of being discarded. Runtime does not read old JSON/frontmatter state.

```sh
radar state-path
radar state-backup /absolute/path/backup.db
radar state-export > radar-state.json
```

Back up notes and the database together. Existing installations [migrate automatically on first startup](sqlite-state-migration.md); explicit migration commands provide read-only preflight and offline recovery. `radar task title` changes display metadata without changing note bytes; `radar task note` explicitly repairs an external note move after workspace cleanup.

## Logs

The daemon writes logs to:

```sh
radar log-path
```

By default this is `$XDG_STATE_HOME/radar/radar.log` or `~/.local/state/radar/radar.log`.

Follow logs with:

```sh
tail -f "$(radar log-path)"
```

Override the log path with `RADAR_LOG=/path/to/radar.log`.

Set log level with:

```sh
RADAR_LOG_LEVEL=debug radar daemon
```

Supported levels: `debug`, `info`, `warn`, `error`. Default is `info`.

`workspace-context --resources-only` is the fresh lifecycle projection used by Pi. It returns member instructions/skills and owned sandbox/shared-directory routing without ports, dirty status, repository discovery, opening resources or a reconciliation revision. Use full `workspace-context` before reconciliation.

Completed cleanup and reconciliation responses can contain `resource_refresh_pending: true`. Removal/provisioning has completed; the cached task/resource list is explicitly pending the next local collection. Partial cleanup failures include their successful removal prefix. The dashboard labels pending lists. A complete collection started after the mutation clears the marker; failed or canceled collection keeps it pending.

Authored-note identity conflicts are never automatically merged or reset. `repair-task-links` defaults to a read-only JSON plan; applying requires explicit ownership/history decisions, an offline state lock and a new SQLite backup. See [task link repair](task-link-repair.md).

`feedback-diagnostics --section cleanup_check --parent-id <retained-task-id>`
runs the native confirmed-manual planner and shared bundle admission/preflight,
never provider Cleanup. It requires separate fresh exact-task/scope approval in
Pi; direct host callers must obtain that consent themselves. Scope defaults to
the registered origin and selects the entire ordered task bundle, including
other workspaces. Real admission may create coordination files/directories,
inspect tmux/protected workspace trees and transiently contend; provider/Git
reads may refresh metadata. All locks are released. Returned aggregate guard
success is not complete last-moment eligibility, runtime/resource evidence,
a reservation, apply token or deletion/retry consent. No subset, cursor, state,
expiry/retirement or action/plan selectors; no automatic replay after unknown
outcomes. Matching schema 40 is required. See
[guard-check safeguards](integrations/pi-cleanup-check.md).

`feedback-diagnostics --section refresh --scope installation` defaults to a
non-mutating preview of all registered collection sources. Apply requires
`--action apply --plan-id <latest-preview-id>` and separate fresh consent in the
Pi tool; plan identity is freshness, not consent. Only installation-wide native
collection/reconciliation is supported, not provider/workspace subsets. The
collection-only taskservice adapter never invokes ordinary daemon refresh or its
hourly workspace GC. Native reconciliation may update notes, bindings, lifecycle
and deliver collection events; provider reads may refresh Git metadata using
existing access. No cleanup, login/setup or runtime start is requested. Busy
admission fails without queued work. One-use current-daemon plans expire after
60 seconds or revision/target drift; native execution has a cooperative 30-second
deadline. Cancellation/disconnect can leave partial changes, not rollback.
Returned per-source counts are collection attempts, not verified resources;
missing/canceled evidence stays unverified. Never replay an unknown apply outcome.
CLI/daemon/Pi schema 40 must match before apply, with no fallback. See
[refresh safeguards](integrations/pi-diagnostics.md#explicit-installation-wide-collection-refresh).

`feedback-diagnostics --section operations` pages volatile current-daemon managed
operation observations using origin/feedback/installation scope and exact `--id`.
Active plus latest 64 terminal/client-reported entries are not complete history.
Client reports remain unverified; handler success is not resource verification.
Native handler rows separately expose start-only or start/finish UTC timestamps
and monotonic elapsed milliseconds. Missing/report timing is unknown, not zero;
wall-clock jumps need not match elapsed time. This is not phase/lock-wait timing,
pre-start queueing, post-finish reconciliation or resource-outcome verification.
Separately labelled native cleanup phase aggregates expose completed interval
counts and summed individually millisecond-truncated durations for eleven fixed
labels. Nested phases overlap, not additive handler elapsed time or active-phase
progress; missing evidence is unknown and invalid/overflowed sums unavailable.
No raw phase payloads, lock-wait timings or complete phase coverage are exposed.
Cleanup counts describe only the successful prefix and original target count,
not failed/remaining target identities. Scope uses cached retained references,
not verified ownership; task-wide effects may include other workspaces. No replay,
refresh or maintenance occurs. Pages are capped at 32 KiB; `--state` and
`--parent-id` are rejected. See [managed operation evidence](integrations/pi-diagnostics.md#current-daemon-managed-operations).

`feedback-diagnostics --section cleanup_preview --parent-id <retained-task-id>`
returns a complete confirmed-manual native plan (default `--action preview`).
`--scope origin|feedback|installation` selects the current production task,
including its related resources in other workspaces. No manual `--id`, `--limit`,
`--cursor`, `--state`, `--workspace-id` or target subset is accepted. All ordered
opaque target/effect/safety entries must fit 32 KiB or no plan is issued.
Fresh preview approval covers provider/Git metadata reads and transient
nonblocking exclusive existing-lease probes; missing lease files are unknown.
Apply uses `--action apply --plan-id <exact-plan-id>` with the same task/scope.
The caller must separately obtain fresh destructive whole-bundle consent; Pi
requires its latest immutable unexpired preview and an interactive confirmation.
A plan is freshness, not consent; native ownership/activity/alias/lease/provider
guards remain. Cleanup can lose local changes, branches, sandbox state, terminal
processes and workspace files/registration where disclosed. Native admission
may create coordination files. One-use plans expire after 60 seconds, reject
registration/revision/publication drift and cannot be replayed after partial or
unknown outcomes. Returned operation/prefix/dispatch evidence is not verified
resource absence or rollback. Deadlines are cooperative (native 30 seconds),
not forced cancellation. `--cleanup-mode expired --workspace-id <registered-id>`
remains a separate paged read-only proposal and rejects apply. Match CLI, daemon
and loaded Pi schema 40; no persisted-format change or legacy fallback. See
[cleanup previews and apply](integrations/pi-diagnostics.md#explicit-native-manual-cleanup-previews).

`feedback-diagnostics --section agents` separately exposes available current-daemon
native retry/backoff records and schedule-delivery-error presence, without runner
or runtime probes. Busy/missing/ambiguous managers are unavailable, not healthy;
no retry record is not success or stopped-state evidence. Records are volatile,
ID-keyed and not registration-lifetime/history attestations; retry deadlines are
not execution guarantees. Scoped pagination invalidates on shown memory changes.
See the Pi diagnostics documentation for fields and limitations. CLI, daemon and
loaded Pi must match schema 40; agent inventory itself exposes no maintenance action.

Native cleanup operation inspection also reports bounded executor stop stages and
provider Cleanup-call counts, never verified resource outcomes or safe retry.

`feedback-diagnostics --section workspace_policy --parent-id <registered workspace ID>`
reads the exact scoped **recorded sandbox recipe shape**: fixed presence flags
and complete mount/port counts, never raw names, paths, argv or referenced files.
It accepts origin/feedback/installation scope, but no row/state/cursor selectors.
No registered sandbox is not runtime absence; unavailable registration has no
fallback. This is not current repository/global effective policy, historical
setting provenance, live runtime configuration or execution eligibility. CLI,
daemon and loaded Pi require schema 40, without a persisted-format change. See
[the policy evidence contract](integrations/pi-policy.md#recorded-workspace-policy-diagnostics-schema-40).

`feedback-diagnostics --section repository_policy --parent-id <registered workspace ID>
--id <member row ID from workspace_resources>` explicitly reads only that registered
source repository's fixed `.radar.yaml` (64 KiB maximum). Pi requires fresh exact-target
file-access approval; the CLI request itself is explicit local file inspection, not
publication consent. Ten presence/mode/count declarations never imply effective
policy. Missing configuration differs from unavailable repository evidence; no
referenced files, runtime/provider calls or maintenance. See
[the repository declaration contract](integrations/pi-policy.md#current-registered-member-repository-declarations-schema-40).

`feedback-diagnostics --section recreation_preview --parent-id <registered-workspace-id>`
previews one exact registered sandbox (origin by default; feedback/installation
remain exact-target). `--action apply --plan-id <latest-opaque-plan>` uses the
existing native executor and one-use 60-second binding, not an inventory subset
or private native confirmation. Pi requires separately fresh exact-target
destructive consent after approved preview. Recreation discards sandbox-local
state and interrupts commands/services; native guards remain authoritative.
Native restoration-check returns are not independent resource verification.
No replay, automatic retry or silent replan after partial/unknown outcomes.
Matching CLI/daemon/loaded Pi schema 40 is required; no old probe/shell fallback.
See the [recreation contract](integrations/pi-recreation-preview.md).

`feedback-diagnostics --section overview` reports schema-40 typed configuration
policy values and file/default provenance. Its fixed ten-setting allowlist returns
only booleans, enablement modes and counts from a bounded native configuration
read, never model strings, paths, commands, secrets or referenced file contents.
Top-level `settings` are CLI-process global defaults; `daemon.settings` separately
reads the same allowlist in a matching running daemon process. Independent scope
and capture times distinguish process configuration drift. Neither is an environment
dump, loaded-settings attestation, repository/workspace-effective policy, live access
or action permission. Old/mismatched daemons remain unavailable without fallback. Unsafe/invalid evidence is
unavailable; no refresh or maintenance follows. See [configuration policy and
origins](integrations/pi-diagnostics.md#configuration-policy-and-origins) for exact semantics.

The schema-40 [exact registered-agent stop control](integrations/pi-agent-stop.md)
uses `--section agent_stop --parent-id <retained task> --agent-id <registered agent>`
with origin/feedback/installation scope, `--action preview`, then exact `--action apply
--plan-id <latest plan ID>`. Pi requires fresh interactive preview approval and
separate exact-target interruption consent for the complete immutable latest
unexpired preview bound to the registered feedback workspace and Pi session.
Stop sets desired false and may interrupt unfinished work; it preserves registered
ownership, data and sandbox. Native handler return is not process exit or verified
resource outcomes. No daemon startup, generic agentCall, retry, replay, silent
replan, start/restart/remove or sandbox-stop fallback. Plan IDs are never consent;
the local CLI is a trusted adapter, not an approval-token issuer.
