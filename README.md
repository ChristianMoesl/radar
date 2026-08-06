# Radar

Radar is a CLI-first tool for keeping track of engineering work that needs your attention. It combines a terminal UI, scriptable commands, a background daemon, Obsidian task authoring, GitHub/Jira/Datadog/Git/tmux/sbx collection, and workspace creation in one Go binary.

## Install

Download the matching archive from the [latest release](https://github.com/ChristianMoesl/radar.nvim/releases/latest), verify it with `checksums.txt`, and run its installer:

```sh
archive=radar_<version>_<os>_<arch>.tar.gz
grep -F "  $archive" checksums.txt | shasum -a 256 -c -
tar -xzf "$archive"
"${archive%.tar.gz}/install.sh"
radar version
```

The installer uses `~/.local` by default. Set `PREFIX` to install elsewhere. macOS archives also install the `RadarNotifier.app` companion under `libexec/radar` and register it with Launch Services.

## Update

Download the new release archive, verify it with `checksums.txt`, and run its installer over the existing installation. Run `radar restart` after updating if the daemon is already running.

## Development setup

Install the local development tools:

```sh
brew install go fd git tmux neovim
curl -fsSL https://pi.dev/install.sh | sh
```

Linux developers also need `xdg-open`, usually provided by the system `xdg-utils` package:

```sh
sudo apt-get install xdg-utils
```

Build, test, and install a local Radar binary:

```sh
make test
make build
make install
radar version
```

## Build

```sh
make build
```

Install a local build:

```sh
make install
```

## Release

Releases are tag-driven. To publish versioned Linux and macOS binaries from a clean, up-to-date `main`:

```sh
make release VERSION=v0.1.0
```

The release script tests, builds the release archives, creates a signed annotated tag, and pushes it. The release workflow then publishes `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64` tarballs, plus `checksums.txt`, with generated notes from the changes since the previous tag.

Release assets should not be replaced after publishing. If a release is wrong, publish a new patch version.

The sandbox image is released separately because it packages frequently updated tools such as Node, pnpm, and gh. The sandbox image workflow runs weekly and can be triggered manually. It publishes:

```text
christianmoesl/radar-sandbox:YYYY.MM.DD
christianmoesl/radar-sandbox:latest
```

Publishing the sandbox image requires the `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` GitHub secrets.

## Prerequisites

Radar uses these local tools:

- `fd` for fast repository discovery in `radar create`
- `git` for repository and worktree operations
- `tmux` for workspace creation; the default tmux configuration also runs `pi` and `nvim`
- `sbx` and the [`pi-sbx`](https://github.com/ChristianMoesl/pi-sbx) Pi extension on macOS for repositories that enable sandboxed tool execution

On macOS, the daemon uses the installed Radar notifier companion to send host notifications when a task newly needs immediate attention or attention. Clicking a pull-request notification opens the relevant GitHub pull request; clicking a Datadog alert opens its monitor; other task notifications open their task URL when one is available. Existing actionable tasks are not notified again on every refresh or daemon restart. Muted and deprioritized tasks do not produce notifications. If the companion app is not installed, Radar continues without host notifications.

Radar opens task URLs with the platform URL opener when you press `o` and choose a URL-backed source such as Jira, GitHub, or Datadog:

- Linux: requires `xdg-open`, usually provided by `xdg-utils`
- macOS: uses the built-in `open` command

## Run

Start the daemon:

```sh
./radar daemon
```

Open the terminal UI:

```sh
./radar
```

Open Radar in a tmux popup from tmux:

```sh
tmux display-popup -E "radar"
```

Useful tmux bindings:

```tmux
bind-key R display-popup -E "radar"
bind-key F display-popup -E "radar fork"
```

The TUI supports:

```text
j / down     move down
k / up       move up
enter        switch/create the selected task's tmux session
o            open a source action or link, including Obsidian, GitHub, Jira, and sbx
i            inspect selected task
c            create workspace
x            clean up all local resources linked to the selected task
X            garbage-collect all eligible workspaces
f            edit config
r            refresh
q / esc      quit
```

The create flow is step-by-step: fuzzy search a repository, choose whether to work on an existing branch or create a new one, then select the existing branch or the new branch's starting reference. New branches also prompt for a name. Repository paths are displayed as `~/...` when they are inside your home directory.

## Workspaces

Open the interactive workspace creation flow:

```sh
./radar create
```

Create a workspace non-interactively:

```sh
./radar create --repo /path/to/repo --base origin/main --name my-feature
```

For a new branch, Radar creates:

- a Git worktree at `<workspace_root>/<repo>/<name>`
- a sanitized branch named after the workspace
- files copied from the source repo when configured
- setup commands run in the new worktree when configured
- a matching tmux session
- user-configured tmux windows and panes; by default, separate `pi` and `nvim` windows

A Radar workspace is one logical bundle: one primary Git worktree, optional member worktrees from other repositories, one tmux session, and at most one SBX sandbox. Radar stores this durable membership in `<workspace_root>/.radar-workspaces.json`; `radar reset` does not remove it.

Every Pi process started by Radar receives the embedded `radar_add_worktree` tool. The tool previews and validates the exact repository, branch, destination, tmux session, and SBX changes, then fails closed unless the user confirms through Pi's UI. It supports new and existing branches and reuses the logical workspace's current tmux and Pi sessions.

The same operation is scriptable without an extra prompt because invoking the CLI is already explicit:

```sh
./radar add-worktree --workspace /path/to/current/worktree --repo /path/to/other-repo --branch-mode new --name DPSCAP-123-update-cache --base origin/main --preview
./radar add-worktree --workspace /path/to/current/worktree --repo /path/to/other-repo --branch-mode new --name DPSCAP-123-update-cache --base origin/main
./radar add-worktree --workspace /path/to/current/worktree --repo /path/to/other-repo --branch-mode existing --branch feature/DPSCAP-123-update-cache
```

`--workspace` defaults to the process working directory. Preview and apply return JSON. Applying the same request again is safe: Radar reuses an already-correct worktree, repairs missing registry or sandbox state, and does not schedule setup twice after it has been recorded successfully.

For an existing branch, Radar reuses its local branch or creates a same-named local branch that tracks `origin/<branch>`. A branch already checked out in a Radar workspace reopens that workspace. To keep a normal source checkout for `.env` files, local services, and database setup while making `main` available to a Radar worktree, park the clean source checkout on the remote-tracking commit:

```sh
git fetch
git switch --no-overwrite-ignore --detach origin/main
```

Ignored development files remain in place. Radar refuses to move a branch that is still checked out in the source repository.

Configure repo-specific workspace setup with a repo-local `.radar.json` file:

```json
{
  "copy_files": [".env", ".env.local"],
  "setup": ["pnpm install --frozen-lockfile"],
  "sbx": {
    "enabled": true,
    "kit": {"name": "radar", "path": "~/kits/radar"},
    "additional_mounts": ["~/repo-tools"]
  },
  "model": "anthropic/claude-sonnet-4",
  "thinking": "high"
}
```

`copy_files` paths are relative to the repository root. `setup` commands run in order from the new worktree in a temporary setup window after tmux and any sandbox are available. Without sandboxing they run on the host. On macOS, when `sbx.enabled` is true, Radar first creates an SBX sandbox for the workspace with `sbx create --name <sandbox-name> [--kit <path>] <kit-name>`, then runs setup commands inside it with `sbx exec`. The deterministic sandbox name is capped at 63 characters. The sandbox mounts every member worktree, each external writable Git common directory, and global and repository `sbx.additional_mounts`. Pi and nvim run on the host; the globally installed [`pi-sbx`](https://github.com/ChristianMoesl/pi-sbx) extension discovers the matching sandbox and routes Pi's regular tools through `sbx exec`. Radar separately injects its own host-side Pi extension for `radar_add_worktree`; users do not install a Radar Pi package. Install `pi-sbx` with `pi install git:github.com/ChristianMoesl/pi-sbx`. `model` and `thinking` are passed to Pi as `--model` and `--thinking` for the workspace session.

Adding a member to a sandboxed workspace reconciles the complete mount set by removing and recreating the sandbox under the same name. This interrupts processes inside the sandbox, so the Pi tool warns before confirmation. If recreation fails, Radar keeps the worktree and desired registry state and returns `ok: false` with `retryable: true`; repeating the request converges instead of rolling back the completed steps.

Enable sandboxes by default, select the kit, and mount additional host directories into every sandbox in the user config at `radar config-path`:

```json
{
  "sbx": {
    "enabled": true,
    "kit": {"name": "shell"},
    "additional_mounts": ["~/shared-tools", "/opt/company-config"]
  }
}
```

A repository's `.radar.json` can use the same `sbx` fields. Repository `enabled` and `kit` values override the user settings; repository additional mounts are appended to the global list. `kit.name` defaults to `shell`; when optional `kit.path` is set, Radar expands a leading `~/` and passes it as `--kit <path>`. Additional-mount paths must be absolute or start with `~/`; Radar expands `~` and creates missing directories before starting SBX. Empty entries and duplicate paths are ignored, and the workspace remains the primary mount.

Configure workspace windows, panes, layouts, and commands in the user config:

```json
{
  "tmux": {
    "windows": [
      {
        "name": "workspace",
        "layout": "horizontal",
        "panes": [
          {"command": "pi $RADAR_PI_ARGS"},
          {"command": "nvim ."}
        ]
      }
    ]
  }
}
```

Every window requires a unique `name` and at least one pane command. Commands run from the workspace directory. The configuration must contain `$RADAR_PI_ARGS` exactly once; Radar replaces it with shell-quoted model, thinking, session, and optional fork arguments before starting tmux. Supported layouts are `horizontal`, `vertical`, `main-horizontal`, `main-vertical`, and `tiled`. Omitting `layout` leaves tmux's initial pane layout unchanged. The pane containing `$RADAR_PI_ARGS` is focused after creation.

When run inside tmux, Radar switches to the new session.

Fork the current tmux workspace into a sibling workspace and fork its Pi session:

```sh
./radar fork
```

`radar fork` detects the current git worktree and tmux session, asks for the base branch with the current branch prefilled, asks for the new workspace name, starts Pi with `--fork`, and switches to the new tmux session.

Clean up every local resource linked to a task:

```sh
./radar cleanup <task-id>
```

Cleanup shows one confirmation covering all linked Git worktrees, tmux sessions, and SBX sandboxes. For a multi-worktree logical workspace, it includes every member but removes the shared session and sandbox once. Dirty worktrees are called out explicitly and their uncommitted changes are discarded only after confirmation. Git branches, Jira issues, and GitHub pull requests are preserved. Standalone local-resource tasks are handled by the same command; for example, cleaning up a standalone tmux task removes only that session.

The daemon automatically garbage-collects local workspaces for tasks that have been done for at least 24 hours. A registered multi-worktree workspace is one conservative cleanup bundle: Radar skips all of it if any member is dirty or its shared tmux session is attached. Run `radar gc`, or press `X` in the TUI, to clean eligible done-task workspaces immediately without waiting for the 24-hour retention period. Manual garbage collection keeps the same clean-worktree and detached-session safety checks. Radar sends the garbage-collection result as a host OS notification.

## Obsidian-authored tasks

Configure one Obsidian vault before creating tasks:

```json
{
  "obsidian": {
    "vault_path": "~/Documents/Obsidian/Work"
  }
}
```

The vault and its `.obsidian/` directory must already exist. Radar creates `Tasks/` inside it. Each task is one direct Markdown child named after its title, such as `Tasks/Write the release process in Notion.md`, with a source-owned UUID in its frontmatter:

```sh
radar task create --title "Write the release process in Notion"
radar task done <task-id>
radar task reopen <task-id>
radar task priority <task-id> urgent
radar task priority <task-id> normal
```

The note filename owns the title, while its frontmatter owns open/done state, normal/urgent priority, and timestamps. The body owns working notes and outcomes. Radar projects those facts with live Jira, GitHub, Git, tmux, and SBX activity. An open normal note is low priority, urgent is immediate, linked activity can promote open work, and a done note remains terminal. Press `n`, `d`, and `p` for the same operations in the TUI, or `o` to open the note in Obsidian.

Obsidian notes are task records rather than workspaces. Radar preserves unknown frontmatter and the complete note body during atomic mutations and never deletes task notes. See [the Obsidian integration contract](docs/integrations/obsidian.md) for the schema and failure behavior.

## Scriptable commands

```sh
./radar task create --title <title>
./radar task done <task-id>
./radar task reopen <task-id>
./radar task priority <task-id> urgent|normal
./radar status
./radar tasks
./radar add-worktree --repo <repo> --branch-mode new --name <name> --base <base> [--preview]
./radar add-worktree --repo <repo> --branch-mode existing --branch <branch> [--preview]
./radar cleanup <task-id>
./radar gc
./radar refresh
./radar reset
./radar stop
./radar restart
./radar config-path
./radar state-path
./radar log-path
```

Task commands return JSON.

## Architecture

Radar's main executable is a single Go binary with three modes:

- terminal UI, opened by `radar`
- scriptable commands, such as `radar status` and `radar create`
- daemon mode, started with `radar daemon`

```text
TUI / CLI -> Unix socket -> radar daemon -> source-compiled integrations
```

The daemon keeps collection centralized so UI/status reads can use cached local state instead of polling external services repeatedly. It refreshes local Obsidian/Git/tmux/sbx state every 15 seconds and runs a full Obsidian/GitHub/Jira/Datadog/Git/tmux/sbx refresh every 5 minutes.

For internals, see [ARCHITECTURE.md](ARCHITECTURE.md). For how Radar decides what needs attention, see [docs/attention-algorithm.md](docs/attention-algorithm.md). To add a source-compiled integration, see [docs/integrations.md](docs/integrations.md).

## GitHub

GitHub integration uses the GitHub CLI. Make sure this works first:

```sh
gh auth status
```

Radar currently tracks:

- PR review requests assigned directly to you as `needs attention`
- open PRs authored by you as `in progress`

Radar checks GitHub rate limits before collection. When a budget is low, Radar pauses GitHub collection until GitHub's reset time. TUI and CLI status reads use cached daemon state and do not trigger GitHub requests.

## Jira

Radar collects authoritative assigned Jira Cloud work and discovers configured linking marks such as `ABC-123` in existing task titles. Title discovery fetches issues directly even when they are unassigned or outside the configured authoritative issue types.

Configure credentials through the environment:

```sh
RADAR_JIRA_BASE_URL="https://your-site.atlassian.net"
RADAR_JIRA_EMAIL="you@example.com"
RADAR_JIRA_API_TOKEN="..."
RADAR_JIRA_CLOUD_ID="..."
# alternatively: RADAR_JIRA_API_BASE_URL="https://api.atlassian.com/ex/jira/<cloud-id>/rest/api/3"
```

`jira.authoritative_issue_types` controls which automatically collected or title-discovered Jira issues can control a Radar task. It defaults to `Task`, `Bug`, and `Sub-task`:

```json
{
  "jira": {
    "authoritative_issue_types": ["Task", "Bug", "Sub-task"],
    "status_mapping": {
      "In Progress": "in_progress",
      "In Review": "in_progress",
      "Blocked": "attention"
    },
    "unmapped_status": "low_priority"
  }
}
```

Names are trimmed and matched case-insensitively. An explicitly empty array skips assigned Jira search and makes every automatically title-discovered issue informational. Omitting the option uses the three default types. The former `jira.issue_types` option is not supported.

Authoritative Jira refs can provide the task title, identity, attention, linking, and contributing lifecycle. An out-of-scope title discovery is shown as an informational **Jira reference** with its URL, status, issue type, priority, and status category, but it cannot rename, merge, reprioritize, complete, or reopen the task. Removing a key from all current title-bearing facts removes its derived reference on a complete refresh. When a Jira ref joins an Obsidian-authored task, Obsidian remains the primary lifecycle owner.

Every distinct key in a title is fetched in title order, up to 50 direct fetches per refresh. Informational refs remain independent; all authoritative refs participate in linking and lifecycle, the first authoritative key supplies the Jira title, and completion requires every authoritative remote ref to be done. Failed or inaccessible direct fetches are non-fatal, retain a previously known ref, and are reported in Jira source status.

Jira status names are trimmed and matched case-insensitively. Mapping targets may be `low_priority`, `in_progress`, `attention`, or `immediate`; Jira's Done category remains authoritative only for authoritative refs. By default, `In Progress` and `In Review` are in progress and every other authoritative non-done status is low priority. An explicitly empty `status_mapping` sends every authoritative non-done issue to `unmapped_status`.

## Datadog

Radar collects a current snapshot of unhealthy Datadog monitors every five minutes. It makes one monitor-search request per full refresh and does not query logs, traces, metrics, events, or monitor history. Each matching monitor becomes one Radar task:

- `Alert` becomes `immediate`.
- `Warn` and `No Data` become `attention`.
- A previously tracked monitor that no longer matches becomes `done` with reason `Datadog monitor recovered`.

Configure the scope as a Datadog monitor search query in the user config. Radar requires a non-empty query so it cannot accidentally collect every unhealthy monitor in an organization. Radar appends the active-state clause `status:(Alert OR Warn OR "No Data")`; do not include alert status in the configured query.

```json
{
  "datadog": {
    "monitor_query": "tag:team:cap"
  }
}
```

Provide credentials only through environment variables:

```sh
RADAR_DATADOG_API_KEY="..."
RADAR_DATADOG_APP_KEY="..."
RADAR_DATADOG_SITE="datadoghq.eu"
```

`RADAR_DATADOG_API_KEY` and `RADAR_DATADOG_APP_KEY` are required, and the application key must have permission to read monitors. `RADAR_DATADOG_SITE` is optional and defaults to `datadoghq.eu`; set it to the Datadog site hostname for another region, such as `us3.datadoghq.com`. Radar does not read Datadog credentials from its JSON config or a credentials file.

The query is limited to 1,000 results in one request. If it matches more, collection is marked as an error and the previous Datadog tasks are retained; narrow `datadog.monitor_query`. Because Radar polls current state rather than alert events, an alert that both starts and recovers between full refreshes is intentionally not shown.

A newly collected monitor produces the normal Radar macOS notification. Clicking it opens the monitor directly in Datadog. Radar does not repeat the notification on every refresh while the monitor remains unhealthy.

## Git worktrees

Radar collects Git checkouts at `<workspace_root>/<repo>/<workspace>`. Registered members emit a shared `workspace-group:<id>` linking key, so worktrees from different repositories appear in one task even without a configured linking mark. Radar also attaches worktrees by configured linking marks such as `ABC-123`. Regular repositories outside the configured workspace root are ignored. Branch names do not affect collection, so a workspace checked out directly on `main` remains visible.

## tmux sessions

Radar collects tmux sessions from the local tmux server and attaches them to matching tasks when their name contains a configured linking mark, or when the session working directory matches a Git worktree path. Sessions without matches are shown as standalone in-progress tasks.

Tmux session refs use `#{session_id}` for stable identity, so renaming a tmux session does not create a new Radar task. Selecting a tmux-backed task switches to the stable session target.

## Docker sbx sandboxes

Radar collects Docker sbx sandboxes with `sbx ls --json` when `sbx` is installed. Sandboxes attach to matching tasks through configured linking marks in the sandbox/workspace name and through their primary workspace path. Sandboxes without matches are shown as standalone in-progress tasks.

The default sandbox kit name is `shell`. Set `sbx.kit.name` to select another kit and optionally set `sbx.kit.path` to pass its location with `--kit`. Configure `sbx.additional_mounts` to add host directories to every sandbox Radar creates.

## Config

Radar uses one editable JSON config file:

```sh
./radar config-path
```

By default this is `$XDG_CONFIG_HOME/radar/config.json` or `~/.config/radar/config.json`.
The daemon creates an example file on startup if it does not exist yet.

Example:

```json
{
  "repository_dirs": ["~/workspace", "~/code", "~/src", "~/dev", "~/projects"],
  "linking_mark_prefixes": ["ABC"],
  "obsidian": {"vault_path": "~/Documents/Obsidian/Work"},
  "model": "github-copilot/claude-sonnet-4.5",
  "thinking": "medium",
  "sbx": {
    "enabled": true,
    "kit": {"name": "shell"},
    "additional_mounts": []
  },
  "datadog": {
    "monitor_query": "tag:team:cap"
  },
  "jira": {
    "authoritative_issue_types": ["Task", "Bug", "Sub-task"],
    "status_mapping": {
      "In Progress": "in_progress",
      "In Review": "in_progress"
    },
    "unmapped_status": "low_priority"
  },
  "github": {
    "filters": {
      "mute_repos": ["some-org/noisy-repo"],
      "deprioritize_repos": ["some-org/archive-*"],
      "mute_users": ["dependabot[bot]"],
      "deprioritize_users": ["renovate[bot]"],
      "rules": [
        {
          "name": "Track bot PRs in owned repos",
          "repos": ["some-org/platform-*"],
          "users": ["renovate[bot]", "dependabot[bot]"],
          "action": "deprioritize"
        }
      ]
    }
  }
}
```

`linking_mark_prefixes` is mandatory and lists the identifier prefixes Radar may use to link work across sources, for example `["DPSCAP"]` permits `DPSCAP-722`. Prefixes are normalized to uppercase, must start with a letter, and may contain only letters and numbers. Radar matches only complete `<PREFIX>-<NUMBER>` marks, so unrelated suffixes such as `Origin-096e274f` are ignored.

`obsidian.vault_path` is required for task authoring and must identify an existing vault containing `.obsidian/`; Radar creates its fixed `Tasks/` root. `repository_dirs` controls where `radar create` discovers base repositories. `workspace_root` controls where Radar creates worktrees. When omitted, the workspace root is `$XDG_DATA_HOME/radar/workspaces`, falling back to `~/.local/share/radar/workspaces`. `model` and `thinking` are passed to Pi as `--model` and `--thinking` for new workspace sessions unless the repository's `.radar.json` defines its own values. `jira.authoritative_issue_types` defaults to Task, Bug, and Sub-task; an explicit empty array disables assigned Jira collection and makes automatic title discoveries informational. `datadog.monitor_query` is the user-owned scope for Datadog monitor collection; secrets are accepted only from `RADAR_DATADOG_API_KEY` and `RADAR_DATADOG_APP_KEY`.

Muted tasks are hidden from the TUI and counts. Deprioritized tasks move to the low-priority section. User filters also apply to GitHub comment and review actors: muted or deprioritized actor activity does not promote a PR to attention. Confirmed GitHub bots match both their API login and the equivalent `[bot]` alias, so `gemini-code-assist[bot]` matches the GraphQL login `gemini-code-assist`. Repository and user patterns support `*` wildcards, and rule matches are case-insensitive.

## Local state

The daemon stores rebuildable task records and source-ref observations locally. Task records provide cache-local numeric IDs, lifecycle projection, source-ref ownership, and acknowledgements. Obsidian notes—not this cache—own authored task content and lifecycle.

Radar groups work by linking mark, source-owned identity, and workspace keys. A primary lifecycle ref controls completion when present; otherwise contributing work-item refs retain their combined lifecycle behavior.

Use `./radar reset` to discard collected observations and rebuild them from integrations. Acknowledgements may be retained. An incompatible state version is intentionally discarded and recollected; malformed state still fails closed.

```sh
./radar state-path
```

By default this is `$XDG_STATE_HOME/radar/tasks.json` or `~/.local/state/radar/tasks.json`.

Override it with `RADAR_STATE=/path/to/tasks.json`.

Radar writes state atomically through a single serialized writer. A malformed state file prevents startup. An incompatible `stateVersion` is treated as a disposable cache and rebuilt from source integrations.

## Logs

The daemon writes logs to:

```sh
./radar log-path
```

By default this is `$XDG_STATE_HOME/radar/radar.log` or `~/.local/state/radar/radar.log`.

Follow logs with:

```sh
tail -f "$(./radar log-path)"
```

Override the log path with `RADAR_LOG=/path/to/radar.log`.

Set log level with:

```sh
RADAR_LOG_LEVEL=debug ./radar daemon
```

Supported levels: `debug`, `info`, `warn`, `error`. Default is `info`.
