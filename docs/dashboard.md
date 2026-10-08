# Using the dashboard

[Documentation](README.md) · [Workspaces](workspaces.md) · [Tasks and notes](integrations/obsidian.md)

`radar` opens the dashboard directly in your current terminal, inside or outside tmux. Only opening a workspace activates tmux: outside tmux, Radar attaches to the workspace session and returns to the dashboard when you detach; inside tmux, it switches the current client. The optional **prefix + r** binding opens Radar as a popup when you are already using tmux.

## Optional tmux bindings

```tmux
bind-key r display-popup -E -w 90% -h 90% -d '#{pane_current_path}' radar
bind-key F display-popup -E "radar fork"
```

## Keyboard shortcuts

| Key | Action |
| --- | --- |
| <kbd>j</kbd> / <kbd>↓</kbd>, <kbd>k</kbd> / <kbd>↑</kbd> | Move between tasks |
| <kbd>Enter</kbd> | Switch to or create the selected task's tmux session |
| <kbd>o</kbd> | Open the source action or link |
| <kbd>i</kbd> | Inspect the selected task and its linked sources |
| <kbd>n</kbd> | Create an Obsidian-backed task |
| <kbd>d</kbd> / <kbd>p</kbd> | Complete or reopen a task / toggle urgent priority |
| <kbd>m</kbd> | Mute or unmute the selected task |
| <kbd>D</kbd> | Delete an authored task with confirmation (move to vault trash) |
| <kbd>c</kbd> | Create a workspace |
| <kbd>w</kbd> | Edit the selected task's workspace resources |
| <kbd>x</kbd> / <kbd>X</kbd> | Clean up the selected task / garbage-collect eligible workspaces |
| <kbd>f</kbd> | Edit the configuration |
| <kbd>s</kbd> | Show or hide source diagnostics |
| <kbd>r</kbd> | Refresh sources |
| <kbd>q</kbd> / <kbd>Esc</kbd> | Quit |

## Navigation

Navigation stops at the first and last item rather than wrapping. Repository, branch, fork-member, worktree-session, and workspace-resource lists adapt to terminal height, keep the selection visible when scrolling or resizing, and show a position indicator. Long list labels are displayed on one line; their full values are preserved for actions. In searchable pickers, typing—including `j` and `k`—still filters the list; use ↑/↓ or Ctrl+P/N to move. The dashboard, Inspect, and scrollable confirmations support Page Up/Down or Ctrl+U/D; Home/End jumps to their top/bottom.

## Reading the task list

The daemon refreshes local sources every 15 seconds and remote sources every two
minutes. Tasks are grouped by attention rather than by source; see the
[attention rules](attention-algorithm.md).

The dashboard uses [Catppuccin Mocha](https://catppuccin.com/palette/) colors while keeping your terminal background. Tasks remain a flowing list with inline metadata and resource badges. A blank line separates tasks without separating their source references. The task area fills the available popup height, keeping Sources at the bottom even when the list is short. On roomy terminals, shortcuts appear in a fixed 32-column right-hand rail: action labels on the left, keys aligned on the right, grouped into **Navigate**, **Selected task** (or **Selected section**), and **Global**. Aliases sit below the main commands. Unavailable actions leave their row slots empty, so selection changes and background operations do not move the other shortcuts. The rail does not consume task-list rows. If there is not enough width for the rail plus a useful task list, or enough height for every shortcut, Radar retains the compact footer instead; no keys or extra help mode are added. Sources starts collapsed to a one-line health summary; failures and other non-healthy states remain visible, with disabled integrations counted separately. Press `s` to show or hide the full source diagnostics without changing the selected task. The list uses the reclaimed rows, and refreshes preserve your choice for the current dashboard session. Outer padding shrinks on smaller terminals, and the footer wraps between shortcuts so every action stays visible.

### Muted and completed tasks

Muted and Done have selectable headers and start collapsed. Move to a header with `j`/`k` or `↓`/`↑`, then press `Enter` to expand or collapse it. Counts remain visible; hidden tasks and their source refs are skipped by navigation. Each section remembers its choice during the current dashboard session, including refreshes. Empty sections are omitted. Active section headers remain non-selectable.

When the selected task becomes muted or done, the overview stays among active tasks: it selects the next task at that position, or the previous task when handling the last active one. If no active tasks remain, selection moves to a visible section header without expanding it. This applies to successful mutations and background updates; delayed responses do not steal focus after you navigate elsewhere. Unmuting unfinished work, reopening, and priority changes follow the selected task, and Inspect stays on the task being inspected. Done remains sorted by completion time, newest first; equal timestamps keep their existing order, and tasks without a valid completion time appear last. Collapsing does not change its three-day display retention or unresolved-workspace exception.

### Open a source

The `o` view lists every source action and link. Move with `j`/`k` or `↓`/`↑` and press `Enter` to open the selection; the list scrolls to keep it visible. Displayed letter/digit shortcuts open entries directly, reserving `j`, `k`, and `q` for navigation and quitting. Entries without an available shortcut leave that column blank and remain selectable. Press `Esc` or `Backspace` to return.

## Resource badges

The TUI summarizes local resources on each task row with emoji and counts: `🌿 2` for Git worktrees, `🐳 1` for Docker SBX sandboxes, `📟 1` for tmux sessions, and `📝 1` for Obsidian notes. These use the same emoji style as the attention indicators and do not require a Nerd Font. Zero counts are omitted. A `⚠️ unresolved` warning appears when linked local resources have issues that prevent automatic workspace cleanup, including uncommitted changes and other safety-check failures. Counts and the warning remain visible when long task text is truncated.

Local resource references no longer occupy separate overview rows. The logical `workspace` reference is also hidden from the overview, without adding a badge or increasing worktree counts. Other references remain underneath the task. Press `i` to inspect resource names, paths, and per-worktree status such as `2 dirty, ahead 1`. Resource actions, the task's activity indicator, and task priority are unchanged.

## Progress and errors

Workspace inspection, preparation, creation/updates, cleanup checks and execution, and session creation show an animated spinner in a fixed slot beside the affected task, with a short operation label. You can navigate and inspect tasks while work runs; other actions wait until it finishes. Confirmation and editing screens do not spin while waiting for input. Workspace creation without an existing task shows progress inline until the new task is available.

Failures stay on the task row with an `!` marker and an `i inspect` hint. Inspect shows the full explanation, including any recovery guidance supplied by the operation. Errors remain for the current TUI session across navigation and live refreshes. Starting the same operation again replaces the marker with the spinner but retains the previous explanation until success; another failure replaces it. Successful unrelated operations do not dismiss the error. Routine completion toasts are omitted, but workspace warnings and resource-reload guidance remain visible. Global refresh, garbage collection, and repository/branch picker loading are unchanged.

## Inspect

Use [cleanup and expiry guidance](workspace-cleanup.md) to resolve warnings.

Inspect is a read-only view of the selected task. Task details (including metadata) come first, followed by **Workspace expiry** when the done task has a valid completion time and registered workspace, then **Unresolved** with affected resources and reasons, and **Source refs**. Expiry shows a timezone-labelled date/time, past eligibility, and a permanent-loss warning; it does not run cleanup checks, fetch remotes, or guarantee deletion at that instant. The expiry section disappears on reopening or when no registered workspace remains. The **Unresolved** section is omitted when there are no cleanup issues. Use `j`/`k`, arrow keys, or Ctrl+N/P to scroll; Page Up/Down or Ctrl+U/D to page; and `g`/`G` or Home/End to jump to the top/bottom. Long paths and URLs wrap, while the task title, line position, and shortcuts stay visible. Scrolling never selects another task or switches workspaces. Live updates retain the inspected task; if it disappears, inspect shows “Task no longer available” rather than another task. Escape or Backspace returns to the overview, where Enter and task/workspace actions remain available. `q` or Ctrl+C quits.
