# Installation and updates

[Documentation](README.md) · [Quick start](../README.md#get-started)

- [Homebrew on Linux and macOS](#homebrew)
- [One-command installation](#one-command-installation)
- [Standalone Linux installer](#standalone-linux-installation)
- [Standalone macOS installer](#one-command-macos-installation)
- [Manual installation](#manual-installation)
- [First-run setup](#guided-first-startup)
- [Reconfigure](#reconfiguring-with-radar-setup)
- [Notification approval](#macos-notification-setup)
- [Updates](#managed-macos-releases)

## Homebrew

Homebrew is available on Linux (amd64/arm64) and macOS (Apple Silicon only,
Ventura+). Linux users can also use the rootless standalone installer below.

**Linux publication gate:** support in this checkout is not proof it is live in
[ChristianMoesl/homebrew-tap](https://github.com/ChristianMoesl/homebrew-tap).
A new signed release with both Linux archives, exact npm approval, all native
Homebrew validation jobs, and publication of the generated tap formula are
required first. Maintainers can enable [automatic publication](releases.md#automatic-publication-setup)
after npm approval. Do not republish an older release's metadata or archives. Until
then, use [standalone Linux installation](#standalone-linux-installation). See the
[maintainer rollout checklist](releases.md#linux-homebrew-rollout).

After publication, with [Homebrew](https://brew.sh) installed and its `brew shellenv`
applied in your shell as directed by Homebrew:

```sh
brew install ChristianMoesl/tap/radar
radar
```

The tap is [ChristianMoesl/homebrew-tap](https://github.com/ChristianMoesl/homebrew-tap).
Post-install caveats show only prerequisites, startup/update commands and the
platform-specific next step; this guide contains the full setup and approval details.
Homebrew provides the colored heading. The caveat body stays plain text, as required
by Homebrew's audit rules and `brew info --json` output.

It installs prebuilt Radar binaries, manual pages, fd, tmux and gh. The
notification companion is macOS-only. Linux URL actions require `xdg-open` on
PATH (usually supplied by the distribution's `xdg-utils` package); it is not
installed by the formula.
Git is expected on PATH. **Pi, Node.js and npm are user-managed**, not formula
dependencies. For a fresh machine missing Node/npm and [Pi](https://pi.dev) 0.85.1+,
use:

```sh
brew install node && curl -fsSL https://pi.dev/install.sh | sh
```

If compatible Node.js 24+ and npm are already installed, run only Pi's official
installer: `curl -fsSL https://pi.dev/install.sh | sh`. Pi's own Node minimum may
be lower; Radar's Pi integration still requires 24+. Existing compatible
installations are reused regardless of package manager. Setup checks them before
configuring Pi extensions; it never installs Node or Pi core itself.

No Go or Swift compiler is needed for Radar. macOS releases require macOS 13 or
newer. On Linux, use a distribution and architecture supported by Homebrew;
Homebrew and its dependencies have their own OS/build-tool requirements.
Homebrew's installation may require administrative setup; Radar's formula does
not use sudo.

Homebrew only installs into its prefix. On macOS, its sandboxed hooks cannot register apps
with Launch Services; `radar setup notifications` verifies and registers the
companion only when you choose **Set up & test** in a normal host terminal.
Homebrew does not launch Radar, edit your shell profile, write
user configuration or install Pi extensions. Run `radar`: first launch or missing
required tools automatically starts setup. You can also run `radar setup`: the normal
consent/review flow installs the required `pi-radar` extension, creates default
`AGENTS.md` instructions if absent, and saves configuration. Existing instructions
(including symlinks) are preserved. On macOS, native notification launch approval and
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
`brew upgrade radar` does **not** install or update them. After the upgraded
stable Radar release starts/restarts its daemon, Radar automatically synchronizes
installed, unpinned `pi-radar` and `pi-sbx` packages in the daemon's startup Pi
profile. Homebrew's hooks never modify Pi profiles themselves. Pins and custom
sources stay untouched. Failures appear in the daemon log (`radar logs`); restart
Radar after correcting the cause to retry. See [Pi package updates](integrations/pi.md#package-updates)
for custom profiles and manual updates. Active Pi sessions remain running;
`pi-radar` shows a red `/reload` status when its installed version is newer than
the loaded version. Choose a safe moment to reload. Do not use bare `pi update`.

### Switching from another installation

Check `which -a radar` before and after installing. An older `~/.local/bin/radar`
can shadow Homebrew's binary. Stop the old daemon with that binary, deliberately
resolve PATH precedence, and use the Homebrew binary for setup/restart. Neither
the formula nor setup removes an existing Radar install or migrates its managed
update receipt. Keep your existing config, notes and workspace data. Use one
installation/update owner; the standalone bootstrap does not repair a Homebrew
installation.

## One-command installation

On macOS or Linux, run in a terminal as your normal user:

```sh
curl -fsSL https://raw.githubusercontent.com/ChristianMoesl/radar/main/install.sh | bash
```

The installer checks required tools, offers to install missing ones, verifies
the release, offers PATH configuration, and starts guided setup. Each installation
requires confirmation; PATH changes have a separate confirmation. Declining a
required tool stops installation. Radar itself installs into a user-owned directory.

Both platforms require **Node.js 24+** for publisher verification and Pi. The
installer offers to install it before downloading a release. On macOS it uses
Homebrew, offering to install Homebrew if needed. Linux uses an existing Homebrew,
apt-get, dnf or pacman. Linux system-package commands may require sudo; the exact
command is shown and runs only after approval. Do not run the bootstrap itself
with sudo. If the package manager cannot supply the required version, install it
on PATH using your preferred method and rerun.

After verifying the release, its authenticated prerequisite helper checks Git,
tmux 3.2+, fd (`fdfind` on Debian/Ubuntu), Node.js 24+, npm, Pi 0.85.1+ and gh.
Suitable tools are retained. Missing tools are offered through the supported
package manager; Pi installs under `~/.local` using npm without sudo or lifecycle
scripts. No Go or Swift compiler is required. Linux URL actions also need
`xdg-open`, usually provided by the distribution's `xdg-utils` package.

PATH configuration supports Bash, Zsh and Fish. Accepted changes append one
marked block while preserving existing content, file permissions and symlink
targets. Declining leaves the profile untouched and prints the installed binary's
path. Setup starts immediately in interactive terminals even if PATH changes
are declined. Headless installation requires all tools to be ready, never consumes
piped approval, never edits profiles, and leaves guided setup for a terminal.

Downloads use HTTPS, time/size limits, a signed stable release manifest, and
verified archive and binary checksums. The selected release must also have its exact public Pi
package. If none is fully published, installation fails with an explanation.

## Standalone Linux installation

The shared installer supports Linux amd64/x86_64 and arm64/aarch64.

Binary directory selection is deterministic:

1. `RADAR_INSTALL_DIR`, when explicitly set. Invalid or unwritable overrides fail.
2. `$HOME/.local/bin`, when that directory is already an entry in PATH.
3. `$HOME/bin`, when that directory is already an entry in PATH.
4. Otherwise `$HOME/.local/bin`, creating it if needed.

Missing directories are created, including a custom destination. Relative
overrides are resolved against the current working directory. The destination
must be owned by you, writable and searchable, with no shared-write permissions
or symlink components. An invalid selected directory fails rather than silently
choosing a different one. Other writable PATH entries, including Homebrew, Cargo
and npm directories, are never chosen automatically. For a downloaded script:

```sh
RADAR_INSTALL_DIR="$HOME/bin" ./install.sh
```

When piping, pass the override to **Bash**, which runs the installer:

```sh
curl -fsSL https://raw.githubusercontent.com/ChristianMoesl/radar/main/install.sh | RADAR_INSTALL_DIR="$HOME/bin" bash
```

Shell changes follow the shared confirmation flow. If the binary directory is
missing from PATH, the installer also prints quoted instructions for Bash
(`~/.bashrc`), Zsh (`~/.zshrc`) and Fish (`fish_add_path`). No `--modify-path` option
is needed. Headless installation requires ready tools and skips shell changes
and setup.
Manuals and the MIT notice go under `~/.local/share`; your Radar configuration,
agent instructions and workspaces are left for guided setup.

An existing `command -v radar` is reported without executing it. Package-manager
paths are identified where possible, and other installations are left alone. A
checksum receipt, `.radar-install.json` next to the binary, identifies installations
owned by this bootstrap. Unrelated binaries, symlinks and modified managed binaries
are refused. The binary is staged on its destination filesystem with executable
permissions, then renamed atomically. The receipt recognizes both old and new
binary identities if activation is interrupted, allowing a safe retry. Concurrent
installations are rejected; after a hard kill, inspect `.radar-install.lock` and
remove it only once no installer is running. The installer warns if another
executable still takes precedence on PATH and refuses downgrades.

Interactive installation starts guided setup after the PATH offer. In headless
environments, run `radar` in a terminal afterward.
For updates, rerun the same bootstrap with the same `RADAR_INSTALL_DIR` override,
if any, then run `radar restart` when the daemon is already running. Review
[older-installation rollout guidance](#before-upgrading-older-installations) first.
The built-in `radar update` command remains macOS-only.

## One-command macOS installation

Use the [shared one-command installer](#one-command-installation).

On macOS, the root `install.sh` is the standalone first-install bootstrap. It
detects Apple Silicon, selects a stable release with a complete macOS arm64
archive **and its exact public npm package**, authenticates the signed metadata,
verifies the selected archive/binary/notifier identities, and safely extracts it.
It calls the existing archive installer and starts bare `radar` from your HOME,
after installing the required CLI tools. Guided setup handles Pi extensions and settings.

No version choice, copied hashes, separate notifier download, Go/Swift compiler,
pnpm or preinstalled GitHub CLI is needed. Release selection can skip a newer
incomplete release in favor of an older ready one. If no coordinated release is
ready, it says to try later and installs no Radar files. Maintainers must finish
npm staging/approval; users do not configure release publishing.

- **Required tools:** prerequisite checks and installation offers follow the
  shared flow above. The official Homebrew script may require normal macOS
  administrative/Command Line Tools approval. Failure or refusal stops;
  already-consented prerequisite installations are not rolled back.
- **PATH:** a separate offer appends one marked PATH block to the user's Bash,
  Zsh or Fish profile, preserving existing content, modes and symlink targets. It includes
  an activated Homebrew path when needed. Declining leaves the profile untouched
  and prints `~/.local/bin/radar` as the command. Radar still starts immediately.
- **Existing installations:** rerunning the bootstrap repairs required tools only;
  it does not replace Radar, its notifier, update state, settings or workspace data.
  It authenticates a release before using its prerequisite helper, then starts
  setup with the existing binary. An orphaned
  notifier/update transaction without an installed CLI still needs inspection.
  Use `radar update` to update eligible Radar installations; custom/symlink/
  package-manager paths remain manual.
- **Consent:** prompts read the controlling terminal even when the script is piped.
  Headless execution requires ready tools and skips profile edits and setup. No env/flag
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

For source builds or a custom prefix/manual-page layout, use the manual path.
Linux (including WSL) can instead use the standalone Linux bootstrap above.
[Published archives](https://github.com/ChristianMoesl/radar/releases) cover
macOS on Apple Silicon (arm64), and Linux on arm64/amd64; on Windows, run Radar
inside WSL. Intel Macs are not supported. Download the matching archive and `checksums.txt` from a release you trust:

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
See [Native Linux/WSL setup and Windows backend limits](integrations/sbx.md#windows--wsl2).

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
installation prompts, GitHub login, and new GitHub/Jira/Datadog connections
default to **Yes**, but still require confirmation. The final review uses
**Enter to Save**, **v to View details**, and **c to Cancel**. Saving still requires
an explicit keypress; viewing details never approves changes. Profile edits retain
their explicit consent/defaults. Existing integration choices are
preselected on repeat setup. Secrets are masked. A new tmux configuration leaves
the prefix at tmux's own default; existing prefix settings are preserved.

1. **Check, don't install, CLI tools:** Git, tmux 3.2+, fd (`fdfind` on Debian),
   Node.js 24+, npm, and Pi 0.85.1+. One status list distinguishes ready,
   missing, outdated and failed checks, followed by relevant repair guidance.
   Green checks and amber attention markers use plain text with `NO_COLOR` or
   redirected output. Missing tools get a copyable installation command when
   available; existing broken/outdated installations are repaired through their
   own installation method, not replaced by another package manager. Pi's primary
   recommendation is its official installer. Setup stops before saving when any
   required tool needs attention; rerun `radar` after repairs.
   GitHub CLI is checked when GitHub is selected below. Node is Pi's runtime,
   not a Go binary runtime requirement; Neovim is not required.
2. **Install Pi extensions:** `pi-radar` is mandatory for completed setup. Show
   the `pi install` command and ask permission; refusal, installation failure or
   failed verification stops setup. Existing Git/local package sources and
   explicit extension disablement are never silently overridden.
3. **Sandboxed workspaces:** on macOS or native Linux, offer SBX (recommended for new installs;
   existing choices are preselected). Check SBX 0.48.x, guide installation or
   updates if needed, offer login, and install/verify pi-sbx 0.7.0+ with consent.
   If SBX reports an uninitialized global network policy, offer balanced
   (recommended), deny-all, allow-all, or Not now. Selecting a policy runs
   `sbx policy init <choice>` immediately and verifies it; this affects all local
   SBX sandboxes and survives cancellation. Existing policies are never reset
   or replaced, and unrelated inspection errors block preparation.
   Declining SBX skips these checks and writes an explicit opt-out. When no
   sandbox recipe is explicitly configured, propose a complete editable local
   kit, an env-file and an empty read-only `startup.d`, using
   `[sandbox-startup, run]` for readiness. Preserve existing custom recipes/files.
   With separate consent, prepare and remove a temporary sandbox to verify
   startup, basic tools and file sharing before saving; show bounded progress.
   Failure offers retry, explicit opt-out or cancellation—never silent host
   fallback. Cleanup failures stop setup with recovery instructions.
   See [local kits, customization and credentials](integrations/sbx.md#guided-setup-and-your-local-kit).
4. Offer the tmux configuration described below.
5. Ask for the repository directory, workspace root, and notes parent directory.
   All may be missing, including nested parent directories: they are created
   after final confirmation, not while typing. Notes live under `Tasks/` in the
   notes parent. An Obsidian vault is optional. Files cannot be used as directories,
   and the workspace root must not contain the repository directory.
6. Ask whether to connect GitHub, Jira, and Datadog. Declining writes an explicit
   `enabled: false`; opting in verifies access and writes `true`. GitHub uses
   `gh` 2.0.0+, offers assisted installation with Homebrew or official manual instructions, rechecks installation, starts `gh auth login` when needed and verifies the account with `gh api user`. A failed connection can be retried or deferred. Jira uses site URL,
   email, hidden API token, automatic Cloud ID discovery, and comma/space-separated
   ticket prefixes. Datadog uses Pup 2.0.0+ OAuth, a supported site, a named session and a required monitor query such as `tag:team:platform`. Setup recommends/installs Pup, launches browser login and verifies monitor reads; it offers retry or defer on failure.
7. Review one compact table of files: **CREATE**, **UPDATE**, **APPEND**, or
   **UNCHANGED**, with each path and a short purpose. Creates are green, changes
   amber, and unchanged rows dim; labels remain readable without color. Only new
   directories are listed. Credentials remain plaintext and owner-only; values
   are never displayed. The summary includes the effective sandbox default and proposed starter files.
   Completed installations, logins, network-policy initialization and sandbox
   preparation are reported as side effects that cancelling cannot undo.
   Press **Enter** to save or **c** to cancel (**Esc** also cancels from the summary).
   Press **v** to select a file with the arrow keys and **Enter**: new files show
   their contents, updates/appends show a diff, and credential details contain
   only masked descriptions. Details are scrollable, not dumped into scrollback.
   **Esc** returns to the file list, then the summary; Enter in details never
   saves. Ctrl+C/Ctrl+D cancel from any review screen.

Tool installations, logins and sandbox preparation happen with their own earlier
consent. Downloads and custom startup-script side effects are not rolled back if
final review is declined. Cancelling does not save Radar
config, secrets, sandbox starter files, or tmux settings. Existing secrets belonging to other integrations
are preserved. Concurrent setup saves are serialized. Setup detects config or
secret-file or proposed sandbox-starter edits made during review and aborts rather than overwriting them;
first-time creation still uses exclusive publication. I/O failures are reported, not presented as success;
independent files already saved before a failure are not destructively rolled back.

## Reconfiguring with `radar setup`

Run the same command again to edit a valid existing configuration. The wizard
prefills directories, connection metadata, ticket prefixes, queries and enabled
integration choices. It edits the primary repository directory while retaining
additional repository directories. Custom tmux layouts, model/thinking choices,
filters, mappings, sandbox recipes and other fields the wizard does not change
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
  org: radar
  monitor_query: tag:team:platform
```

`secrets.yaml` contains integration-namespaced secrets:

```yaml
# Plaintext credentials: keep this file private and out of version control.
jira:
  api_token: <token>
```

The directory is `0700`, the files are `0600`, and writes use private temporary
files and atomic publication. Secrets are **plaintext**, protected by filesystem
permissions, not encrypted. The reader rejects symlinks, non-regular files, broad
permissions, and malformed secret YAML without echoing its contents. Repair
permissions with `chmod 600`; edit/rotate secrets locally. GitHub credentials stay
with gh and Pi provider authentication stays with Pi (`/login`).

The documented `RADAR_JIRA_*` environment variables currently override Jira settings/secrets. Datadog uses Pup OAuth only; its old key environment variables and secret mapping are rejected. See the [manual cutover guide](cli-oauth-migration.md). `jira.api_base_url`
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
| Jira | Connection settings and a token in config/secrets or `RADAR_JIRA_*` overrides (TWG migration pending qualification) |
| Datadog | Pup 2.0.0+ on PATH, a named OAuth session and an explicit `datadog.monitor_query`; no automatic organization-wide query |
| SBX collection | `sbx` on PATH, or Windows `sbx.exe` plus `wslpath` on WSL2 |
| SBX for new workspaces | macOS or native Linux `sbx` 0.48.x on PATH, with accessible KVM on Linux; Windows `sbx.exe` provisioning remains blocked |

Git worktrees and tmux sessions are discovered when their CLIs are available.
The macOS notifier is used when its companion is installed. Interactive first-run setup requires the tools listed above; background collection
continues to report missing optional sources without hiding healthy ones. Tools installed later on PATH are detected
on subsequent checks, without rewriting the config. If the daemon's PATH itself
changes, restart the daemon to give it the updated environment.

Dashboard startup recovers reported SBX authentication failures by launching
`sbx login` (`sbx.exe login` for Windows SBX from WSL2) before opening the TUI,
then refreshing local sources. `radar create` and `radar fork` also check SBX
authentication in the foreground. Background collection never opens login
prompts, and unrelated runtime failures do not trigger login. Setup offers GitHub and SBX login; they can also be run manually with `gh auth login` and `sbx login`. SBX runtime failures never cause a
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
  sandbox tool routing requires `pi-sbx` >=0.7.0 for early sandboxed launch.
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
  Apple Silicon and explicit Intel rejection, signed readiness selection, incomplete npm/upload gating,
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
sandbox provisioning. CI also builds native macOS notifier/release archives and installs/tests/uninstalls
native Linux archives with real Homebrew on amd64 and arm64;
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
- SBX policy choices, existing-policy preservation, inspection/init/verification
  failures, cancellation, retry without reinitialization, and diagnostic redaction
  use fake systems; they never initialize the user's real policy.
- Tmux profile preservation, symlinks, XDG locations, idempotence and edits made
  during preview; startup from outside/inside tmux and informational commands.
- With tmux installed, real isolated-server tests exercise attachment, optional popup bindings, reuse of
  a detached session, and the actual prefix + r keystroke. They skip explicitly
  when tmux is unavailable; the hermetic command/config tests always run.

No test uses real service credentials, installs software on the machine, or talks
to the user's tmux server. The PTY matrix adds roughly 90 seconds to the suite.

## macOS notification setup

After saving the main setup, macOS users can optionally set up notifications.
The initial screen shows only the purpose, a short macOS approval notice, and
**Set up & test**, **Skip for now**, and **Help**. Press **t** or **Enter** to start,
**s** to skip without changing preferences, or **h** for detailed instructions.
EOF never starts a test. Successful setup ends immediately after you confirm
that the notification arrived and its click opened the test page.

Return at any time with `radar setup notifications`. Choosing **Set up & test**
verifies the companion's code signature and registers its installed app bundle
with Launch Services before launching it. Registration failure is reported and
stops that test; retry explicitly after fixing the problem. Deferring setup or
opening a settings link does not verify, register or launch the companion. Deferring
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

The setup offers **Privacy & Security** and **Notification Settings** shortcuts
under **Help**, and the relevant shortcut beside **Retry** when a check needs
attention. Signature/registration failures stop the attempt with repair guidance;
a generic launch failure is not treated as proof of a Gatekeeper block or denied
notification permission. Long diagnostics and the companion path are available
in Help instead of the initial screen. Settings links never launch or register
the companion. Routing can vary by macOS version; written navigation remains
available when a link fails. Opening Settings does not grant permissions or
promise to select the app/scroll to its approval row.

Send the test and confirm delivery and the click destination. Focus or banner
settings can hide a banner even with permission granted; also check Notification
Center. A successful launch is not proof of authorization/delivery/clicks.
Background notifications do not request permission; after a failed helper launch,
Radar avoids repeatedly launching that same binary until a successful setup/test.

## Managed macOS releases

Run `radar update` to review a release and confirm adoption/update of the
standard `~/.local` installation. No silent install occurs. Homebrew installations
use `brew upgrade radar`; other custom/symlink/package-manager installs and
Windows installs remain inside WSL. The built-in updater does not support Linux;
standalone Linux installs rerun the bootstrap and Linux Homebrew installs use
`brew upgrade radar`. CLI and Pi updates have separate
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
