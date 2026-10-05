# Radar sandbox image and SBX kit

The image is a development environment for Go, Node.js and native builds. Pi,
Radar, tmux and the editor remain on the host; `pi-sbx` routes tool execution into
this environment. There are no model-provider credentials in the image or kit.

## Use the published kit

On macOS, Radar automatically enables sandboxing when `sbx` is installed. Use
SBX 0.43.0 or newer and sign in with `sbx login`. To require sandboxing explicitly,
set this in Radar's user configuration or a repository's `.radar.json`:

```json
{"sbx": {"enabled": true}}
```

Windows SBX is detected from WSL2 for collection, shell actions and cleanup, but
Radar-managed sandbox workspaces are not yet supported there: SBX v0.43.0 cannot
read the required `notes.md` symlink through WSL-mounted directories. See the
[WSL2 validation notes](../docs/integrations/sbx.md#windows--wsl2). Setting
`sbx.enabled: true` reports this limitation; it does not bypass it.

New workspaces default to `docker.io/christianmoesl/radar-kit:latest`; no kit
selection is required. Set `sbx.enabled: false` to opt out; explicit repository
settings override global settings in either direction. Existing explicit false
values are preserved; remove the field to adopt automatic detection. Explicit
user and repository kit settings still override the default kit. If an older generated
configuration contains `"kit": {"name": "shell"}`, remove that override manually
to adopt the new default. Radar does not edit existing user configuration.

For a reproducible environment, set `sbx.kit.name` to the kit's
`docker.io/christianmoesl/radar-kit@sha256:…` reference from the publishing
workflow summary. Each published kit pins its
multi-platform image by digest, so pinning the kit also pins the toolchain.
Do not use the image reference as a kit reference: these are separate artifacts.

Direct SBX usage:

```sh
sbx create --name radar-dev docker.io/christianmoesl/radar-kit:latest "$PWD"
sbx exec radar-dev bash -lc 'go version; node --version; pnpm --version; docker info'
```

The kit does not add host mounts, expose ports, inject credentials, or override
network policy. It inherits the host's SBX policy; package registries and other
project-specific endpoints may need explicit permission under a restrictive
policy. The base image's `com.docker.sandboxes.start-docker=true` label requests
a private Docker daemon inside the SBX microVM. **No host Docker socket is
mounted.** Containers, images and volumes inside that VM are disposable sandbox
state, not host Docker resources.

Changing the configured kit affects newly created workspaces. Existing sandboxes
do not upgrade in place, and existing Radar workspace records retain their
original kit. Recreate an existing workspace only after saving needed files and
accepting the loss of private VM state; this change performs no automatic reset,
configuration migration or sandbox recreation.

## Startup hooks

The image includes a generic `sandbox-startup` runner. The kit registers
`sandbox-startup run` as a native SBX `setup.startup` command, running as UID 1000
(`agent`) on sandbox starts. The native hook records failure but returns success
to SBX so the runtime remains accessible; `sandbox-startup wait` then reports the
failure to dependent callers. It does not replace the upstream container
initialization, proxy environment, Bash initialization, or private Docker daemon.
Image and kit contain no personal Git identities, startup scripts, or private keys.

Provide a machine-local env-file and mount a trusted host directory separately:

```dotenv
SBX_STARTUP_DIR=/absolute/host/startup.d
```

Use the absolute path visible **inside** the sandbox; local macOS mounts retain
the host path. The runner does not expand `$HOME` or `~` in that value. Keep the
directory outside writable project workspaces and mount it read-only. With
Radar, set optional `sbx.env_file` and
`"ready_command": ["sandbox-startup", "wait"]`, and add the directory through
the workspace's requested read-only mount controls. The env-file is not mounted
or baked into the image.

For direct SBX usage:

```sh
sbx create --name startup-dev --env-file /absolute/host/sandbox.env \
  docker.io/christianmoesl/radar-kit:latest "$PWD" /absolute/host/startup.d:ro
sbx exec startup-dev sandbox-startup wait
```

### Script contract

- Only regular executable files directly in `SBX_STARTUP_DIR` run. Directories,
  symlinks and non-executable files are skipped; no file extension is required.
- Filenames use byte order (equivalent to `LC_ALL=C` sorting). Use zero-padded
  prefixes such as `10-git-config` and `20-tools`.
- Files execute as separate processes, using their own shebang. They are not
  sourced; an `export` in one cannot configure later processes. Persist settings
  in the appropriate application config instead.
- The working directory is the startup directory. Standard input is closed;
  hooks must not prompt. They run as the unprivileged `agent` user, not root.
- A nonzero exit stops later hooks and records failure. Use idempotent scripts;
  they rerun after a VM or container restart. Concurrent invocations are locked,
  and an already successful run for the current boot is a no-op.
- Unset/empty `SBX_STARTUP_DIR` makes both `run` and `wait` immediate no-ops. A
  configured missing or relative directory fails initialization.

A hook may configure Git's email and public signing key at runtime. Private keys
stay in a forwarded host SSH agent. The runner is deliberately generic and
contains no Git-specific behavior. Do not set the host's `SSH_AUTH_SOCK` inside
Linux; SBX supplies its own forwarded socket.

### Readiness and diagnostics

Native SBX startup hooks are **asynchronous** and do not block arbitrary `exec`
commands or the agent's entrypoint, even with `background: false`. Radar's optional
`sbx.ready_command` gates its setup/Pi launch; if omitted or `[]`, Radar does not
wait. Other clients must run `sandbox-startup wait` before dependent commands.

`wait` defaults to 60 seconds; `--timeout <seconds>` can shorten the image-side
wait. It exits 0 on success and nonzero on failure or timeout. No stale state
from a previous VM/container boot can pass: state identifies the kernel boot ID
and PID-1 start time. A long-running or interrupted hook may time out without
being killed; the sandbox is retained so it can be inspected or retried.

Private runtime state is under `~/.cache/sandbox-startup/` (directory mode 0700,
files 0600). `status.json` records only boot identity, state, and, on failure,
script filename/exit code. Hook output goes to `output.log`, which can contain
private values and is never copied into the image or emitted by `wait`. Inspect
it locally only when needed. Radar suppresses readiness command diagnostics.
After fixing a failed hook, run `sbx exec <name> sandbox-startup run`, then wait
again; that retries a failed run. Successfully completed hooks rerun only after
a restart, not merely because their host files change. Do not publish snapshots
of a provisioned sandbox containing personal runtime state.

The generic runner is available in the image, but a direct image launch without
the kit must invoke `sandbox-startup run` explicitly. Updating the image/kit does
not modify existing sandboxes or recorded Radar readiness settings automatically.
New workspace creation is the normal adoption path; save private VM state before
any explicit recreation.

## Toolchain

The exact versions and upstream image digests live in [`../Dockerfile`](../Dockerfile):

- **Go:** the latest stable release at the last reviewed update, currently 1.27.1,
  copied from the official Go image. `/usr/local/go/bin` takes precedence over
  the base's distro Go, and `$HOME/go/bin` is on `PATH`.
- **Node:** fnm with Node **24 LTS** installed and selected by default. Node's
  headers are retained for native addons. In Bash, `fnm install <version>` and
  `fnm use <version>` work as the non-root `agent` user. No automatic project
  switching or runtime downloads happen during shell initialization.
- **pnpm:** **12.x** is preloaded through Corepack. Project `packageManager`
  declarations can select another version; downloading an uncached version
  requires network access.
- **Native builds:** `build-essential` and `pkg-config` supply C/C++, make and
  development headers. Python is inherited from the base for tools like node-gyp.
- **Containers:** Docker Engine, containerd, Docker CLI, Compose and Buildx come
  from the `shell-docker` base.

Node is available both through direct `exec`/`sh` and through interactive,
login and non-interactive Bash. Bash gets fnm's per-shell environment without
replacing SBX's existing persistent-environment or proxy initialization.

### Everyday utility inventory

Checked against `docker/sandbox-templates:shell-docker` 0.5.0's package database
and executable paths, not just its description:

| Utilities | Source |
| --- | --- |
| `git`, `ssh`, `curl`, CA certificates | Already in the base |
| `gh`, `jq`, `rg` (ripgrep) | Already in the base |
| `unzip`, `rsync`, `less`, process tools (`ps`, etc.) | Already in the base |
| Bash, `sh`, coreutils, `sed`, `grep`, `find`, `xargs` | Already in the base |
| `make`, `python3` | Already in the base |
| `fd` | Added via `fd-find`, exposed as `fd` as well as the package's `fdfind` |
| `file`, `zip` | Added |
| GCC, G++, libc headers, `pkg-config` | Added |

Upstream also bundles other tools, including distro Node/Go, a JDK and uv. Radar
does not add another JDK or attempt to strip upstream layers; its explicit
`PATH` selects the supported Go and fnm-managed Node installations.

### Pi tool requirements

Pi's normal `find` and `grep` tools use `fd` and `rg`. In the current
[`pi-sbx`](https://github.com/ChristianMoesl/pi-sbx) extension, the sandbox worker
runs on Node and routes operations through Bash, `sh`, `rg`, `file`, `cat` and
`mkdir`. In particular, **`file` is required for image MIME detection**; it is
not included in the upstream shell image. The image supplies all of these.

Pi itself, its extensions, clipboard integration and external editor stay on
the host. Skills or extra tools may have additional dependencies; browsers and
project-specific SDKs are not part of this image's contract.

## Build and validation

```sh
docker buildx build --pull --load -t radar-sandbox:test .
docker run --rm --network none -i radar-sandbox:test bash -s < sandbox/smoke-test.sh
sbx kit validate sandbox/kit
```

`python3 -B sandbox/test_startup.py` runs offline startup-runner tests, also
included in `go test ./scripts`. They cover ordering, ignored files, failures,
concurrent runs, timeout and stale readiness after VM/container restarts.

The smoke test runs as `agent`, checks utility availability and shell startup,
uses fnm and offline pnpm, compiles C/C++, exercises Go/cgo and loads a native
Node addon. It also checks file discovery, search and PNG MIME detection used
by Pi's tools.

On a **disposable Linux test host** with Docker, SBX, accessible `/dev/kvm`, an
SBX login and an initialized network policy, additionally run:

```sh
bash scripts/test-sandbox-runtime.sh radar-sandbox:test
```

This loads the image into SBX's separate image store, creates a sandbox from a
copy of the kit with an env-file and read-only fixture hooks, checks readiness
before direct Node execution and again after stop/start, reruns the smoke test,
and launches `hello-world` using the sandbox's automatically started Docker
daemon. It also checks failed startup cannot reuse the preceding boot's success. It removes only its temporary
sandbox and files. It does not reset other sandboxes or alter network policy.

## Publishing and updates

[The workflow](../.github/workflows/sandbox-image.yml) runs on relevant changes
to `main`, weekly, or manually. Pull requests build and smoke-test both
architectures without publishing or receiving Docker Hub credentials. Trusted
`main` runs additionally initialize a balanced policy on the disposable runner
and test the real SBX runtime before publishing.

The workflow publishes **linux/amd64 and linux/arm64** images and one
architecture-independent kit:

- `docker.io/christianmoesl/radar-sandbox:<build-tag>` and `:latest`
- `docker.io/christianmoesl/radar-kit:<build-tag>` and `:latest`

Build tags contain the UTC date, source revision, workflow run ID and attempt,
so a rebuild never overwrites a previous build tag. The kit is published only
after the image exists, and its `sandbox.image` is rewritten to that image's
multi-platform digest. The kit is signed using GitHub Actions OIDC. Rolling tags
are moved to the already-published manifests rather than rebuilt or repacked.
The workflow summary records both digest references.

The existing `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` secrets need read/write
access to **both** repositories. Make `christianmoesl/radar-kit` public for
anonymous consumption. To require signatures on clients, trust this workflow's
GitHub Actions identity using SBX's kit signer policy.

[`../renovate.json`](../renovate.json) configures update PRs for the upstream
image digests, Go, fnm, Node, pnpm and the publishing SBX CLI. **Enable the Renovate
app for this repository** to activate those PRs. Node stays on 24 and pnpm on 12;
Go follows stable releases, including new major/minor releases. Merging updates
runs the same build and runtime checks. Weekly rebuilds refresh distribution
packages; they do not magically update pinned tool versions or base digests.
