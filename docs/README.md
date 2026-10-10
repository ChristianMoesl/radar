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
- [CLI reference](cli.md) — commands, JSON output, state and logs.
- [Workspace resource API](workspace-reconciliation.md) — inspect and reconcile worktrees, mounts and ports.
- [Attention rules](attention-algorithm.md) — why a task appears and how linked signals are ranked.

## Integrations

| Tool | Guide |
| --- | --- |
| GitHub | [Pull requests, reviews, tracking and filters](integrations/github.md) |
| Jira | [Assigned issues, ticket linking and workflow states](integrations/jira.md) |
| Datadog | [Scoped monitor alerts](integrations/datadog.md) |
| Markdown / Obsidian | [Task notes and lifecycle](integrations/obsidian.md) |
| Git | [Worktrees and branches](integrations/git.md) |
| tmux | [Sessions, pane layouts and agent activity](integrations/tmux.md) |
| Pi | [Installation, workspace tools and session behavior](integrations/pi.md) |
| Docker SBX | [Sandbox configuration, startup and recovery](integrations/sbx.md) |

## Development and releases

- [Contributing](../CONTRIBUTING.md) — build, test and develop locally.
- [Architecture](../ARCHITECTURE.md) — components, task model and process boundaries.
- [Integration development](integrations.md) — provider contracts and adding a source.
- [Release publishing and recovery](releases.md) — signed releases, component versions and updater safety.
- [npm publishing](npm-publishing.md) — trusted staging and human approval.
- [Sandbox image and kit](../sandbox/README.md) — tools, startup hooks and image releases.
