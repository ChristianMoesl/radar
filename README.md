# Radar

**See what needs your attention. Get back to work.**

Radar brings your issues, pull requests, notes and local work into one terminal
dashboard. Open a task to jump into its workspace—with Git worktrees, tmux and
[Pi](https://pi.dev) ready to go.

![Radar dashboard showing tasks grouped by attention](docs/images/radar-tui.png)

## Get started

### 1. Install

On **macOS**, choose Homebrew or the standalone installer. Run without sudo.

**Homebrew** (Apple Silicon or Intel):

```sh
brew install ChristianMoesl/tap/radar
radar
```

Homebrew installs Radar, its notification companion, fd, tmux and gh. Git is
expected on PATH; install [Pi](https://pi.dev) yourself with Node.js 24+ and npm.
`radar` automatically starts setup on first launch or when a required tool is
missing. Setup checks prerequisites and asks before changing configuration or
installing Pi extensions. Update with `brew upgrade radar`, not `radar update`.
[Homebrew setup and updates →](docs/installation.md#homebrew)

**Standalone installer:**

```sh
curl -fsSL https://raw.githubusercontent.com/ChristianMoesl/radar/main/install.sh | bash
```

The installer verifies the release, installs required CLI tools (including Pi,
fd and gh), and starts guided setup. Installation prompts default to Yes but
still ask permission; PATH changes have a separate confirmation. You can decline
any offer; declining a required dependency stops installation.

[Linux, Windows/WSL and manual installation →](docs/installation.md#manual-installation)

### 2. Make it yours

Setup walks you through the tools you need and asks you to:

- Choose where your repositories, workspaces and task notes live. **Obsidian is optional.**
- Connect GitHub, Jira or Datadog—or skip them and start with local work.
- Install the required `pi-radar` extension.
- Review each file and directory change before saving. You can change them later with `radar setup`.

On macOS, follow the optional [notification approval steps](docs/installation.md#macos-notification-setup).
After setup, open a new terminal and run **`radar`** whenever you want your dashboard.
Inside a workspace with the generated tmux config, press **Ctrl+B**, release, then **R**.
Pi offers a short introduction once; choosing No means it will not ask again.

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


### Offline help

Use `man radar` and `man radar-config` for the installed reference. Inside a
Radar workspace, you can also ask Pi “What can Radar do?” or “How do I configure
my workspaces?”. The Radar extension reads documentation embedded in your
installed CLI. See [manual discovery](docs/installation.md#manual-pages) and
[Pi documentation access](docs/integrations/pi.md#ask-pi-about-radar).
