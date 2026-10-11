# SBX integration

SBX supplies local Docker sandbox resources and shell actions.

Start with [guided setup](#guided-setup-and-your-local-kit), then choose
[startup scripts or a custom image](#preconfigure-with-an-image-or-startup-scripts).
See [credentials](#credentials-and-access-inside-the-sandbox) for service access
and [recreation](#explicit-sandbox-recreation) before resetting an existing sandbox.

## Capabilities

`Source`, `StatusReporter`, `LocalSource`, `RuntimeProvider`, `ActionProvider`, `InteractiveAuthenticator`, and `CleanupProvider`.

## Guided setup and your local kit

Run `radar setup` on supported macOS. New installations recommend SBX; repeat
setup prefills the existing choice. Setup checks SBX 0.43.0+, offers `sbx login`
when needed, and installs/verifies pi-sbx 0.6.0+ with consent. Missing or outdated
SBX gets installation/update guidance, not an unattended installer.

When no sandbox recipe has been explicitly configured, setup proposes these
user-owned files under Radar's config directory (`XDG_CONFIG_HOME` is respected):

```text
~/.config/radar/
├── config.yaml
└── sandbox/
    ├── spec.yaml
    ├── sandbox.env
    └── startup.d/
        └── README.md
```

The resulting SBX settings look like this (setup writes your absolute paths):

```yaml
sbx:
  enabled: true
  kit:
    name: /Users/you/.config/radar/sandbox
  env_file: /Users/you/.config/radar/sandbox/sandbox.env
  additional_mounts:
    - /Users/you/.config/radar/sandbox/startup.d:ro
  ready_command: [sandbox-startup, run]
```

`kit.name` is the **complete local kit directory**, not an image name. `kit.path`
is reserved for an additional mixin; it is not needed for this starter.
`spec.yaml` starts from Radar's [kit definition](../../sandbox/kit/spec.yaml),
using `docker.io/christianmoesl/radar-sandbox:latest`, Bash, and the native startup
hook. Edit `sandbox.image` to choose your own image or pin an image digest.
This local file is not automatically replaced by newer published kits.

`sandbox.env` initially contains only:

```dotenv
SBX_STARTUP_DIR=/Users/you/.config/radar/sandbox/startup.d
```

Only `startup.d` is mounted read-only, not the whole config directory. The env-file
is passed to SBX, not mounted. No executable scripts, credentials, extra network
permissions, exposed ports or host Docker socket are installed by the starter.
An empty startup directory succeeds immediately.

With separate consent, setup prepares a disposable sandbox, runs the configured
readiness command, checks Pi's basic sandbox utilities and verifies host↔sandbox
file sharing. It uses a temporary copy of not-yet-created starter files; existing
kits, env-files and mounts are used as configured. **Custom startup commands can
have side effects on services and writable mounts**, even in this temporary
sandbox. Setup does not change network policy or authorize cloud access for you.
First preparation may download an image and take several minutes. Version/status
checks are bounded to 15 seconds, creation to five minutes, readiness to 60
seconds, and the file-sharing check to 30 seconds. Captured provider output is
not displayed because it may contain credentials.

The probe is removed even after failed creation or cancellation. If cleanup
cannot be confirmed, setup stops, retains its temporary mount directory, and
prints the exact sandbox name and recovery instructions. It never removes an
existing workspace sandbox. Downloads, logins, extension installations and
startup-script side effects cannot be rolled back by cancelling setup.

Failures offer retry, an **explicit** choice to continue with SBX disabled, or
cancellation; they never silently enable host execution. The final review shows
the effective workspace default and every proposed starter file. Settings and
starter files are written only after save confirmation. Rerunning setup preserves
explicit kit, env-file, mount and readiness settings—including a previously
configured published kit. Existing starter files are never overwritten, and
concurrent edits to proposed starter files abort saving. Existing env-file
contents are not displayed. Upgrades do not regenerate this user-owned kit.
Linux/WSL setup does not enable managed sandboxes.

### Preconfigure with an image or startup scripts

**Both are supported and can be combined.** Choose the smallest durable change:

| Need | Prefer |
| --- | --- |
| Reusable tools, system packages, expensive installs or a pinned toolchain | Modify/derive the sandbox image |
| Git identity, tool config, lightweight boot-time initialization | Add startup scripts; no image rebuild needed |
| Image choice, entrypoint or native startup behavior | Edit the local `spec.yaml` |

For a startup script, create `startup.d/10-git-config` on the host:

```sh
#!/bin/sh
set -eu
git config --global user.name "Example Developer"
git config --global user.email "developer@example.com"
```

Make it executable with `chmod +x <path-to-script>`. Regular executable files run
in byte-sorted filename order as UID 1000 (`agent`), noninteractively, with
`startup.d` as their working directory. Scripts must be idempotent: they run
after restarts, and a failed run can retry from the beginning. Each is a separate
process, so `export` does not configure later scripts or tools; write persistent
tool configuration instead. Do not put private keys or tokens in scripts.

The image already includes `/usr/local/bin/sandbox-startup`. The native hook
starts `run`, and Radar's generated `ready_command` also invokes `run` to ensure
initialization actually happens. A lock serializes callers; successful startup
is skipped for the remainder of that boot. `wait` only observes state and cannot
start or retry hooks. Early Pi tools independently wait through pi-sbx. Keep the
native hook: Radar's readiness command is not a replacement for early-tool gating.

Editing scripts does not rerun already successful initialization. Restart the
sandbox to apply changes; after a failed run, `sbx exec <name> sandbox-startup run`
retries. Logs stay inside the sandbox at `~/.cache/sandbox-startup/output.log`
and may contain private values. Inspect locally, not in pasted unredacted logs.
See the [full startup contract](../../sandbox/README.md#startup-hooks).

### Build and use your own image

The easiest starting point is a derived image, which inherits Radar's utilities,
non-root user, startup runner and private Docker daemon configuration:

```dockerfile
FROM docker.io/christianmoesl/radar-sandbox:latest
USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends tree \
    && rm -rf /var/lib/apt/lists/*
USER agent
```

Build and publish it to a registry you control (authenticate with that registry
first; replace the example account):

```sh
docker buildx build --platform linux/arm64,linux/amd64 \
  -t ghcr.io/your-account/radar-dev:v1 --push .
```

Alternatively, modify `sandbox/Dockerfile` in a Radar checkout and build from
**the repository root** so `COPY sandbox/startup.py` retains its build context:

```sh
docker buildx build --file sandbox/Dockerfile \
  --platform linux/arm64,linux/amd64 \
  -t ghcr.io/your-account/radar-dev:v1 --push .
```

Then change your local `spec.yaml`:

```yaml
sandbox:
  image: ghcr.io/your-account/radar-dev:v1
  entrypoint: [bash]
```

Keep the rest of the kit, including startup hooks. Validate with
`sbx kit validate /absolute/path/to/your/kit`, then rerun `radar setup` to test it.
For a reproducible environment, pin the image by digest. Publishing is the normal
Radar workflow: SBX's image store is separate from the host Docker daemon, so a
local `docker build --load` alone does not make an image available to SBX. For
manual SBX experiments, `docker save` and `sbx template load` can import an image;
manual `sbx create --pull never ...` avoids registry pulls. Radar does not expose
a pull-policy setting. Prefer a reachable registry reference for managed work.

An unrelated base image must supply Bash, Node.js, `rg`, `fd`, `file`, normal shell
utilities and the `sandbox-startup` contract. If deliberately replacing that
contract, update the kit, `SBX_STARTUP_DIR` and readiness settings together;
pi-sbx will not bypass a configured but missing startup runner. Preserve a
non-root user and avoid putting credentials in image layers or build arguments.

Changes affect the next sandbox creation, not a running sandbox. Existing Radar
workspaces retain their recorded kit/env-file/mount/readiness recipe: editing a
recorded local kit's contents takes effect during approved recreation, but merely
changing the path in global config does not update that recipe. Use a new
workspace for a new recipe, or review [recreation](#explicit-sandbox-recreation)
for existing ones. Save needed VM-local state first. Manual installs inside a
sandbox are disposable, not a durable substitute for images or scripts.

## Credentials and access inside the sandbox

These are three separate authentication layers:

| Purpose | Owner and location |
| --- | --- |
| Run SBX itself | Host Docker session, established with `sbx login` |
| Radar integrations and Pi's model access | Host `gh`, Radar `secrets.yaml`, and Pi's own authentication |
| Tools executing inside the sandbox | Explicit SBX service/custom secrets, SSH-agent forwarding, and network policy |

Host `gh auth login` and `sbx login` do **not** by themselves prove that sandboxed
tools have access. Pi runs on the host: its model credentials need not be copied
into the image. Do not mount `~/.ssh`, `~/.aws`, the entire home/config directory,
or Radar's `secrets.yaml` as a general credential shortcut.

Prefer SBX's host-side secret store/proxy: service and custom secrets can expose
placeholders to the sandbox and substitute the actual credential into permitted
outbound requests. `sbx.env_file` is **not equivalent**: its values are injected
into the runtime and are available to sandbox processes. Keep its default use to
non-secret configuration. Never commit env-files, bake secrets into images, or
publish your local kit directory if it contains private files.

The commands below were checked against **SBX 0.46.0 CLI help**; credential APIs
include experimental features. Check your installed `sbx secret --help` and
subcommand help before use. These examples require your own service permission
and tokens; setup does not execute them or grant cloud access automatically.

### GitHub CLI and API access

On the host, verify your GitHub session, then explicitly authorize SBX to resolve
its token on the host:

```sh
gh auth status
sbx secret set github --command 'gh auth token'
```

This creates a **global** service secret, available across sandboxes. Use a
least-privilege token; add `--sandbox <name>` for a single existing sandbox instead
when isolation is more important than recreatability. Sandbox-scoped secrets
block Radar recreation to avoid their loss; plan their lifecycle first.

Verify from the target sandbox without displaying the token:

```sh
sbx exec <name> gh api user --jq .login
```

Command-secret resolvers run on the **host**, from a fresh temporary directory.
Keep helper scripts and their dependencies outside writable sandbox mounts, use
absolute helper paths, and never mount the host's temporary/config directories
broadly. Do not use `--show-error` or shell tracing around secret resolution.

### Private npm packages

For a custom HTTP package registry, bind the secret to its exact hostname. For
example, with an authenticated host 1Password CLI and your own vault/item:

```sh
sbx secret set-custom --host packages.example.com --env NPM_TOKEN \
  --ref 'op://Development/npm/token'
```

This is also global unless scoped with `--sandbox <name>`. The sandbox receives
`NPM_TOKEN` as a placeholder; the proxy substitutes it in headers for that host.
Do not use `--value`/`--token` on the command line or paste tokens into shell
history. Do not broaden the trusted hosts to fix a failed request without first
checking which registry/download endpoint actually needs authorization.

A startup script can write a non-secret npm configuration referencing the variable:

```sh
#!/bin/sh
set -eu
umask 077
cat > "$HOME/.npmrc" <<'NPMRC'
@acme:registry=https://packages.example.com/
//packages.example.com/:_authToken=${NPM_TOKEN}
NPMRC
```

Use `sbx exec <name> npm whoami --registry=https://packages.example.com/` if the
registry supports that endpoint, or try installing a package you may access.
This recipe depends on HTTP-header placeholder substitution; protocols that
require the real secret locally need a separately reviewed credential mechanism.

### Private container images and kits

Registry pull credentials are distinct from sandbox API credentials. For GHCR,
with an appropriately scoped host token:

```sh
gh auth token | sbx secret set --registry ghcr.io --password-stdin
```

By default these are **host-only** credentials for pulling images and kits. They
are not injected into sandbox tools. Only use `--all-sandboxes` or `--sandbox`
when access from inside the sandbox is intended as well. Configure other registry
hosts and usernames according to `sbx secret set --help`.

### SSH access and Git signing

Keep private keys in the host SSH agent. Check `ssh-add -l` on the host and
`sbx exec <name> ssh-add -l` inside the sandbox to verify forwarding before
relying on it. SBX supplies a sandbox-side socket; never set it to the host's
`SSH_AUTH_SOCK` path. An available agent authorizes use of its keys, even though
private key material remains on the host; only expose the identities you intend.

Startup scripts may configure Git identity, `gpg.format=ssh`, a **public** signing
key and `commit.gpgsign=true`. Verify with a disposable signed commit in a scratch
repository. Do not generate or copy private keys into the kit, image or hooks.

### Network policy, verification and rotation

Credentials do not grant network access. Inspect the applicable policy first:

```sh
sbx policy ls
sbx policy ls <name>
```

If you are authorized to grant an endpoint, a narrowly scoped example is:

```sh
sbx policy allow network --sandbox <name> packages.example.com:443
```

Sandbox-specific policies also block Radar recreation; global rules survive it
but grant broader access. Respect organization-managed policy and do not replace
it with allow-all defaults. Setup never resets or initializes policy for you.

Troubleshoot the failing layer:

- **SBX login/runtime:** `sbx version`, `sbx ls --json`, then `sbx login` if needed.
- **Missing secret/scope:** `sbx secret ls --global` or `sbx secret ls --sandbox <name>`
  shows metadata. Do not use `env`, `printenv`, or token-printing commands as a test.
- **Network denied:** inspect the policy and required destination; a deny rule can
  take precedence over an allow rule.
- **Service rejects access:** verify token validity, scope and organization/SSO
  authorization with a harmless service request; do not print the token.
- **Startup failure:** inspect private runner status/logs locally, fix the script,
  then retry `sandbox-startup run`.

Update/rotate credentials at their host-side source, then repeat the harmless
service check. Do not assume adding a secret changes an existing sandbox:
**a recorded test with SBX 0.46.0 required recreation to apply a newly added global
custom secret, even after a restart.** Dynamic-value refresh and introducing a
new placeholder are different operations. Review `radar recreate-sandboxes
--preview` before an approved recreation. Sandbox-scoped secrets and custom
sandbox policies block that operation; Radar does not export them or bypass
these safeguards. Never delete a sandbox containing scoped credentials or needed
VM-local state just to troubleshoot authentication.

## Configuration and authentication

`sbx.enabled`, `sbx.kit`, `sbx.additional_mounts`, optional `sbx.env_file`, and optional `sbx.ready_command` configure managed runtimes. Repository-local settings use the same shape. Omitted `enabled` automatically enables new workspaces on macOS when `sbx` is on PATH. Explicit repository `enabled` overrides explicit global `enabled`; otherwise automatic detection applies. Explicit enablement with missing tools or unsupported platforms fails before provisioning; runtime/authentication failures never fall back to the host. On dashboard startup, Radar detects reported SBX authentication failures and runs the provider-owned login flow before opening the TUI. `radar create`, `radar fork`, and CLI cleanup of sandbox targets also check authentication before proceeding. The check is bounded and only authentication failures trigger login; missing tools, healthy sessions, and unrelated runtime failures do not. Successful startup login refreshes local sources. Background collection never prompts. You can also sign in manually with `sbx login`.

### Kits and mounts

Guided setup uses the complete local kit described above. Without an explicit
kit, new workspaces use `docker.io/christianmoesl/radar-kit:latest` (SBX 0.43.0+). A user config or repository `.radar.yaml` can add:

```yaml
sbx:
  additional_mounts:
    - ~/shared-tools
  env_file: ~/.config/sbx/sandbox.env
  ready_command: [sandbox-startup, run]
```

Repository `enabled` and `kit` settings override the user settings; repository
mounts are appended to global mounts. `kit.name` selects an agent, sandbox-kit
reference or pinned digest. Optional `kit.path` expands `~/` and is passed as
`--kit <path>`. Additional-mount paths must be absolute or start with `~/`;
Radar creates missing directories and ignores empty, duplicate and redundant
child entries. Existing workspace registrations keep their recorded runtime.

The anchor is the first writable workspace mount. Radar also mounts the private
task directory, external Git common directories and configured/requested mounts;
nested members are already visible through the anchor. Sandbox names are capped
at 63 characters. Pi and editors run on the host; pi-sbx owns tool routing.

Changing effective membership or mounts recreates the sandbox under the same
name, interrupting its processes. Plans warn at 20 or more mounts, without a hard
limit. Transient creation failures get up to three bounded retries, then the
operation retains completed work and reports recovery guidance. Ports are
reconciled with `sbx ports` on host IPv4 loopback. See
[workspace reconciliation](../workspace-reconciliation.md#sbx-mounts) for the
complete desired-state and partial-failure contract.

### Environment files

Pass one machine-local environment file through to SBX with Radar's user config:

```yaml
sbx:
  env_file: ~/.config/sbx/sandbox.env
```

The path must be absolute or begin with `~/`. Radar expands it to a host-native
absolute path and passes separate `--env-file` and path arguments to `sbx create`.
The selected file must exist, be regular, and be readable before provisioning or
destructive sandbox recreation. A missing file does not prevent unrelated
listing or cleanup. Radar does not interpret or log file contents, copy the file
into the sandbox, or expose it as a mount. SBX controls parsing and runtime
injection; the values are not baked into the image. For creation failures with
an env-file, Radar withholds raw SBX diagnostics because a parser error could
echo a private value; retry classification and attempt counts remain available.

A repository's `.radar.yaml` can select another file with the same field. Omission
inherits the user setting; `"env_file": ""` explicitly disables it. Multi-repository
creation uses the first member's repository settings, matching kit selection.
Only one file is selected, not a merged list.

The workspace registry stores the resolved path in `sandbox.env_file`, not the
contents. Missing-runtime recovery and mount-triggered recreation reuse that
recorded selection, including creation retries. Later user/repository setting
changes affect new workspaces only. Editing file contents does not change a
running sandbox or trigger recreation; the next actual creation reads the
current file. Existing version-2 registrations without this field remain valid
and keep no env-file. No automatic migration or backfill is performed. Restart
the Radar daemon after updating the binary so older processes do not drop the
new field when rewriting registrations.

A file can supply generic image inputs such as:

```dotenv
SBX_STARTUP_DIR=/absolute/host/startup.d
```

The directory must be mounted separately, preferably as a read-only requested
workspace mount. On macOS, mounts retain their host absolute paths, so the value
must name the sandbox-visible path. Radar does not expand expressions inside
the file. The development image includes the generic `sandbox-startup` runner;
the kit registers it through SBX's native `setup.startup` hook. Other images
must provide their own initialization. Keep machine-specific files untracked
and private keys in the host's signing agent.

### Optional readiness command

The generated local starter uses `[sandbox-startup, run]`. In hand-written
configuration, omission still means **no Radar readiness command**. To gate a
workspace using the development kit's startup scripts:

```yaml
sbx:
  env_file: ~/.config/sbx/sandbox.env
  ready_command:
    - sandbox-startup
    - run
```

The command is an argv array, not shell text. Radar runs it with
`sbx exec --workdir <anchor> <sandbox> <argv...>` before repository setup and
before releasing auxiliary pane commands. **New sandboxed workspaces start and
switch to host Pi earlier**, after local note/worktrees/shared-directory setup
but before SBX creation. The originating Radar operation continues provisioning;
it still returns final completion, not an early success. Ordinary reopening,
recreation and missing-runtime recovery retain their sandbox-first ordering.
`sbx exec` starts a stopped sandbox first. Failure stops dependent work and is
reported without destroying the early Pi conversation. The bound is 60 seconds and the
parent context can cancel earlier. Raw command output and provider errors are
withheld to avoid logging private values. A readiness failure is not a create
failure: the existing sandbox is retained for inspection/retry, and repository
setup is not marked scheduled.

Repository `ready_command` overrides the user value; an explicit `[]` disables
an inherited command. Omission inherits it. The selected argv is recorded as
`sandbox.ready_command`, participates in the workspace revision, and survives
recovery/recreation independently of later user configuration. Existing
version-2 registrations without this optional field remain unchanged. Restart
the Radar daemon after upgrading before creating records with the new field.

The image's [startup hook contract](../../sandbox/README.md#startup-hooks)
executes regular executable files in byte-sorted filename order as UID 1000,
with no shell sourcing. Readiness is atomically recorded for the current VM
boot ID and PID-1 start time, not merely a persistent `ready` file. Script
failure stops later scripts; output stays in private sandbox-local state.
An unset `SBX_STARTUP_DIR` is an immediate no-op.

SBX native startup commands do not gate the agent's entrypoint, regardless of
`background: false`. Radar's command gates its auxiliary panes and repository
setup, **not the early Pi tools**. Tool readiness is independently owned by
**pi-sbx >=0.6.0**: a nonempty sandbox `SBX_STARTUP_DIR` requires the image's
`sandbox-startup wait --timeout 60` contract before the worker becomes ready.
The helper must exist and report current-boot readiness; failure never enables
implicit host execution. With no startup directory, pi-sbx only checks worker
readiness. Put every tool-critical prerequisite in the image-owned contract;
an arbitrary Radar `ready_command` alone cannot protect early tool calls.
Manual `sbx exec` callers still need to wait explicitly when readiness matters.

### Early Pi launch and recovery

Install `pi-sbx` in the host Pi profile (`pi install npm:@christianmoesl/pi-sbx`)
and keep its extension enabled. Radar's launch-only guard checks the **active**
`/sbx` command, ownership of all seven routed tools, and the owning package's
stable version, not just installation declarations. Missing, old, filtered-out,
or conflicting providers refuse the Pi launch with installation guidance; no
automatic host/slow-path fallback is selected. `/sbx off` remains explicit host
consent within a verified pi-sbx session. `pi-radar` remains separately recommended
for workspace tools/context, not responsible for routing.

Custom agent commands must invoke host Pi, forward `$RADAR_PI_ARGS` including
its required guard extension, and be safe to start before SBX exists. Do not
suppress the guard or do sandbox-dependent work in a wrapper preamble. All
non-agent pane commands keep their layout/cwd and wait on unique, one-shot tmux
gates. Their pending gate names live in pane user options; ordinary open or
reconciliation releases them after readiness, without replaying started commands.
A failed Pi launch retains its pane for diagnostics; normal successful exits
are unchanged. New creation will not reuse an unrelated existing tmux session
whose early-start guard cannot be verified.

If provisioning fails or the creator is terminated after switching, the note,
completed worktrees, early session and completed runtime resources remain for
inspection. Creation writes phase/failure diagnostics to `radar log-path`; inspect
that log and reconcile/open the retained workspace. An early switch explicitly
closes the dashboard popup so it no longer captures keyboard input. Popup
hangup/graceful termination dismisses the TUI but the same Radar process drains
the accepted creation operation before exiting. Force-killing that process or
canceling the operation still interrupts provisioning; there is no detached job.
Managed
open, reconciliation and cleanup serialize against creation; read-only context
inspection remains available. If SBX appears after pi-sbx's 60-second discovery
window, retry explicitly with `/sbx on`; earlier tool calls are never replayed.
Repository setup is still scheduled asynchronously, not awaited to completion.

No registry schema or configuration migration is introduced. The added launch
helper is content-addressed cache data, and pending pane gates are scoped to
fresh tmux panes rather than persisted workspace records.

## Explicit sandbox recreation

`radar recreate-sandboxes` previews and confirms recreation of **existing,
registered Radar-managed sandboxes** under the configured workspace root.
`--workspace <anchor-or-member>` limits the operation to one registered
workspace; it never enrolls an unmanaged sandbox. `--preview` is read-only.
`--yes` explicitly accepts the destructive reset for scripts, independently of
`workspace.auto_confirm`. `--json` only changes output formatting.
There is no TUI action.

```sh
radar recreate-sandboxes --preview
radar recreate-sandboxes
radar recreate-sandboxes --workspace /path/to/workspace
radar recreate-sandboxes --yes --json   # Explicitly approved automation
```

A live test with SBX 0.46.0 confirmed that adding a global custom secret did not
update even fresh executions after a VM reboot, but recreating the sandbox
supplied the new placeholder. Real secret values stay in SBX's host-side secret
store/proxy, not the environment. The same experiment confirmed that removing
the sandbox deletes its sandbox-scoped secret configuration.

Before deletion, Radar checks the registered recipe and runtime identity,
existing mount directories, local kit references, readable env-file, matching
mounts/IPv4 ports, scoped-secret metadata, and policies. Scoped secrets (including
custom and env-only entries) block recreation. Sandbox-specific policies also
block it, except SBX's noneditable, automatically provisioned kit rule. Global
policies are retained by SBX. Unknown or unreadable safety metadata fails closed;
Radar neither exports secret values nor offers an override for these blockers.
Incomplete registrations with only an unknown resolved agent name are blocked:
a custom agent name is not a substitute for its original kit reference.

Recreation uses the **recorded** primary kit, mixin, env-file path, mounts,
readiness command and ports, not newly changed user/repository settings.
SBX reads current env-file contents and resolves the recorded kit reference at
creation time. Only those recorded settings are reproducible; do not depend on
manually added SBX creation options surviving a reset. Changed live mounts or
ports must first be reconciled through Radar. Editing a local kit, env-file or
remote tag can change what creation does, even when the recorded path/reference
is unchanged.

The operation serializes with managed workspace mutations, checks its confirmed
plan again under that lock, and rechecks each runtime's identity and safety
metadata just before removal. External SBX changes do not share Radar's lock;
avoid modifying sandbox configuration while recreation runs.

Sandboxes are rebuilt sequentially using the existing bounded removal/create
retry path (at most three transient-create attempts), with a five-minute limit
per target. Readiness retains its existing 60-second bound. Published ports are
restored and each target's previous running/stopped state is restored on success.
Host worktrees, notes, the workspace registry, and host Pi/tmux sessions are not
replaced. Commands inside the sandbox are interrupted and never replayed;
services must be restarted by their normal startup mechanism.

Per-target results distinguish `recreated`, `blocked`, `failed`, and
`skipped` (absent runtimes). A blocked or failed target makes the command exit 1;
other eligible targets still run. Empty plans and missing-runtime skips are
successful no-ops. Canceling the confirmation changes nothing. A changed plan
requires another invocation and confirmation. Creation failures do not restore
the old sandbox filesystem; readiness/port-restoration failures retain the new
runtime for inspection. Inspect the reported workspace and recover it explicitly
rather than repeatedly resetting healthy targets with a bulk command.

This command adds no configuration or registry schema and requires no migration.
It uses SBX's scoped-secret and policy JSON metadata as exposed by v0.46.0;
versions without compatible inspection fail closed before removal.

## Windows / WSL2

Radar detects Windows Docker Sandboxes when running in WSL2. Install SBX for
Windows, enable WSL interoperability, and expose `sbx.exe` on the WSL PATH:

```sh
sbx.exe version
sbx.exe ls --json
```

A native `sbx` takes precedence if both are installed, matching `pi-sbx` discovery.
Radar selects one installation for each operation and never switches after a
runtime/authentication failure. No executable override or wrapper is required.
Automatic login uses that same selected executable: `sbx login` on macOS or
native Linux, and `sbx.exe login` for Windows SBX from WSL2. Both the Windows
authentication check and login run with `/` as their working directory; login
inherits the foreground terminal's stdin/stdout/stderr and process group. You
can also authenticate manually with `sbx.exe login`. Restart Radar's daemon if
its PATH predates the installation.

Supported operations are collection, task/workspace matching, opening sandbox
shells, and cleanup. `wslpath -u` translates Windows drive and WSL UNC mount paths
to local Linux paths, including custom drive mount roots and paths with spaces.
Read-only mount suffixes are preserved. Sandboxes in other WSL distributions
remain resources that can be opened/removed by name, but their paths do not link
to this distribution's workspaces. Other conversion failures report an error.
Shell text and paths passed to commands **inside** a sandbox are not rewritten.
Windows SBX processes run outside host workspace directories to avoid holding
Windows directory handles that prevent removal.

### Managed workspace limitation

**Detection does not mean managed workspace provisioning is supported.** A real
WSL2 preflight with Windows SBX v0.43.0 verified ordinary file reads/writes, but
both relative and absolute symlinks in WSL-mounted directories failed with
`Invalid argument` from `readlink`/`cat` inside the sandbox. Radar requires its
canonical `notes.md` symlink, so enabling workspace sandboxing would produce a
broken workspace. Windows SBX also exposes mounts at different sandbox paths
(`/wsl.localhost/<distribution>/...` or `/c/...`), affecting absolute Git worktree
pointers and shared-directory references.

New workspace sandboxing therefore remains macOS-only. Explicit enablement on
WSL reports the limitation before provisioning the workspace bundle; existing
Windows-backed managed workspaces cannot be opened/reconciled through Radar.
Collection, sandbox shell actions and cleanup remain available. Radar does not
rewrite host note links/Git metadata, install mount aliases, or silently execute
sandboxed setup on the host. No config, registry, or sandbox filesystem migration
is introduced.

Before enabling full WSL workspace support, validate canonical note links, Git
worktrees with external common directories, shared files, read-only mounts,
recreation, restart, and agent tool routing against a backend that supports the
required filesystem semantics. The separate `pi-sbx` extension's WSL support does
not by itself solve host symlink transport.

## Published development kit

As an alternative to the generated local kit, with SBX 0.43.0 or newer, enabling
`sbx.enabled` without an explicit kit selects `docker.io/christianmoesl/radar-kit:latest` for new workspaces unless a user or repository kit overrides it. No `kit.name` or `kit.path` is required to use this default. Set `sbx.kit.name` only to select another kit or pin a digest. The [sandbox guide](../../sandbox/README.md) documents its tool inventory, Pi requirements, private Docker daemon, publication and rollout.

Sandboxing is automatic on supported systems. Existing `sbx.enabled: false` values remain opt-outs; remove the field manually to adopt automatic detection. Explicit kit settings, including `"shell"` in an older generated configuration, are preserved; remove that override manually to opt into the new default. Existing workspaces retain their recorded kit even during sandbox recreation. No configuration or registry migration is performed.

## Clipboard images and shared screenshots

Radar provisions one private host directory per sandbox-backed workspace:

```text
<host temporary directory>/radar-workspaces/<workspace-id>/
```

The directory and its parent are created with mode `0700`. Only the workspace's child is mounted read/write, at the same absolute path in the sandbox. The shell image needs no special screenshot support: screenshot tools accept an explicit output path. Radar's image includes `file` so `pi-sbx` can recognize image MIME types when reading the resulting files; browser/screenshot tools remain project-specific dependencies.

The recorded `sandbox.shared_directory` is a managed resource, separate from agent-requested `additional_mounts`. New workspaces provision it during creation. An existing workspace without this resource keeps its current behavior until explicit workspace reconciliation provisions it. Preview is read-only and shows the new writable mount and any sandbox recreation; apply persists the path so later host `TMPDIR` changes do not relocate files.

The installed `pi-radar` extension sets **Pi's host-process `TMPDIR`**, on startup and `/reload`, only when workspace introspection confirms the directory exists and the running sandbox mounts it writable. Pi's Ctrl+V handler writes `pi-clipboard-<uuid>.<extension>` there. `RADAR_HOST_TMPDIR` preserves the original host temp root for Radar subprocesses, preventing nested workspaces from allocating directories inside each other's shared storage and keeping Radar's daemon socket and PID paths unchanged. Switching to a workspace without a ready shared directory, or shutting down the active Radar extension, restores the host temp root. Sandbox discovery and tool routing remain owned by `pi-sbx`.

Agent guidance names the shared directory for outgoing screenshots. Use unique filenames and pass the absolute output path to tools such as `playwright-cli screenshot`; do not change the sandbox's `TMPDIR`. This does not alter tmux, other host panes, or the sandbox image's environment. Pi's other temporary files also use this directory.

Shared files survive Pi exits and sandbox recreation but are **temporary**, not durable attachments: the OS may purge them. Workspace cleanup removes the recorded directory, including screenshots, after managed worktrees are removed. Save files worth retaining inside the workspace before cleanup. Cleanup rejects symlinked shared roots and never follows links inside the directory into another workspace.

### Existing installations

Before rollout, inventory `.radar-workspaces.json` and existing shared directories. An absent `shared_directory` means the resource is unprovisioned, not a guessed path; no note or registry reset is needed. Restart the Radar daemon after updating the binary so old processes do not rewrite newer workspace resource state. Reconcile each existing workspace through Radar, then `/reload` its Pi session. Reconciliation may restart the sandbox and interrupt its running processes.

Existing configured mounts are preserved. If configuration explicitly mounts the host's entire `/tmp`, review and remove that mount separately when its other uses are no longer needed; otherwise `/tmp` remains shared despite the new isolated directory. Update any personal screenshot instructions that hardcode `/tmp/screenshots` to prefer Radar's shared directory when available.

## Collection and refs

With SBX installed, local refreshes parse `sbx ls --json` and emit stable sandbox refs linked by name, mark, and primary workspace. Global `sbx.enabled: false` disables collection of unmanaged sandboxes, but registered workspace runtimes are still observed and remain available for explicit cleanup. This preserves existing workspace behavior and repository opt-ins. Changing defaults never adds or removes a sandbox from an existing registration. The runtime capability resolves provider-owned sandbox names. Shell actions use the registered multiplexer rather than invoking tmux themselves.

Cleanup descriptions and opaque resource IDs are provider-owned. Workspace reconciliation preserves mount, port, recreation, and failure behavior; failed runtime recreation does not roll back completed filesystem or Git changes.

## Validation

Authentication tests use fake CLIs to cover macOS and WSL2 executable selection,
Windows working directories, forwarded login streams, expired/healthy sessions,
login failures without backend fallback, and foreground entry points. They do
not authenticate a real account; the WSL2 dispatch tests can run on either OS.

```sh
go test ./cmd/radar ./internal/integration/sbx/... ./internal/integration/workspace/... ./internal/pi
# Extension runtime tests use Node.js with native TypeScript stripping (Node 22.18+).
# Optional macOS/SBX integration tests (each uses disposable resources):
RADAR_SBX_SETUP_E2E=1 go test ./internal/integration/onboarding -run '^TestSandboxSetupE2E$' -count=1 -v -timeout 10m
RADAR_SBX_E2E=1 go test ./internal/integration/workspace -run 'SharedDirectoryRoundTripE2E'
# Optional WSL2/Windows SBX test (creates and removes one disposable sandbox):
RADAR_SBX_WSL_E2E=1 go test ./internal/integration/sbx -run TestWindowsSBXLifecycleE2E -count=1
```
