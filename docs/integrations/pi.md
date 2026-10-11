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

`pi-radar` provides Radar workspace tools and context; `pi-sbx` provides sandbox tool routing. **`pi-sbx` >=0.6.0 is required for early sandboxed launch.** Installing the SBX CLI alone does not install either Pi package.

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

Restart Pi afterwards. `pi update` without a package source updates Pi itself, not these packages. To pin either extension, use `pi install npm:@christianmoesl/pi-radar@<version>` or `pi install npm:@christianmoesl/pi-sbx@<version>`; choose `pi-sbx` >=0.6.0 for early sandboxed launch. Versioned npm sources are pinned and skipped by package updates, so move to a new pinned release by installing its version explicitly. The Radar CLI and `pi-radar` npm package share the same release version (the npm version omits the tag's leading `v`); `pi-sbx` is versioned separately.

For a standard macOS installation, `radar update` updates both installed,
unpinned user-level npm packages automatically, without a separate extension
confirmation—even when the CLI is already current. It preserves unpinned
sources and skips pins, filtered/disabled declarations and custom/project
installations. See [updates](../installation.md#managed-macos-releases).

In an interactive Radar workspace, `pi-radar` checks its locally installed
version every 30 seconds. If it is newer than the version loaded by the session,
a red statusbar notice reads **pi-radar updated · /reload to apply**. Otherwise
nothing is shown. A successful `/reload` clears the notice by loading the new
version. There is no corresponding notice for `pi-sbx`, no automatic reload, and
no idle/subagent detection; choose a safe moment to reload.

## Installation notices

Radar-launched interactive Pi sessions show one combined, non-blocking recommendation per Pi profile for missing packages. It explains each package's benefits and installation command; `/radar-dismiss-install-hint` hides it. The notice never installs packages or changes Pi settings. Install scripts provision required CLI tools; foreground onboarding installs Pi extensions after permission, while `radar update` automatically updates eligible installed unpinned packages without an extra extension confirmation. The notice checks the running Pi's Radar tools and personal/project declarations (including `PI_CODING_AGENT_DIR`), suppresses advice independently for each known configured or explicitly disabled package, and does not appear in RPC/print mode. Having `pi-radar` installed does not hide relevant missing `pi-sbx` advice. That advice requires an effectively sandbox-enabled workspace and a usable SBX CLI, established by bounded read-only probes; absent/disabled/uncertain sandboxing stays quiet. This helper gives installation advice, not version enforcement. Pi runs on the host even for sandboxed workspaces, so install both packages in that host Pi profile. For a custom agent directory, prefix each command with `PI_CODING_AGENT_DIR=/path/to/profile`; the suggested commands include the matching directory. Restart Pi after installation.

All launches load a small **notice-only helper**, not the integration; early sandboxed launches additionally load the required [pi-sbx prerequisite guard](sbx.md#early-pi-launch-and-recovery). Radar caches these helpers under `$XDG_CACHE_HOME/radar/pi/` (or the platform's user cache directory) and records that the notice was shown in `<Pi agent directory>/radar/install-hint-seen`. Removing that marker allows the recommendation to appear again. Existing settings and workspaces are not rewritten; unreadable settings or unavailable notice storage simply skip the advice.
