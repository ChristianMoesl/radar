# CLI-first migration

Radar is moving from a Neovim-plugin-first project to a CLI-first tool.

The existing Neovim plugin stays in the repository for now, but it is legacy code. New product work should target the Go binary and daemon, not the Lua plugin.

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
- tmux integration should open Radar in a floating popup and provide shortcuts for quick access.
- The Neovim plugin is legacy and should not receive new features or refactors unless explicitly requested.

Example target shape:

```sh
radar                  # open interactive TUI
radar daemon           # run daemon
radar status           # scriptable status summary
radar tasks            # scriptable task list
radar refresh          # refresh daemon state
radar tmux popup       # open Radar in a tmux popup
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

## tmux integration

The first tmux integration can be intentionally small:

```sh
tmux display-popup -E "radar"
```

Future commands may provide installable bindings or helper commands, for example:

```sh
radar tmux popup
radar tmux bind-key
```

The tmux integration should call the CLI/TUI. It should not become a separate source of domain logic.

## Neovim plugin status

`lua/radar/` is legacy code.

Rules for the migration:

- do not add new Neovim features
- do not refactor the Lua plugin as part of CLI/TUI work
- do not preserve compatibility with the plugin when changing new CLI/domain concepts unless explicitly requested
- leave the plugin in place for now as a historical/reference frontend

If Neovim support is revisited later, it should integrate with the CLI instead of becoming the primary implementation again.

## Migration steps

1. Document the CLI-first direction and legacy plugin boundary.
2. Keep daemon and scriptable CLI commands working.
3. Introduce a TUI package in Go that uses the existing client/service boundaries.
4. Make `radar` without subcommands open the TUI.
5. Add a minimal tmux popup command.
6. Expand the TUI around session/task creation, switching, filtering, and inspection.
7. Update README examples to present Radar as a CLI-first tool.
