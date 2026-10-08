# Radar

**See what needs your attention. Get back to work.**

Radar brings your issues, pull requests, notes and local work into one terminal
dashboard. Open a task to jump into its workspace—with Git worktrees, tmux and
[Pi](https://pi.dev) ready to go.

![Radar dashboard showing tasks grouped by attention](docs/images/radar-tui.png)

## Get started

### 1. Install

On **macOS**, run this in a terminal without sudo:

```sh
curl -fsSL https://raw.githubusercontent.com/ChristianMoesl/radar/main/install.sh | bash
```

The installer verifies the release and starts guided setup. It asks before
installing Homebrew or Node.js and before changing your PATH. You can decline
any offer; declining a required dependency stops installation.

[Linux, Windows/WSL and manual installation →](docs/installation.md#manual-installation)

### 2. Make it yours

Setup walks you through the tools you need and asks you to:

- Choose where your repositories, workspaces and task notes live. **Obsidian is optional.**
- Connect GitHub, Jira or Datadog—or skip them and start with local work.
- Review your settings before saving. You can change them later with `radar setup`.

On macOS, follow the optional [notification approval steps](docs/installation.md#macos-notification-setup).
After setup, open a new terminal and run **`radar`** whenever you want your dashboard.

### 3. Open your first workspace

Press **`c`**, give the workspace a name, and optionally add a repository.
Press **Enter** to create it and open Pi. You can start with just a note and add
repositories later with **`w`**. If Pi needs model access, use **`/login`** in Pi.

Back in the dashboard, use **↑/↓** to choose a task, **Enter** to resume its
workspace, **`i`** to inspect it, or **`o`** to open its issue, PR or note.

Workspaces are disposable: completed ones are automatically cleaned up, and
can lose uncommitted work after eight days. Read the [cleanup policy](docs/workspace-cleanup.md).

## Learn more

| I want to… | Guide |
| --- | --- |
| Finish setup, fix notifications or update Radar | [Installation and updates](docs/installation.md) |
| Learn the dashboard and keyboard shortcuts | [Using Radar](docs/dashboard.md) |
| Create workspaces, add repositories or configure setup commands | [Workspaces](docs/workspaces.md) |
| Connect tools or customize settings | [Configuration](docs/configuration.md) · [Integrations](docs/README.md#integrations) |
| Use the CLI, inspect state or find logs | [Command-line reference](docs/cli.md) |

[All documentation](docs/README.md) · [Contributing](CONTRIBUTING.md) · [MIT License](LICENSE)
