# Radar documentation

New to Radar? Start with the [quick start](../README.md#get-started).

Offline: use `man radar` and `man radar-config`, or [ask Pi about Radar](integrations/pi.md#ask-pi-about-radar) in a workspace.

## Getting started and everyday use

- [Installation and setup](installation.md) — macOS installer, manual platforms, prerequisites and consent.
- [Notifications](installation.md#macos-notification-setup) — launch approval, permissions and testing.
- [Updates](installation.md#managed-macos-releases) — macOS updates and manual installation paths.
- [Dashboard](dashboard.md) — shortcuts, source status, task details and progress.
- [Workspaces](workspaces.md) — notes, repositories, session layout, setup commands and forks.
- [Tasks and notes](integrations/obsidian.md) — create, prioritize, mute, complete, reopen or delete tasks.
- [Cleanup and expiry](workspace-cleanup.md) — preserve valuable work before removing local resources.

## Configuration and automation

- [Configuration](configuration.md) — directories, linking, defaults and repository overrides.
- [Task-state migration and backup](sqlite-state-migration.md) — automatic SQLite cutover, recovery and content-only notes.
- [CLI reference](cli.md) — commands, JSON output, state and logs.
- [Workspace resource API](workspace-reconciliation.md) — inspect and reconcile worktrees, mounts and ports.
- [Durable agents](durable-agents.md) — code-defined pi-durable agents, events, interaction and workspace/SBX execution.
- [Attention rules](attention-algorithm.md) — why a task appears and how linked signals are ranked.

## Integrations

| Tool | Guide |
| --- | --- |
| GitHub | [Pull requests, reviews, tracking and filters](integrations/github.md) |
| Terraform | [Personal runs, plan summaries and confirmation](integrations/terraform.md) |
| Jira | [Assigned issues, ticket linking and workflow states](integrations/jira.md) |
| Datadog | [Scoped monitor alerts](integrations/datadog.md) |
| Markdown / Obsidian | [Task notes and lifecycle](integrations/obsidian.md) |
| Git | [Worktrees and branches](integrations/git.md) |
| tmux | [Sessions, pane layouts and agent activity](integrations/tmux.md) |
| Pi | [Installation, workspace tools and session behavior](integrations/pi.md) |
| Docker SBX | [Local kits, credentials, custom images, startup scripts and recovery](integrations/sbx.md) |

## Development and releases

- [Contributing](../CONTRIBUTING.md) — build, test and develop locally.
- [Architecture](../ARCHITECTURE.md) — components, task model and process boundaries.
- [Integration development](integrations.md) — provider contracts and adding a source.
- [Live provider E2E](live-e2e.md) — hosted CI with real Jira, GitHub and Datadog environments and encrypted OAuth sessions.
- [Release publishing and recovery](releases.md) — signed releases, component versions and updater safety.
- [npm publishing](npm-publishing.md) — trusted staging and human approval.
- [Sandbox image and kit](../sandbox/README.md) — tools, startup hooks and image releases.
