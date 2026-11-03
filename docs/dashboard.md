# Using the dashboard

[Documentation](README.md) · [Workspaces](workspaces.md) · [Tasks and notes](integrations/obsidian.md)

`radar` opens the dashboard directly in your current terminal, inside or outside tmux. Only opening a workspace activates tmux: outside tmux, Radar attaches to the workspace session and returns to the dashboard when you detach; inside tmux, it switches the current client. The optional **prefix + r** binding opens Radar as a popup when you are already using tmux.

## Optional tmux bindings

```tmux
bind-key r display-popup -E -w 90% -h 90% -d '#{pane_current_path}' radar
bind-key F display-popup -E "radar fork"
```

## Workspaces and Agents

The dashboard starts in **Workspaces**, the existing task list (including source-only work without a workspace). Above it, `[Workspaces]   Agents N · !M` provides two peer views. **N** counts all registered durable-agent instances, including stopped/unhealthy ones; **!M** counts distinct instances requesting attention or reporting runtime, provisioning, collection-delivery or schedule errors. An instance counts once even if it has both attention and an error. Zero attention omits the suffix. Counts remain global while searching either view.

Press **Tab** from either overview to switch directly; arrows and j/k select list rows, not a tab header. Each view keeps its own selection, scroll and applied query for the current session. Tab does not switch while entering search text, inspecting details, editing forms or confirming actions. Enter applies search and returns to navigation; Escape clears search as before. No configuration or cross-session UI preference is saved.

**Agents** shows one row per registration, ordered attention/errors first, then title and registration ID. Running/Stopped is runtime state, not Idle/Busy or business success. Definition-authored attention and one summary line are separate; full content remains in details. `/` searches only local registration ID, title and summary, case-insensitively with every term matching. It never searches logs/conversations or remote services. An empty view says **No durable agents registered**; a filter with no matches says **No agents match your search**.

Registered-agent contributions and proven dedicated backing-only workspace tasks are separated from Workspaces, including its search, Muted/Done sections and category counts. Ordinary explicitly bound workspaces, independent linked source/user work and interactive Pi activity remain visible. Separation uses registration/source identities and validated existing ownership metadata, not names or path prefixes; uncertain ownership leaves ordinary work visible. No tasks or source facts are deleted, completed, muted or unregistered, and cleanup policy is unchanged.

Refreshes preserve selected identity and the active view. Removed registrations select the nearest remaining row; open details instead report unavailability. Failed collection keeps last-known information and source diagnostics, with `?` in navigation (an unknown count is not `0`). The navigation and Tab hint remain available in compact layouts; essential runtime/attention/error state takes precedence over summaries.

## Keyboard shortcuts

| Key | Action |
| --- | --- |
| <kbd>Tab</kbd> | Switch Workspaces / Agents from either overview |
| <kbd>j</kbd> / <kbd>↓</kbd>, <kbd>k</kbd> / <kbd>↑</kbd> | Move within the current list |
| <kbd>Enter</kbd> | Workspaces: open/create the selected session; Agents: open existing agent details |
| <kbd>/</kbd> | Search the current view; <kbd>Esc</kbd> clears only that view's query |
| <kbd>o</kbd> | Open the source action or link |
| <kbd>i</kbd> | Inspect the selected task and its linked sources |
| <kbd>n</kbd> | Create an Obsidian-backed task |
| <kbd>d</kbd> / <kbd>p</kbd> | Complete or reopen a task / toggle urgent priority |
| <kbd>m</kbd> | Mute or unmute the selected task |
| <kbd>D</kbd> | Delete an authored task with confirmation (move to vault trash) |
| <kbd>c</kbd> | Create a workspace |
| <kbd>F</kbd> | [Improve Radar](workspaces.md#improve-radar): create a feedback workspace (requires a live Pi session with the `linear` MCP) |
| <kbd>w</kbd> | Edit the selected task's workspace resources |
| <kbd>x</kbd> / <kbd>X</kbd> | Clean up the selected task / garbage-collect eligible workspaces |
| <kbd>f</kbd> | Edit the configuration |
| <kbd>s</kbd> | Show or hide source diagnostics |
| <kbd>r</kbd> | Refresh sources |
| <kbd>q</kbd> / <kbd>Esc</kbd> | Quit |

## Navigation

Navigation stops at the first and last item rather than wrapping. Repository, branch, fork-member, worktree-session, and workspace-resource lists adapt to terminal height, keep the selection visible when scrolling or resizing, and show a position indicator. Long list labels are displayed on one line; their full values are preserved for actions. In searchable pickers, typing—including `j` and `k`—still filters the list; use ↑/↓ or Ctrl+P/N to move. The dashboard, Inspect, and scrollable confirmations support Page Up/Down or Ctrl+U/D; Home/End jumps to their top/bottom.

## Search tasks

Press `/` to search the dashboard as you type. Matching is case-insensitive; every whitespace-separated term must occur in the task title, repository, or linked-source titles, identifiers, branches, or workspace names/paths. Terms can match different fields. Search uses only the current dashboard task data, not note bodies, remote queries, or older history beyond the existing Done retention.

Use ↑/↓ or Ctrl+P/N to select a result. Letters such as `j`, `k`, `d`, and `q` are text while the search field is focused; Ctrl+C still quits. Enter leaves text entry with the filter applied, so normal task actions work again. Press `/` to edit the query. Inspect and source-link views preserve the filter when you return.

A nonempty query keeps normal grouping and ordering but reveals matching Muted/Done tasks even when their sections were collapsed. These headers are not selectable during filtering. Clearing search restores the previous section expansion choices. Esc clears the query, exits search, and restores the normal list in one press; it does not quit Radar on that press. With no search active, Esc retains its usual quit behavior.

## Reading the task list

The daemon refreshes local sources every 15 seconds and remote sources every two
minutes. Local updates continue while remote services respond. A full refresh
first publishes local data, then merges remote results with the latest local
observations. Overlapping full-refresh requests share the same in-flight work;
each caller waits for it to finish. Tasks are grouped by attention rather than by source; see the
[attention rules](attention-algorithm.md).

The dashboard uses [Catppuccin Mocha](https://catppuccin.com/palette/) colors while keeping your terminal background. Tasks remain a flowing list with inline metadata and resource badges. A blank line separates tasks without separating their source references. The task area fills the available popup height, keeping Sources at the bottom even when the list is short. On roomy terminals, shortcuts appear in a fixed 32-column right-hand rail: action labels on the left, keys aligned on the right, grouped into **Navigate**, **Selected task** (or **Selected section**), and **Global**. Aliases sit below the main commands. Unavailable actions leave their row slots empty, so selection changes and background operations do not move the other shortcuts. The rail does not consume task-list rows. If there is not enough width for the rail plus a useful task list, or enough height for every shortcut, Radar retains the compact footer instead; no keys or extra help mode are added. Sources starts collapsed to a one-line health summary; failures and other non-healthy states remain visible, with disabled integrations counted separately. Press `s` to show or hide the full source diagnostics without changing the selected task. The list uses the reclaimed rows, and refreshes preserve your choice for the current dashboard session. Outer padding shrinks on smaller terminals, and the footer wraps between shortcuts so every action stays visible.

### Muted and completed tasks

Muted and Done have selectable headers and start collapsed. Move to a header with `j`/`k` or `↓`/`↑`, then press `Enter` to expand or collapse it. Counts remain visible; hidden tasks and their source refs are skipped by navigation. Each section remembers its choice during the current dashboard session, including refreshes. Empty sections are omitted. Active section headers remain non-selectable.

When the selected task becomes muted or done, the overview stays among active tasks: it selects the next task at that position, or the previous task when handling the last active one. If no active tasks remain, selection moves to a visible section header without expanding it. This applies to successful mutations and background updates; delayed responses do not steal focus after you navigate elsewhere. Unmuting unfinished work, reopening, and priority changes follow the selected task, and Inspect stays on the task being inspected. Done remains sorted by completion time, newest first; equal timestamps keep their existing order, and tasks without a valid completion time appear last. Collapsing does not change its three-day display retention or unresolved-workspace exception.

### Agent results awaiting review

A task whose completion was held by agent work, or which was reopened by resumed
agent work, stays unfinished. While agents run it shows the normal busy/waiting
badge. Once they settle it appears in Attention with **Agent finished — review
result**. Completed source rows retain their merged/resolved status, so source
completion is visible without hiding the handoff. Urgency and task mute still apply.

Inspect or open the workspace to review the result, then press `d` to mark Done.
Opening it does not itself acknowledge the result. There is no timeout or separate
review action. Done reports a clear error while agents are busy/waiting or activity
cannot be verified; a failed pending note write must be resolved and refreshed first.

### Open a source

The `o` view lists every source action and link. Move with `j`/`k` or `↓`/`↑` and press `Enter` to open the selection; the list scrolls to keep it visible. Displayed letter/digit shortcuts open entries directly, reserving `j`, `k`, and `q` for navigation and quitting. Entries without an available shortcut leave that column blank and remain selectable. Press `Esc` or `Backspace` to return.

## Resource badges

The task row shows configured sandbox host ports beside `🔌`, for example `🔌 5173, 8080`, and Obsidian note counts such as `📝 1`. Empty badges are omitted. Git worktree, Docker SBX and tmux counts are not shown in the overview; their resources, management actions and Inspect details are unchanged. A `⚠️ unresolved` warning still identifies local-resource issues that prevent automatic workspace cleanup, including uncommitted changes and other safety-check failures. Long port lists and task text are truncated to fit the row while retaining note counts and the warning.

Inspect (`i`) retains every configured host → sandbox mapping, including differing ports. Open (`o`) includes an `http://localhost:<host-port>` link for each forwarding, opened in the host's default browser. For host `8080` → sandbox `3000`, the URL is `http://localhost:8080`. Normal workspace/dashboard refreshes update the badge, Inspect and generated Open links. No ports or links appear without configured forwards. These entries describe configuration only: Radar does not scan ports, probe HTTP services, infer HTTPS or paths, or change forwarding configuration.

Local resource references no longer occupy separate overview rows. The logical `workspace` reference also remains hidden from the overview and does not add a badge. Other references remain underneath the task. Press `i` to inspect resource names, paths, and per-worktree status such as `2 dirty, ahead 1`. Resource actions, the task's activity indicator, and task priority are unchanged.

## Progress and errors

Workspace inspection, preparation, creation/updates, cleanup checks and execution, and session creation show an animated spinner in a fixed slot beside the affected task, with a short operation label. You can navigate and inspect tasks while work runs; other actions wait until it finishes. Pressing Enter on a task during cleanup or its resource check shows **Cleanup in progress — Retry Enter when finished**, including for an unrelated workspace. The request is not queued: press Enter again after cleanup succeeds or fails. Switching resumes when the cleanup response arrives, even if the resource list is still refreshing; failed cleanup remains available in the initiating task's Inspect view. Confirmation and editing screens do not spin while waiting for input. Workspace creation without an existing task shows progress inline until the new task is available.

Failures stay on the task row with an `!` marker and an `i inspect` hint. Inspect shows the full explanation, including any recovery guidance supplied by the operation. Errors remain for the current TUI session across navigation and live refreshes. Starting the same operation again replaces the marker with the spinner but retains the previous explanation until success; another failure replaces it. Successful unrelated operations do not dismiss the error. Routine completion toasts are omitted, but workspace warnings and resource-reload guidance remain visible. Global refresh, garbage collection, and repository/branch picker loading are unchanged.

## Inspect

Use [cleanup and expiry guidance](workspace-cleanup.md) to resolve warnings.

Inspect is a read-only view of the selected task. Task details (including configured sandbox port forwards and metadata) come first, followed by **Workspace expiry** when the done task has a valid completion time and registered workspace, then **Unresolved** with affected resources and reasons, and **Source refs**. Expiry shows a timezone-labelled date/time, past eligibility, and a permanent-loss warning; it does not run cleanup checks, fetch remotes, or guarantee deletion at that instant. The expiry section disappears on reopening or when no registered workspace remains. The **Unresolved** section is omitted when there are no cleanup issues. Use `j`/`k`, arrow keys, or Ctrl+N/P to scroll; Page Up/Down or Ctrl+U/D to page; and `g`/`G` or Home/End to jump to the top/bottom. Long paths and URLs wrap, while the task title, line position, and shortcuts stay visible. Scrolling never selects another task or switches workspaces. Live updates retain the inspected task; if it disappears, inspect shows “Task no longer available” rather than another task. Escape or Backspace returns to the overview, where Enter and task/workspace actions remain available. `q` or Ctrl+C quits.

## Durable agents

Switch to **Agents** with Tab. Enter opens the existing agent's summary, runtime health, associated workspace, declared actions and optional conversations/messages. Use `s` to read the full summary; arrows navigate or scroll, `r` refreshes, and Escape returns to Agents with its list context intact. Start/stop/restart, schedules and logs remain available. **Open associated workspace**, **Inspect associated workspace** and **Workspace resources** retain the existing workspace opening, inspection and reconciliation flows; missing resources report their existing error/recovery information. CLI workspace inspection/cleanup and its ownership/lease protections are unchanged.

Done, Mute, task priority and task cleanup are not agent-row operations; the overview help shows only applicable and global commands. Viewing details, switching views or opening a workspace does not start an agent, clear attention, acknowledge a report or complete a task. Existing action revision, confirmation and ambiguous-delivery retry protections remain unchanged. Actions that changed while you were responding require a fresh decision. See [durable agents](durable-agents.md) for authoring and event delivery.
