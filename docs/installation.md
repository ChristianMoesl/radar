# Installation and updates

[Documentation](README.md) · [Quick start](../README.md#get-started)

- [Homebrew on macOS](#homebrew)
- [Standalone macOS installer](#one-command-macos-installation)
- [Manual installation](#manual-installation)
- [First-run setup](#guided-first-startup)
- [Reconfigure](#reconfiguring-with-radar-setup)
- [Notification approval](#macos-notification-setup)
- [Updates](#managed-macos-releases)

## Homebrew

On macOS (Apple Silicon or Intel), with [Homebrew](https://brew.sh) installed:

```sh
brew install ChristianMoesl/tap/radar
radar
```

The tap is [ChristianMoesl/homebrew-tap](https://github.com/ChristianMoesl/homebrew-tap).
It installs prebuilt Radar binaries, the notification companion, manual pages,
fd, tmux and gh.
Git is expected on PATH. **Pi, Node.js and npm are user-managed**, not formula
dependencies: install Node.js 24+ with npm using your preferred method, then
[Pi](https://pi.dev) 0.85.1+ (for example,
`npm install -g @earendil-works/pi-coding-agent`). Existing compatible installations
are reused regardless of package manager. Setup validates them before configuring
Pi extensions; it never installs Node or Pi core itself.

No Go or Swift compiler is needed for Radar. The release requires macOS 13 or
newer; Homebrew and its dependencies may require a newer supported macOS version
or build tools of their own.

Homebrew only installs into its prefix and registers the notification companion
with Launch Services. It does not launch Radar, edit your shell profile, write
user configuration or install Pi extensions. Run `radar`: first launch or missing
required tools automatically starts setup. You can also run `radar setup`: the normal
consent/review flow installs the required `pi-radar` extension, creates default
`AGENTS.md` instructions if absent, and saves configuration. Existing instructions
(including symlinks) are preserved. Native notification launch approval and
permission remain separate user actions; see [notification setup](#macos-notification-setup).

### Homebrew updates

```sh
brew update
brew upgrade radar
radar restart # if the daemon is running
```

Use Homebrew, **not `radar update`**, to replace Homebrew-managed files. Review the
release notes and [older-installation rollout guidance](#before-upgrading-older-installations)
before upgrading: Homebrew does not run Radar's managed-update data preflight or
transactional rollback. Close/reopen dashboards after updating. Homebrew upgrade
and uninstall leave your configuration, notes and workspaces alone; automatic
workspace expiry still follows the configured Radar policy when Radar runs.

Git, Node/npm and Pi core remain managed by your chosen installation method;
`brew upgrade radar` does **not** install or update them. The host `pi-radar`
extension is profile-managed and is not updated by that command either. Follow the
[Pi package update instructions](integrations/pi.md#package-updates), respecting
custom/pinned/project package sources. Active Pi sessions need `/reload` or
restart after an extension update. Do not use bare `pi update`.

### Switching from another installation

Check `which -a radar` before and after installing. An older `~/.local/bin/radar`
can shadow Homebrew's binary. Stop the old daemon with that binary, deliberately
resolve PATH precedence, and use the Homebrew binary for setup/restart. Neither
the formula nor setup removes an existing Radar install or migrates its managed
update receipt. Keep your existing config, notes and workspace data. Use one
installation/update owner; the standalone bootstrap does not repair a Homebrew
installation.

## One-command macOS installation

In a normal Terminal window, without sudo:

```sh
curl -fsSL https://raw.githubusercontent.com/ChristianMoesl/radar/main/install.sh | bash
```

The root `install.sh` is the standalone first-install bootstrap, not an updater. It
detects Apple Silicon/Intel, selects a stable release with both complete macOS
archives **and its exact public npm package**, authenticates the signed metadata,
verifies the selected archive/binary/notifier identities, and safely extracts it.
It calls the existing archive installer and starts bare `radar` from your HOME,
after installing the required CLI tools. Guided setup handles Pi extensions and settings.

No version choice, copied hashes, separate notifier download, Go/Swift compiler,
pnpm or preinstalled GitHub CLI is needed. Release selection can skip a newer
incomplete release in favor of an older ready one. If no coordinated release is
ready, it says to try later and installs no Radar files. Maintainers must finish
npm staging/approval; users do not configure release publishing.

- **Required tools:** the installer provisions Git, tmux 3.2+, fd, Node.js 24+,
  npm, Pi 0.85.1+, and gh (GitHub CLI). Already suitable tools are retained. It
  offers Homebrew when needed, and Node before authenticating the release.
  Each installation needs explicit approval, defaults to **Yes**, and can be refused. The official Homebrew script may
  require normal macOS administrative/Command Line Tools approval. Failure or
  refusal stops; already-consented prerequisite installations are not rolled back.
- **PATH:** a separate offer appends one marked PATH block to the user's zsh or
  Bash profile, preserving existing content, modes and symlink targets. It includes
  an activated Homebrew path when needed. Declining leaves the profile untouched
  and prints `~/.local/bin/radar` as the command. Radar still starts immediately.
- **Existing installations:** rerunning the bootstrap repairs required tools only;
  it does not replace Radar, its notifier, update state, settings or workspace data.
  It authenticates a release before using its prerequisite helper. An orphaned
  notifier/update transaction without an installed CLI still needs inspection.
  Use `radar update` to update eligible Radar installations; custom/symlink/
  package-manager paths remain manual.
- **Consent:** prompts read the controlling terminal even when the script is piped.
  Headless execution cannot approve installations or profile edits. No env/flag
  bypass changes publisher trust, endpoints, release selection or prompts.
- **Notifications:** the installer preserves Gatekeeper policy. Launch approval and
  notification authorization still require the user's macOS actions; setup guides
  them. It never strips quarantine or grants permissions on the user's behalf.

The bootstrap's committed Ed25519 roots must match `internal/update/keys.json`;
tests check that alignment and the state epoch. Root rotation must update both.
Downloads have HTTPS-only redirects, time/size limits and strict release/archive
validation. Unsigned, unknown-key, tampered or incompatible releases fail closed;
checksums alone never authorize installation.

The official bootstrap source is an **initial trust anchor**, delivered over
GitHub HTTPS. Inspect/trust it before execution. Release signatures do not make
a substituted malicious bootstrap safe, and the bootstrap does not download a
new trust root from the release. Its embedded verifier needs Node because stock
macOS crypto utilities do not provide a consistent Ed25519 interface; refusing
Node does not switch to weaker verification.

## Manual installation

For Linux, Windows/WSL, source builds or a custom prefix, use the manual path.
[Published archives](https://github.com/ChristianMoesl/radar/releases) cover
macOS and Linux on arm64/amd64; on Windows, run Radar inside WSL. Download the
matching archive and `checksums.txt` from a release you trust:

```sh
archive=radar_<version>_<os>_<arch>.tar.gz
grep -F "  $archive" checksums.txt | shasum -a 256 -c -
tar -xzf "$archive"
"${archive%.tar.gz}/install.sh"
```

Checksums detect corruption but do not independently authenticate an initial
download. Trust the release/source independently. Archive `install.sh` uses
`~/.local` by default; `PREFIX`, `BINDIR` and `LIBEXECDIR` remain available for
manual installations. It does not edit shell profiles. Both the archive installer
and `make install` check/install required CLI tools with explicit, default-Yes
permission, using the same prerequisite helper. Homebrew and Linux apt-get are
supported; Pi is installed with npm without sudo or lifecycle scripts. If the
package manager cannot supply Node 24+ or another required version, install it
on PATH and rerun the installer. A headless installation succeeds only when all
prerequisites are already available; piped input never approves installation.
An existing configuration/instruction file is preserved. A manual source install
uses `make install`; see [build prerequisites](../CONTRIBUTING.md#development-setup).
Node is Pi's runtime, not a Go binary runtime requirement. Ensure `~/.local/bin`
(or your chosen binary directory) is on PATH, then run `radar` for guided setup.
Linux URL actions need `xdg-open`, usually from `xdg-utils`.
See [Windows/WSL sandbox limits](integrations/sbx.md#windows--wsl2).

Both source and archive installation add the MIT notice under `share/radar/LICENSE`
and default agent instructions under the Radar config directory. Existing
instructions are never overwritten. macOS archives include the notifier app
under `libexec/radar` and register it with Launch Services.

## Guided first startup

Run `radar` in an interactive terminal, or `radar setup` to complete setup without
opening the dashboard. `radar setup` is repeatable: existing settings prefill the
wizard and are updated only after review and affirmative save confirmation.
Bare `radar` starts setup when `config.yaml` is missing or any required CLI tool
cannot be found on PATH, even with an existing configuration. Normal dashboard
startup checks executable availability only—it does not run version probes or
inspect/install Pi packages. Setup checks usability and minimum versions and
reports missing or outdated tools before any configuration is saved. Explicit
`radar setup` also performs those full checks. Empty or malformed config files
must be repaired before setup; they are never silently reset.
`radar config-path`, `version`, and background daemon startup do not generate a
config. A headless daemon can still collect with in-memory defaults; it never
prompts or installs anything.

The inline prompts stay in terminal scrollback. Escape/Ctrl+C cancels. Software
installation prompts default to **Yes**; final save, login, profile edits and
new integration opt-ins retain their explicit consent/defaults. Existing
integration choices are preselected on repeat setup. Secrets are masked.

1. **Check, don't install, CLI tools:** Git, tmux 3.2+, fd (`fdfind` on Debian),
   Node.js 24+, npm, Pi 0.85.1+, and gh. Setup lists all missing/unusable tools
   together and stops before saving. For Homebrew, Git is expected on PATH and
   Pi/Node/npm are user-managed; repair fd/tmux/gh using Homebrew. Otherwise rerun
   your platform's installer or `make install` from the source checkout, then
   `radar` or `radar setup`. The one-command bootstrap is macOS-only. gh is required even if GitHub integration is disabled;
   installing it does not log in or enable the integration. Node is Pi's runtime,
   not a Go binary runtime requirement; Neovim is not required.
2. **Install Pi extensions:** `pi-radar` is mandatory for completed setup. Show
   the `pi install` command and ask permission; refusal, installation failure or
   failed verification stops setup. `pi-sbx` is checked only when SBX is both
   installed and effectively enabled for workspaces. `sbx.enabled: false` skips
   it even with the CLI installed. Automatic mode enables SBX on macOS with its
   CLI installed; those sessions need pi-sbx 0.6.0+. Existing Git/local package
   sources and explicit extension disablement are never silently overridden.
3. Offer the tmux configuration described below.
4. Ask for the repository directory, workspace root, and notes parent directory.
   All may be missing, including nested parent directories: they are created
   after final confirmation, not while typing. Notes live under `Tasks/` in the
   notes parent. An Obsidian vault is optional. Files cannot be used as directories,
   and the workspace root must not contain the repository directory.
5. Ask whether to connect GitHub, Jira, and Datadog. Declining writes an explicit
   `enabled: false`; opting in verifies access and writes `true`. GitHub uses
   `gh auth status` and offers `gh auth login` when needed. Jira uses site URL,
   email, hidden API token, automatic Cloud ID discovery, and comma/space-separated
   ticket prefixes. Datadog uses a supported site/API endpoint, hidden API and
   application keys, and a required monitor query such as `tag:team:platform`.
6. Review separate **Radar settings**, **Agent instructions**, **Credentials**, **Radar tmux settings**,
   **User tmux configuration**, and **Directories** sections. Each file shows its
   path and CREATE / UPDATE / APPEND INCLUDE / UNCHANGED action, with full
   non-secret contents or masked credential names. Missing directories are listed
   separately. An **Already completed** notice distinguishes prior installation/
   authentication from pending writes. Only affirmative confirmation saves.

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
are preserved, including unrecognized YAML fields. Config dotfile symlinks are
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
Generated YAML includes short comments explaining important settings. You can add
your own comments; setup and secret updates retain them, along with unedited
settings and key order. No-op updates leave the file unchanged; edits may normalize
whitespace. Each file contains one YAML mapping document.

`config.yaml` contains settings and connection metadata:

```yaml
jira:
  enabled: true
  base_url: https://example.atlassian.net
  email: you@example.com
  cloud_id: discovered-cloud-id
datadog:
  enabled: true
  site: datadoghq.eu
  monitor_query: tag:team:platform
```

`secrets.yaml` contains integration-namespaced secrets:

```yaml
# Plaintext credentials: keep this file private and out of version control.
jira:
  api_token: <token>
datadog:
  api_key: <API key>
  app_key: <application key>
```

The directory is `0700`, the files are `0600`, and writes use private temporary
files and atomic publication. Secrets are **plaintext**, protected by filesystem
permissions, not encrypted. The reader rejects symlinks, non-regular files, broad
permissions, and malformed secret YAML without echoing its contents. Repair
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

If no tmux configuration exists, setup proposes a starter config with **Ctrl+B**
as its prefix: press Ctrl+B, release, then R to open Radar. It also enables mouse
support, larger scrollback, one-based window/pane numbering, renumbering, low
Escape delay, focus events, and a status-bar reminder. The starter is independent
of which installer provisioned tmux. Existing configurations receive an optional
popup/extended-keys addition, warning that an existing prefix + r binding is replaced.

Generated snippets enable `extended-keys on` for Pi's modified Enter keys. tmux
3.5+ also gets `extended-keys-format csi-u`; a tmux version condition omits that
option on supported 3.2–3.4 installations. A supporting terminal is still needed. Declining the optional addition leaves tmux configuration unchanged;
`radar` still opens the dashboard directly and can attach when you select a workspace.

After final confirmation, Radar writes `radar/tmux.conf` beside `config.yaml` and
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
  `pi-radar` does not hide relevant missing `pi-sbx` advice. SBX advice appears
  only for an inspected sandbox-enabled workspace with a usable SBX CLI; missing,
  disabled or uncertain sandbox context stays quiet. This notice does not enforce
  versions, auto-install packages, or change Pi settings. Hide it with
  `/radar-dismiss-install-hint`.
- URL actions require the platform opener (`xdg-open` on Linux, `open` on macOS).

`linking_mark_prefixes` defaults to `[]`. This disables only ticket-prefix
linking, not source identity, branch, or workspace linking. GitHub's generated
tracked-PR rules also default to an empty list, not example repository searches.

## Installing and updating Pi packages

First-run setup requires `pi-radar`; sandboxed workspaces also need `pi-sbx`.
Pi and both extensions are installed on the **host**, including when tools run
inside a sandbox. See the [Pi guide](integrations/pi.md) for manual installation,
custom profiles, switching package sources and version-pinned updates.

## Upgrading and data handling

Installers preserve existing configuration and agent instructions. Existing
`enabled` booleans retain their explicit meaning, including `sbx.enabled: false`
written by older installers. Remove an explicit setting manually to adopt
automatic activation; Radar cannot distinguish an intentional opt-out from an
old generated default. Explicit kit selections are preserved too.

Setup now creates the same default `AGENTS.md` instructions as the source/archive
installers, only if absent and only after final confirmation. Existing files and
symlinks are retained; no data or configuration schema migration is required.

Configuration uses `config.yaml`, `secrets.yaml`, and repository-local
`.radar.yaml`. Workspace registries, notes, and task-cache schemas are unchanged.
Restart a running daemon after updating the binary.

## Regression coverage

`make test` includes:

- One-command bootstrap under macOS system Bash and a controlling PTY, including
  piped source, Homebrew/Node refusals/failures, PATH consent and preserved profiles,
  Apple Silicon/Intel, signed readiness selection, incomplete npm/upload gating,
  signature/hash/tree/architecture errors and hostile/truncated archives. Network,
  Homebrew, Launch Services and binary execution boundaries are test-local; no
  test installs host tools or grants native permissions.

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
isolated HOME/XDG/PATH, real YAML configuration files, stub installers and a local HTTP server:

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

## macOS notification setup

After saving the main setup, macOS users can optionally set up notifications.
Return at any time with `radar setup notifications`. Deferring
keeps the dashboard usable and does not change notification preferences.

The daemon notifies you when a task newly needs attention, not on every refresh
or restart. A PR notification opens its pull request, a Datadog alert opens its
monitor, and other notifications use the task URL when available. Muted tasks do
not notify; suppressing one PR's contribution does not suppress other linked
sources. Radar remains usable without the companion.

The small `RadarNotifier.app` companion supplies Radar's native application
identity, task alerts and notification clicks. It is not a second main UI.
Its version is independent of the CLI: ordinary CLI updates preserve an unchanged
helper, while genuine helper changes can require fresh launch approval.

**Launch approval and notification permission are separate:**

1. If macOS says **RadarNotifier.app Not Opened**, choose **Done**, not **Move to
   Trash**. For the official release you chose to trust, go to **Apple menu →
   System Settings → Privacy & Security → Security → Open Anyway**. Authenticate
   and confirm **Open**, then retry Radar's test. The approval entry appears after
   the blocked launch. Ad-hoc signing is not Apple Developer ID/notarization;
   Apple cannot verify this developer. Never disable Gatekeeper or strip quarantine.
2. Choose **Allow** when the foreground test requests notification permission.
   To change it later: **Apple menu → System Settings → Notifications → Radar →
   Allow notifications**. Launch approval alone does not grant this permission.

The setup offers **Privacy & Security** and **Notifications Settings** actions
using macOS pane links. Routing can vary by macOS version; the written navigation
remains available. Opening Settings does not grant permissions or promise to
select the app/scroll to its approval row.

Send the test and confirm delivery and the click destination. Focus or banner
settings can hide a banner even with permission granted; also check Notification
Center. A successful launch is not proof of authorization/delivery/clicks.
Background notifications do not request permission; after a failed helper launch,
Radar avoids repeatedly launching that same binary until a successful setup/test.

## Managed macOS releases

Run `radar update` to review a release and confirm adoption/update of the
standard `~/.local` installation. No silent install occurs. Homebrew installations
use `brew upgrade radar`; other custom/symlink/package-manager installs and
Linux/Windows remain manual. CLI and Pi updates have separate
outcomes; active Pi sessions need `/reload` or restart after a package change.
See [release trust, prerequisites, supported layout and recovery](releases.md#user-flow-and-recovery).

### Manual updates

For manual installations, download a trusted release archive and run its
installer over the existing installation, or build with `make install`.
Run `radar restart` if the daemon is already running. Update the host Pi packages
separately; see [Pi package updates](integrations/pi.md#package-updates).
Bare `pi update` is not the package-update command.

### Before upgrading older installations

- **Task muting:** users of the former per-task ignore feature must stop the daemon
  and run the [explicit migration](integrations/obsidian.md#migrating-task-muting)
  before starting the new version. A cache reset does not migrate note preferences.
- **Workspace expiry:** already-completed registered workspaces keep their original
  completion time. Those eight days past completion can be deleted at the next
  GC run, including local changes and unpublished commits. There is no recovery
  archive or new startup grace period. Review and preserve work or reopen the task
  before installing; see [expiry rollout](workspace-cleanup.md#before-installing-this-policy).
- **Configuration and notes:** review integration-specific rollout instructions
  for old configuration keys, note titles and workspace links. Radar does not
  silently migrate authored data or user configuration.


## Manual pages

Radar installs **`radar(1)`** and **`radar-config(5)`** alongside the binary:

```sh
man radar
man radar-config
```

The default location is `~/.local/share/man/man1/radar.1` and
`~/.local/share/man/man5/radar-config.5`. Source and archive installations use
`$PREFIX/share/man`; set `MANDIR` to override that location. The macOS bootstrap
and managed updater use the standard `~/.local/share/man` location.

Discovery depends on your system's man implementation and search configuration.
If the user-local directory is not found automatically, use:

```sh
man -M "$HOME/.local/share/man" radar
```

Or add it to your shell configuration while retaining the system search path:

```sh
export MANPATH="$HOME/.local/share/man:${MANPATH:-}"
```

Use your chosen manual directory instead for custom-prefix installations.
Installers report the location but do not change `MANPATH` or require root.
Homebrew installs both pages into its standard manual directories, so `man radar`
and `man radar-config` use the same discovery as other Homebrew packages.
A local man reader is required to display pages; the Pi documentation tool does
not need one. Keyword-search databases (`man -k`) are managed by your system,
not by Radar's user-local installer.

Pages are generated at build time from `docs/cli.md` and `docs/configuration.md`;
no Markdown converter is required on the user's system. CLI documentation is
embedded from the same checkout, so offline answers match the installed build.
Manual pages are included in signed release archives and replaced/restored with
the CLI during managed updates. Existing configuration and notes are untouched.
