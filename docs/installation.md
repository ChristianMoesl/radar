# Installation defaults and setup requirements

## Guided first startup

Run `radar` in an interactive terminal, or `radar setup` to complete setup without
opening the dashboard. `radar setup` is repeatable: existing settings prefill the
wizard and are updated only after review and affirmative save confirmation.
Automatic setup on bare `radar` runs only when `config.json` is missing. Empty or
malformed config files must be repaired before setup; they are never silently reset.
`radar config-path`, `version`, and background daemon startup do not generate a
config. A headless daemon can still collect with in-memory defaults; it never
prompts or installs anything.

The inline prompts stay in terminal scrollback. Escape/Ctrl+C cancels; yes/no
installation/save prompts default to **no**; existing integration choices are preselected on repeat setup. Secrets are masked while typing and in answered prompts.

1. Check Git, tmux 3.2+, fd (`fdfind` on Debian), Node.js 24+, npm, Pi 0.85.1+,
   pi-radar, and gh. Node.js is a **Pi runtime prerequisite**, not a dependency
   of Radar's Go binary. Neovim is not required: the generated layout has one
   Pi window, without altering existing user layouts.
2. Show each necessary installation command and ask permission. Homebrew and
   apt-get installs are supported; Pi uses npm and pi-radar uses `pi install`.
   No downloaded bootstrap shell is run. Install/update failures, declining,
   or binaries still missing/too old on PATH stop setup. Linux distributions
   whose apt repositories do not provide Node 24+ need Node installed separately.
   No sudo npm install or silent PATH/shell-profile changes are made. If SBX is
   already installed on macOS, setup also checks pi-sbx 0.6.0+ so automatic
   sandbox activation does not produce an unusable early Pi session. Existing
   Git/local package sources are never silently replaced by duplicate npm installs.
3. Offer the tmux configuration described below.
4. Ask for the existing repository directory, workspace root, and notes parent
   directory. Notes live under its `Tasks/` folder. An Obsidian vault is optional.
5. Ask whether to connect GitHub, Jira, and Datadog. Declining writes an explicit
   `enabled: false`; opting in verifies access and writes `true`. GitHub uses
   `gh auth status` and offers `gh auth login` when needed. Jira uses site URL,
   email, hidden API token, automatic Cloud ID discovery, and comma/space-separated
   ticket prefixes. Datadog uses a supported site/API endpoint, hidden API and
   application keys, and a required monitor query such as `tag:team:platform`.
6. Preview the full config, any tmux additions, and the secret destination (never
   token values). Only affirmative confirmation saves the files and directories.

Tool installations and GitHub login happen with their own earlier consent and
are not rolled back if final review is declined. Cancelling does not save Radar
config, secrets, or tmux settings. Existing secrets belonging to other integrations
are preserved. Concurrent setup saves are serialized. Setup detects config or
secret-file edits made during review and aborts rather than overwriting them;
first-time creation still uses exclusive publication. I/O failures are reported, not presented as success;
independent files already saved before a failure are not destructively rolled back.

## Reconfiguring with `radar setup`

Run the same command again to edit a valid existing configuration. The wizard
prefills directories, connection metadata, ticket prefixes, queries and enabled
integration choices. It edits the primary repository directory while retaining
additional repository directories. Custom tmux layouts, model/thinking choices,
filters, mappings, sandbox settings and other fields the wizard does not change
are preserved, including unrecognized JSON fields. Config dotfile symlinks are
preserved; the target is updated atomically.

Secret inputs are never prefilled. Leave a stored-secret prompt blank to retain
it, or enter a replacement. Disabling an integration keeps its stored credentials
for later reuse; setup does not implicitly delete secrets. Environment credentials
remain overrides, are not copied to disk, and are identified by variable name
only; unset the variable to replace the saved credential through setup. Existing
Jira API overrides are retained and verified against that same endpoint.

The final preview is the configuration that will be saved. Cancelling leaves
config, secrets and tmux settings unchanged. Changing directory settings does
**not** relocate existing workspaces or notes. `radar setup` replaces `radar init`;
there is no alias for the old command.

## Settings and secrets

Both live in `$XDG_CONFIG_HOME/radar/`, or `~/.config/radar/` by default.
`config.json` contains settings and connection metadata:

```json
{
  "jira": {
    "enabled": true,
    "base_url": "https://example.atlassian.net",
    "email": "you@example.com",
    "cloud_id": "discovered-cloud-id"
  },
  "datadog": {
    "enabled": true,
    "site": "datadoghq.eu",
    "monitor_query": "tag:team:platform"
  }
}
```

`secrets.json` contains integration-namespaced secrets:

```json
{
  "jira": {"api_token": "<token>"},
  "datadog": {"api_key": "<API key>", "app_key": "<application key>"}
}
```

The directory is `0700`, the files are `0600`, and writes use private temporary
files and atomic publication. Secrets are **plaintext**, protected by filesystem
permissions, not encrypted. The reader rejects symlinks, non-regular files, broad
permissions, and malformed secret JSON without echoing its contents. Repair
permissions with `chmod 600`; edit/rotate secrets locally. GitHub credentials stay
with gh and Pi provider authentication stays with Pi (`/login`).

The documented `RADAR_JIRA_*` and `RADAR_DATADOG_*` environment variables remain
supported and override corresponding stored settings/secrets. `jira.api_base_url`
can specify an API base explicitly, like the existing `RADAR_JIRA_API_BASE_URL`;
otherwise the Cloud ID determines the Atlassian API URL. No credentials are
copied from the environment into files automatically.

## Tmux dashboard workflow

Bare `radar` opens the dashboard in the current terminal, without attaching to or
starting tmux. Opening a workspace then starts/reuses its session and attaches
outside tmux, or switches the current client inside tmux. After detaching an
outside-tmux workspace client, the original dashboard resumes. Attachment errors
are shown in the dashboard rather than closing it.

For people already using tmux, prefix + r is an optional dashboard popup:

```tmux
bind-key r display-popup -E -w 90% -h 90% -d '#{pane_current_path}' 'radar'
```

If setup installs tmux, it proposes a starter config with mouse support, larger
scrollback, one-based window/pane numbering, renumbering, low Escape delay, focus
events, and a status-bar reminder. If tmux was already installed, it offers only
the popup binding, explicitly warning that an existing prefix + r binding will
be replaced. Declining the optional addition leaves tmux configuration unchanged;
`radar` still opens the dashboard directly and can attach when you select a workspace.

After final confirmation, Radar writes `radar/tmux.conf` beside `config.json` and
appends a `source-file` line to `~/.tmux.conf`, or an existing
`$XDG_CONFIG_HOME/tmux/tmux.conf` when no `~/.tmux.conf` exists. Existing contents,
file permissions, and dotfile symlinks are preserved. Custom `tmux -f` configs
need the displayed source line added to that custom file. Existing prefixes are
not changed. Repeat application does not duplicate the include. If a server is
running, setup sources the snippet immediately; failure is reported with a
manual reload instruction. A new server loads the snippet from the user config.

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
| Jira | Connection settings and a token in config/secrets or `RADAR_JIRA_*` overrides |
| Datadog | Both credentials and an explicit `datadog.monitor_query`; no automatic organization-wide query |
| SBX collection | `sbx` on PATH, or Windows `sbx.exe` plus `wslpath` on WSL2 |
| SBX for new workspaces | macOS and `sbx` on PATH; Windows SBX managed workspaces remain blocked by WSL symlink support |

Git worktrees and tmux sessions are discovered when their CLIs are available.
The macOS notifier is used when its companion is installed. Interactive first-run setup requires the tools listed above; background collection
continues to report missing optional sources without hiding healthy ones. Tools installed later on PATH are detected
on subsequent checks, without rewriting the config. If the daemon's PATH itself
changes, restart the daemon to give it the updated environment.

Dashboard startup recovers reported SBX authentication failures by launching
`sbx login` (`sbx.exe login` for Windows SBX from WSL2) before opening the TUI,
then refreshing local sources. `radar create` and `radar fork` also check SBX
authentication in the foreground. Background collection never opens login
prompts, and unrelated runtime failures do not trigger login. First-run setup offers GitHub login; it can also be run manually with `gh auth login`. SBX runtime failures never cause a
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
  task-notes directory. An existing Obsidian vault is optional; Radar does not guess one.
- Workspace sessions require tmux and default to one Pi window running
  `pi $RADAR_PI_ARGS`. Neovim is not required unless explicitly configured.
  New sessions check configured `pi` and `nvim` pane commands before provisioning.
  Unused pane tools are not required; arbitrary custom shell commands remain
  the user's responsibility.
- Git members require Git. Repository discovery requires `fd`.
- Pi's Radar tools/activity require the separately installed `pi-radar` package;
  sandbox tool routing requires `pi-sbx` >=0.6.0 for early sandboxed launch.
  Installing the SBX CLI does not install those extensions. Radar-launched
  interactive Pi sessions show one non-blocking install notice per Pi profile,
  recommending each missing package independently. Configured or explicitly
  disabled package declarations suppress only that package's advice, so installed
  `pi-radar` does not hide missing `pi-sbx` advice. This notice does not enforce
  versions, auto-install packages, or change Pi settings. Hide it with
  `/radar-dismiss-install-hint`.
- URL actions require the platform opener (`xdg-open` on Linux, `open` on macOS).

`linking_mark_prefixes` defaults to `[]`. This disables only ticket-prefix
linking, not source identity, branch, or workspace linking. GitHub's generated
tracked-PR rules also default to an empty list, not example repository searches.

## Installing and updating Pi packages

Install both packages in a host terminal, in the Pi profile used for Radar
sessions (Pi 0.85.1 or newer, Node.js 24+):

```sh
pi install npm:@christianmoesl/pi-radar
pi install npm:@christianmoesl/pi-sbx
```

For a custom profile, prefix each command with
`PI_CODING_AGENT_DIR=/path/to/profile`. These user-scoped installs also serve
sandboxed workspaces because Pi itself runs on the host. Restart Pi afterwards.
The existing `<Pi agent directory>/radar/install-hint-seen` marker still owns
notice suppression; this recommendation does not reset it or migrate profiles.

Update unpinned packages with `pi update npm:@christianmoesl/pi-radar` and
`pi update npm:@christianmoesl/pi-sbx`, in the same host Pi profile, then restart
Pi. Bare `pi update` updates Pi itself, not these packages. Versioned npm sources
are pinned and skipped by package updates; install the desired version explicitly
with `pi install npm:@christianmoesl/pi-sbx@<version>` (>=0.6.0 for early sandboxed
launch), or `pi install npm:@christianmoesl/pi-radar@<version>`. The two extensions
have independent release versions.

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
- First launch of the installed daemon with no optional tools or credentials, without silently generating config.
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

## Onboarding regression matrix

`go test ./...` also drives the real inline wizard through a pseudo-terminal with
isolated HOME/XDG/PATH, real JSON files, stub installers and a local HTTP server:

- Every GitHub/Jira/Datadog opt-in combination, both with existing tools and with
  simulated tmux/Node installation; existing vs missing GitHub authentication.
- Reloaded integration status, real Jira/Datadog collector requests with the stored
  secrets, and note-only workspace creation from every generated configuration.
- Declining dependencies or final review, interrupted prompts, invalid input,
  failed installation/authentication, existing/broken configs, and concurrent saves.
- Secret redaction, owner-only permissions, unrelated-secret preservation, unsafe
  secret-file rejection, and explicit environment overrides.
- Tmux profile preservation, symlinks, XDG locations, idempotence and edits made
  during preview; startup from outside/inside tmux and informational commands.
- With tmux installed, real isolated-server tests exercise attachment, optional popup bindings, reuse of
  a detached session, and the actual prefix + r keystroke. They skip explicitly
  when tmux is unavailable; the hermetic command/config tests always run.

No test uses real service credentials, installs software on the machine, or talks
to the user's tmux server. The PTY matrix adds roughly 90 seconds to the suite.
