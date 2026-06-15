# CLI-first migration

Radar is moving from a Neovim-plugin-first project to a CLI-first tool.

The Neovim plugin has been removed. New product work targets the Go binary, TUI, CLI commands, and daemon.

## Motivation

Radar should be usable from the developer workflow wherever that workflow happens:

- a plain terminal
- tmux
- Neovim
- scripts and automation

Requiring an active Neovim session to create, inspect, or switch work sessions makes Radar less useful as the workflow moves outside Neovim. The CLI should become the primary interface, with tmux integration as the first-class interactive workflow.

## Direction

- `radar` remains a single Go binary.
- The daemon remains part of that binary.
- The default interactive experience should become a terminal UI.
- Non-interactive commands should remain scriptable.
- tmux integration should use a simple tmux binding to open Radar in a floating popup.
- Radar should absorb the useful workflow functionality from [`fork.nvim`](https://github.com/ChristianMoesl/fork.nvim) into the CLI/TUI experience.
- The Neovim plugin has been removed; do not reintroduce editor-specific product logic.

Example target shape:

```sh
radar                  # open interactive TUI
radar daemon           # run daemon
radar status           # scriptable status summary
radar tasks            # scriptable task list
radar refresh          # refresh daemon state
```

## Binary and process model

The CLI, TUI, and daemon should live in the same Go binary unless a concrete reason to split them appears.

Benefits:

- one installable artifact
- simple tmux bindings
- simple scripting
- shared configuration and state paths
- no coordination between separate `radar`, `radard`, and `radar-tui` binaries

The important boundary is architectural, not binary-level: the TUI must not own domain logic. It should call the same application/service layer used by scriptable CLI commands and the daemon.

## TUI stack

Use Go-native TUI libraries. The preferred stack is the Charm ecosystem:

- Bubble Tea for the application model/update/view loop
- Lip Gloss for styling and layout
- Bubbles for reusable widgets
- Huh for forms/prompts when useful
- Glamour for Markdown rendering if needed

Other libraries can be reconsidered later, but Bubble Tea/Lip Gloss is the default direction for a polished terminal UI.

## fork.nvim functionality

Radar should include the useful functionality from [`fork.nvim`](https://github.com/ChristianMoesl/fork.nvim) as part of the CLI-first product direction.

The goal is not to maintain another Neovim plugin, but to bring that workflow into the standalone Radar CLI/TUI so it works from tmux, terminals, scripts, and any future editor integration.

The exact feature mapping should be designed when this work starts, but the target is:

- project/worktree/session creation from the CLI/TUI
- quick selection and switching between active work contexts
- tmux-friendly workflows for opening or attaching to work
- scriptable commands for automation, starting with `radar create --repo <repo> --base <branch> --name <name>` and `radar delete --path <workspace-path>`
- no new dependency on Neovim as the primary interface

## tmux integration

The first tmux integration can be intentionally small:

```sh
tmux display-popup -E "radar"
```

The tmux integration should call the CLI/TUI. It should not become a separate source of domain logic.

## Editor integrations

The old Neovim plugin has been removed.

Rules for future editor integrations:

- do not put product/domain logic in an editor plugin
- integrate through the CLI or daemon API
- keep Radar usable from terminals, tmux, and scripts without an editor

## Migration steps

1. Document the CLI-first direction. Done.
2. Keep daemon and scriptable CLI commands working. Done.
3. Introduce a TUI package in Go that uses the existing client/service boundaries. Done.
4. Make `radar` without subcommands open the TUI. Done.
5. Document a simple tmux popup binding. Done.
6. Fold the useful `fork.nvim` workflow into Radar's CLI/TUI model. In progress: create is implemented; delete remains CLI-only.
7. Expand the TUI around session/task creation, switching, filtering, and inspection. In progress: create, switching, filters, reset, and inspection are implemented.
8. Update README examples to present Radar as a CLI-first tool. Done.
