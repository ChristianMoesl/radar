# Installation defaults and setup requirements

Radar's dashboard starts with the generated configuration even if no external
tools are installed. Missing sources do not hide healthy ones. Configuration is
created on first launch, not tailored to whichever tools happened to be present
at install time.

## Optional integrations

`github`, `jira`, `datadog`, and `sbx` accept an optional `enabled` boolean:

| Setting | Behavior |
| --- | --- |
| Omitted | Detect prerequisites at runtime; missing prerequisites mean disabled |
| `false` | Disabled by config |
| `true` | Require prerequisites; missing prerequisites are errors |

No `auto` string or separate activation switch is needed. A detected integration
that fails during collection reports an error instead of silently disabling
itself. Disabled/unavailable sources do not run background reconciliation.
Explicit source actions and cleanup are separate from background collection.

| Integration | Automatic activation prerequisites |
| --- | --- |
| GitHub | `gh` on PATH and a locally configured GitHub token; API failures, including expired credentials, are errors |
| Jira | Endpoint, email and token in the documented `RADAR_JIRA_*` variables |
| Datadog | Both credentials and an explicit `datadog.monitor_query`; no automatic organization-wide query |
| SBX collection | `sbx` on PATH, or Windows `sbx.exe` plus `wslpath` on WSL2 |
| SBX for new workspaces | macOS and `sbx` on PATH; Windows SBX managed workspaces remain blocked by WSL symlink support |

Git worktrees and tmux sessions are discovered when their CLIs are available.
The macOS notifier is used when its companion is installed. None of these is
required just to open the dashboard. Tools installed later on PATH are detected
on subsequent checks, without rewriting the config. If the daemon's PATH itself
changes, restart the daemon to give it the updated environment.

Dashboard startup recovers reported SBX authentication failures by launching
`sbx login` (`sbx.exe login` for Windows SBX from WSL2) before opening the TUI,
then refreshing local sources. `radar create` and `radar fork` also check SBX
authentication in the foreground. Background collection never opens login
prompts, and unrelated runtime failures do not trigger login. Authenticate
GitHub manually with `gh auth login`. SBX runtime failures never cause a
sandboxed setup command to execute on the host instead.

## Repository precedence and existing workspaces

For new workspace sandboxing:

**Explicit repository setting → explicit global setting → automatic detection.**

A repository can opt in despite a global `false`, or opt out despite a global
`true`. Repo configuration applies to the initial repository used to create the
workspace. Additional members do not change an established workspace's runtime.
Kit overrides and additive mount configuration retain their existing semantics.

Changing defaults never changes an existing registered workspace's runtime or
kit. With global SBX disabled, Radar still observes registered sandboxes (including
repository opt-ins), but not unrelated sandboxes. Registered resources can still
be explicitly cleaned up. CLI cleanup of sandbox targets also checks SBX
authentication in the foreground.

## Required only when using a workflow

- Task authoring and every new workspace require an explicitly configured,
  existing Obsidian vault containing `.obsidian/`. Radar does not guess a vault.
- Workspace sessions require tmux. New sessions using the standard `pi` and
  `nvim` pane commands check these tools before provisioning. Unused pane tools
  are not required; arbitrary custom shell commands remain the user's responsibility.
- Git members require Git. Repository discovery requires `fd`.
- Pi's Radar tools/activity require the separately installed `pi-radar` package;
  sandbox tool routing requires `pi-sbx`. Installing the SBX CLI does not install
  those extensions.
- URL actions require the platform opener (`xdg-open` on Linux, `open` on macOS).

`linking_mark_prefixes` defaults to `[]`. This disables only ticket-prefix
linking, not source identity, branch, or workspace linking. GitHub's generated
tracked-PR rules also default to an empty list, not example repository searches.

## Upgrading and data handling

Installers preserve existing configuration and agent instructions. Existing
`enabled` booleans retain their explicit meaning, including `sbx.enabled: false`
written by older installers. Remove an explicit setting manually to adopt
automatic activation; Radar cannot distinguish an intentional opt-out from an
old generated default. Explicit kit selections are preserved too.

The optional fields and omitted automatic defaults do not invalidate existing
configuration JSON. No config migration, registry migration, note rewrite, or
cache reset is needed for this change. Workspace records, notes, and task-cache
schemas are unchanged. Restart a running daemon after updating the binary.

## Regression coverage

`make test` includes:

- Actual release installer and source-install recipe, isolated HOME/XDG paths,
  default/custom prefixes, custom binary directories, and paths with spaces.
- First launch of the installed daemon with no optional tools or credentials.
- Installer reruns preserving configuration and instructions byte for byte.
- Auto/on/off activation with absent, partial and available prerequisites.
- Newly installed tools detected without changing generated configuration.
- Linux/macOS sandbox defaults and every global/repository enablement combination.
- WSL executable selection and Windows path normalization without config changes;
  unsupported managed workspaces rejected before provisioning.
- Missing workspace tools/vaults failing before resource provisioning.
- Existing workspace runtime preservation and explicit cleanup while disabled.
- Background source failures remaining errors without launching authentication or
  suppressing healthy sources; skipped sources never running reconciliation.
- Foreground SBX login recovery, macOS/WSL2 executable selection and login streams,
  with no login for healthy sessions or unrelated errors.

Tests use fake CLIs and local fixtures, not real authentication, remote APIs, or
sandbox provisioning. CI also builds native macOS notifier/release archives;
real SBX runtime tests remain separately opt-in.
