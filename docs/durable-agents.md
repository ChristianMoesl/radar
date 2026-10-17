# Programmable durable agents

Radar can supervise a code-defined [pi-durable](https://github.com/earendil-works/pi/tree/main/packages/durable) program, deliver events to it, and show its progress and requests for attention. Every registration belongs to an existing Radar workspace. The definition owns models, tools, routing, tasks, conversations, and business behavior. Radar has no built-in mapping from a pull request, ticket, or alert to an agent.

## Install and register

The Node host is a separate project dependency, `@christianmoesl/radar-agent-host`, requiring Node 24+. It uses pi-durable, pi-ai and chord **1.3.0**. It is independent of the interactive `pi-radar` package. Ordinary Pi extensions and session files are not durable definitions.

Until the first npm release is approved, build and install a local package from this checkout:

```sh
pnpm install --frozen-lockfile
pnpm --filter @christianmoesl/radar-agent-host build
pnpm --dir agent-host pack --out /tmp/radar-agent-host.tgz
mkdir -p /path/to/my-agents
cd /path/to/my-agents
npm init -y
npm install /tmp/radar-agent-host.tgz
```

After publication, install the exact approved host version in that project and retain its lockfile. Radar finds `node_modules/.bin/radar-agent-host` by walking up from the definition's file. It never downloads a host automatically. For checkout development only, set `RADAR_AGENT_HOST=/path/to/radar/agent-host/src/runner.ts` before starting the daemon. Restart the daemon after changing its inherited environment.

Configure Radar normally with `radar setup`, including the canonical note vault and workspace settings. Select an existing workspace, or prepare a new one without launching tmux/Pi:

```sh
radar create --prepare-only --name automation --preview --json > plan.json
# Inspect the complete plan, including its note, before applying it.
radar create --prepare-only --name automation \
  --plan-id "$(jq -r .plan_id plan.json)" \
  --prepared-note "$(jq -c .note plan.json)" --json
```

The returned `path` is the registration's workspace. Workspace creation follows existing note, authorization and SBX settings. Native model tools apply workspace changes only when automatic confirmation is configured. Otherwise they return the complete request/plan; the definition persists them, presents a human action, and applies that exact request after a host-authenticated user event. The model cannot confirm its own plan. `workspace.auto_confirm: false` requires an accepted creation plan. `--prepare-only` does not start an interactive agent. Supplying `--workspace <registered-path>` to prepared creation adds members to that workspace through the same planner. `reconcile-workspace --prepare-only` likewise applies resource/setup changes without opening Pi.

Create `counter.ts` inside the agent project:

```ts
import {
  createModels, createRegistry, defineDoc, defineTask, present,
  type AgentDefinition, type AgentEvent,
} from "@christianmoesl/radar-agent-host";

const Count = defineDoc<{ n: number }>({
  kind: "example.count", version: 1, scope: "session",
  initial: () => ({ n: 0 }),
});
const Event = defineTask<AgentEvent, { phase: "count" }, number>({
  name: "example.event", version: 1, initial: () => ({ phase: "count" }),
  phases: {
    count: async (_task, runtime, context) => {
      await runtime.commit(async tx => {
        const count = await tx.doc(Count);
        count.n++;
        await present(tx, { title: "Counter", summary: `${count.n} events`, attention: true });
        return { status: "terminal", outcome: { status: "completed", result: count.n } };
      }, context);
    },
  },
  abort: async (_task, runtime, context) => {
    await runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted" } }), context);
  },
});
export default {
  version: 1,
  configure: () => ({ models: createModels(), registry: createRegistry() }),
  eventTask: Event,
} satisfies AgentDefinition;
```

Register and send an event. A background/state-only definition still requires a workspace; it needs no model credential if it never uses a model.

```sh
radar agent add ./counter.ts --id counter --workspace /path/to/workspaces/automation
radar agent start counter
printf '%s' '{"value":1}' | radar agent emit counter --source fixture --event-id one
radar agent inspect counter
radar agent list
radar agent stop counter
radar agent start counter
# Redelivering source=fixture/id=one returns the original receipt.
```

Registration is initially stopped. Config is a non-secret JSON object supplied using `--config file.json`. Credentials belong in the daemon's inherited environment or a definition's explicit binding, not in configuration, events or presentation. `radar agent token <id>` prints its event-ingress bearer token; keep it private.

## Workspace and SBX execution

Registration persists the canonical workspace ID and path. Start checks them again before recovery. A missing or replaced workspace, stopped/missing sandbox, or missing required SBX mounts fails visibly. A workspace configured for SBX never falls back to host execution. A global SBX default also prevents starting an agent against a workspace that has no sandbox; reconcile its resources first.

`configure(host)` receives `id`, `dataDir`, `config`, `workspace` (`id`, `path`, `mode`, optional `sandbox`), and `execution`. Use `host.execution.exec(argv, cwd, signal)` for commands, `readOnly`/`listOnly` for confined reads, and `loadSkill` for workspace skill instructions/resources. These call the existing workspace/SBX backend through `radar workspace-exec`. Working directories must belong to the associated workspace or its registered members. Each execution rechecks current workspace/SBX policy.

Radar supplies `HarnessOptions.env`; definitions select conversation working directories and tools, rather than replace that environment. The native adapter supports bounded reads/writes, directory inspection, binary readers and commands. Reads are limited to 512 KiB; command responses to 1 MiB and two minutes. Streaming file watchers, spill files, temp files and environment overrides are explicitly unsupported. Unsupported operations return an error. Commands use the workspace environment's PATH and credentials; they do not automatically inherit every host variable into SBX. Use Radar's existing sandbox environment configuration for tools that need credentials.

Node orchestration, definition imports, SQLite and model/remote-API clients remain on the host, like ordinary Pi orchestration. Definitions are trusted user code: arbitrary JavaScript can access host APIs. SBX isolates the workspace tools routed through this adapter; it is not a security boundary around the definition itself. A working directory alone does not confine a shell. The example review extension offers only confined file read/list tools, with no shell or write tool exposed to the model. Symlink escapes from selected read roots are rejected in the selected environment.

Keep selected skill files and their resources inside mounted workspace paths. `host.execution.loadSkill(path)` reads `SKILL.md` through that environment and returns its name, source/root, SHA256 and instructions. Code can record the hash in its own durable run state. Restart does not freeze edits to code or skill resources: preserve versions/resources required by unfinished work and bump native task/doc versions intentionally.

## Interact in Radar or the CLI

The compiled Agents source shows each registered instance and its agent-authored summary/attention. Enter opens its detail view. Open the associated workspace, inspect exposed conversations, use declared actions, or send a message when the definition enables it. Start/stop/restart and logs are available there. Runtime failures remain visible separately from agent presentation.

`present(tx, patch)` persists title, summary, attention, actions, optional messaging, exposed conversations and workspace references in the same native transaction as the agent's state. Every update increments `revision`. Actions carry definition-owned opaque JSON; human input becomes the same durable event task as external input. Radar never executes domain actions itself. Native task input has a host-assigned `origin` (`external` or `user`); definitions must require `origin === "user"` for actions that require human intent. Producers cannot set this field, and `radar.user` is a reserved source.

```sh
radar agent inspect counter --json
radar agent message counter 'Please summarize' --revision 3 --event-id question-1
radar agent action counter action-id 'Optional text' --revision 3 --event-id decision-1
radar agent conversation counter 2
radar agent logs counter
```

Use the current presentation revision. A stale decision fails without creating a task; refresh and reconsider it. Reusing the same event ID/content returns its original receipt even if presentation has since changed. The TUI retains the original ID after an ambiguous delivery failure so retry cannot silently duplicate a click. Messaging/conversations remain optional. The CLI prints JSON only with `--json`.

## Feed remote events

```sh
radar agent serve --listen 127.0.0.1:7433
```

POST `/agents/<id>/events` with `Authorization: Bearer <instance-token>` and JSON `{ "source": "producer", "id": "stable-delivery-id", "payload": ... }`. Accepted or duplicate delivery returns HTTP 202 with a durable receipt. Changed content under an existing identity returns 409; malformed input 400, authentication failure 401, unavailable/uncertain admission 503. Payload/frames are bounded to 1 MiB; leave room for transport wrappers. Reuse source/ID/content after a lost response. A receipt confirms admission, not business completion.

Ingress listens on loopback by default. For remote producers, use an authenticated TLS reverse proxy/tunnel and configure producer retries. Radar is a local service: it does not receive events while unavailable, expose a hosted inbox, or poll/schedule sources automatically. A producer or external scheduler owns those responsibilities.

The editable GitHub bridge in `agent-host/examples/github-webhook.ts` verifies HMAC-SHA256 over the original request bytes, validates delivery/event headers, and forwards the opaque event with its delivery ID. Configure `RADAR_EVENT_URL`, `RADAR_EVENT_TOKEN`, and `GITHUB_WEBHOOK_SECRET`; see the example for its listener configuration. It does not filter PRs or create conversations; that belongs in the definition.

## Editable examples

Source lives in `agent-host/examples`; packed JavaScript examples live in the package's `dist/examples`. Copy the selected definition and `shared` helper into your project. Replace their `../src/index` imports with `@christianmoesl/radar-agent-host`, and `../src/tools` imports with `@christianmoesl/radar-agent-host/tools`. Use `.ts` or `.js` consistently for copied helper imports. The SDK is normal code, without a workflow manifest or DSL.

**Review assistant:** configure `reviewer`, `repositories` (GitHub `owner/name` to mounted local repository paths), `modelId`, and optionally `skill`. Register against a prepared workspace containing those repositories. Copy `examples/review/SKILL.md` to `<workspace>/skills/review/SKILL.md` or select a workspace skill. Supply `OPENAI_API_KEY` before starting the daemon. The example filters review requests for the configured user, handles synchronization/end-of-request events, records exact base/head IDs, prepares a distinct review worktree per head inside the associated workspace, and exposes findings/follow-up. Newer heads mark older conversation labels outdated. Findings are local; the example does not post GitHub reviews. It requires local Git access to both commits, or configured origin fetch access inside SBX. With manual workspace confirmation, use its declared preparation action after inspecting the plan.

**Jira assistant:** configure `baseUrl`, `jql`, `accountId`, `modelId`, and optionally `allowPosting: true`. Supply `JIRA_EMAIL` plus `JIRA_API_TOKEN`, or `JIRA_ACCESS_TOKEN`, and the model credential. Emit `{ "type": "jira.poll" }` using a new stable poll ID per scheduled execution. It paginates the selected issue set and comments, tracks changes/direct ADF mentions and later replies, retains ticket conversations, and presents a summary/draft. Optional posting requires a declared human action with the exact reply text; comment properties are used to reconcile uncertain posts. This cannot guarantee exactly-once comments under every provider consistency/permission failure. Access is limited to the configured JQL and account permissions; changes outside that selection and Jira data unavailable to the API are not covered. This uses configured credentials and does not implement issue #6's OAuth login.

**Datadog investigator:** configure `baseUrl` for your Datadog site, `modelId`, and selected `logsQuery`, `metrics` (name-to-query map), `spansQuery`, or workspace `runbook`. Supply `DD_API_KEY` and `DD_APP_KEY` with the corresponding read scopes. Emit `{ "type": "datadog.alert", "incidentKey": "example", "monitorId": "7", "transition": "alert", "timestamp": "2026-10-17T00:00:00Z" }`. Your webhook adapter owns signature/authentication and translates its payload into this definition-owned schema. The example gathers bounded logs/metrics/spans evidence, consolidates repeats (default five minutes), retains recovery history and accepts follow-up. Missing telemetry is recorded as incomplete evidence. It recommends investigation steps and has no remediation tool. The core Datadog snapshot collector is independent of this event adapter.

Examples require explicit `fixture: true` to use the deterministic local model. It returns labelled test output and does not demonstrate real model quality. Never treat fixture findings as a real review or investigation.

## Persistence, recovery and lifecycle

One detached Node runner owns each instance's native SQLite store. The Go daemon uses newline JSON-RPC 2.0 over a private Unix socket, protocol **1**: `hello`, `deliver`, `interact`, `snapshot`, `watch`, `conversation`, `shutdown`. Stdout/stderr go to `runner.log`. RPC IDs are transient; source/event IDs identify durable admission. The host commits a receipt and a native event task atomically. There is no second Go queue and no promise of execution ordering across independent tasks. Definitions implement their concurrency and routing policies.

Storage defaults to `$XDG_STATE_HOME/radar/agents/<id>` or `~/.local/state/radar/agents/<id>` (`RADAR_AGENT_ROOT` overrides it). Registration/config/token, store format, exclusive-owner SQLite, native agent SQLite, cached snapshot and logs live there. All are separate from the rebuildable tasks cache and disposable workspace. A SQLite exclusive lock prevents a second store owner. Daemon restart reconnects to the runner; runner crashes release the lock and supervision retries with bounded backoff. Intentional stop stays stopped. Killing the owning Node process during a checkpoint resumes native work when restarted.

Admission receipts are retained without automatic pruning; event/document history and runner logs can grow. There is no destructive data-deletion CLI. `radar reset` leaves agent data intact. Every registration, including a stopped one, pins its associated workspace against Radar cleanup/expiry. Additional active workspace operations use OS leases. To release the association:

```sh
radar agent stop counter
radar agent remove counter
```

Removal deletes registration only and preserves durable data. Archive it before any deliberate filesystem deletion. Reusing an ID reopens that retained store, so only use compatible definition code. Restore a removed registration with `agent add` against the same workspace and compatible entrypoint/config. Never point an ID at unrelated retained work.

The initial registration/store formats are version 1 and reject invalid/incompatible metadata rather than silently reset it. Definition reload is explicit with `radar agent restart`. Native task versions must remain recoverable; changing unfinished task semantics without a migration/version plan can block work. Future incompatible schema rollouts require inventory and read-only preflight before install, following `AGENTS.md`.

Tests establish process-crash recovery, not a universal host/power-failure guarantee. Native SQLite uses its published adapter transaction/WAL durability; backups and reliable persistent disks remain the operator's responsibility. External effects can happen before a checkpoint records their result. Repository setup commands may rerun after interruption and must be idempotent. Use native replay policies, stable operation IDs and reconciliation appropriate to each API. Never infer exactly-once external effects from atomic event admission.

## Develop and validate

```sh
go build -o /tmp/radar ./cmd/radar
RADAR_BINARY=/tmp/radar pnpm --filter @christianmoesl/radar-agent-host check
pnpm --filter @christianmoesl/radar-agent-host build
go test -p 2 ./...
pnpm check
```

The full review test uses the actual CLI/daemon, Git worktrees, note registry, a local deterministic model and isolated state. Other tests cover receipt loss, SIGKILL checkpoints, owner exclusion, stale actions, provider HTTP fixtures, workspace pinning and SBX command routing/failure. Real provider accounts and a real SBX VM are separate validation environments; fixture success does not establish those live integrations or model quality.

Host packages are packed in CI, separately versioned from the CLI, and can be staged by `.github/workflows/agent-host-package.yml`. A maintainer must bootstrap the npm package, configure that workflow as a **stage-only** trusted publisher, then review/approve each stage with 2FA. CI never directly publishes it. See [npm publishing](npm-publishing.md) for the existing staging procedure. No account or external credential is created by this feature.
