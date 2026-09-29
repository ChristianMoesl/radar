# Contributing to Radar

Radar has a Go CLI and an installable TypeScript Pi extension. Contributions should include relevant tests and follow the existing project conventions.

## Development setup

Install the local development tools:

```sh
brew install go fd git tmux neovim
curl -fsSL https://pi.dev/install.sh | sh
```

Linux developers also need `xdg-open`, usually provided by the system `xdg-utils` package:

```sh
sudo apt-get install xdg-utils
```

Build, test, and install a local Radar binary:

```sh
make test
make build
make install
radar version
```

## Installation regression tests

`go test ./scripts ./internal/app ./internal/config ./internal/collector ./internal/integration/...`
exercises release and source installers, first daemon launch, optional-integration
activation, repository precedence, and missing workspace prerequisites. Fixtures
isolate HOME/XDG directories, PATH and credentials; they never call real remote
services or create real sandboxes. CI runs this suite on Linux and macOS. See
[installation defaults](docs/installation.md) for the supported setup matrix.

## Pi extension

Use Node.js 24+ and the pnpm version pinned by `packageManager` in `package.json` (currently 12.4.2). Radar's sandbox already provides both. Outside it, enable Corepack or install the pinned pnpm version, then install the development dependencies:

```sh
pnpm install --frozen-lockfile
pnpm check
pnpm pack --dry-run
pi install /absolute/path/to/radar
```

The package manifest points to `extensions/pi-radar/index.ts`; Pi loads the TypeScript directly. `pnpm check` typechecks it and runs unit tests, release-version checks, and an isolated packed-package startup/reload/session-switch test. The latter packs the actual npm distribution, verifies its file allowlist and manifest, and loads the extracted artifact without development dependencies. It uses fake Radar commands, separate Pi settings and no model calls. `make test` remains the Go test suite. Run both suites before delivery.

Commit dependency changes with `pnpm-lock.yaml`. `pnpm-workspace.yaml` configures reviewed dependency build-script approvals for this single package; it is not a multi-package workspace. New dependency install scripts must be reviewed rather than globally enabled.

For sandbox development, keep dependencies on the sandbox's local filesystem if the host-mounted filesystem cannot reliably extract packages. Do not commit environment-specific dependency paths or registry URLs.

## Build

```sh
make build
```

Install a local build:

```sh
make install
```

## Release

Releases are tag-driven. The Radar CLI and `@christianmoesl/pi-radar` npm package share a version. First update `package.json.version` to the intended release version (without `v`), commit it, and ensure `main` is clean and up to date. To release:

```sh
make release VERSION=v0.1.1
```

The release script validates the version against `package.json`, installs locked Pi dependencies, runs the Pi and Go suites, builds the release archives, creates a signed annotated tag, and pushes it. The GitHub release workflow repeats the version and test checks, then publishes `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64` tarballs, plus `checksums.txt`, with generated notes from the changes since the previous tag. After the binary release succeeds, a dedicated GitHub-hosted job stages the packed Pi extension with `npm stage publish` using npm trusted publishing. Stable versions target npm's `latest` dist-tag; prereleases target `next`. A successful workflow means the binaries are published and the npm package is awaiting review, not publicly available yet. Review the package in npm's **Staged Packages** tab and approve with 2FA to make it available. The workflow summary records this approval requirement.

Before the first staged release, complete the [npm trusted publishing setup](docs/npm-publishing.md). Leave **Allow npm publish** unchecked; CI needs only stage-only permission, not a GitHub npm publishing secret. Staged publishing requires npm 11.15.0 or newer and an npm account with publish access and 2FA enabled. The release tag must reach the GitHub repository even if development uses another Git remote.

Release assets and npm versions should not be replaced after publishing. If a release is wrong, publish a new patch version. If staging fails after the binary release succeeds, fix the cause and re-run only the failed staging job; do not recreate the GitHub release or move its tag. If staging succeeds, approve or reject that stage rather than re-running the job. See the [staged review and recovery instructions](docs/npm-publishing.md#review-and-approve).

The sandbox image and SBX kit are released together, separately from Radar binaries. The [sandbox workflow](.github/workflows/sandbox-image.yml) builds and smoke-tests both architectures on relevant pull requests and changes to `main`, weekly, or manually. Trusted `main` runs also verify a real SBX sandbox and its private Docker daemon before publishing:

```text
docker.io/christianmoesl/radar-sandbox:<date>-<revision>-<run>-<attempt>
docker.io/christianmoesl/radar-sandbox:latest
docker.io/christianmoesl/radar-kit:<date>-<revision>-<run>-<attempt>
docker.io/christianmoesl/radar-kit:latest
```

Publishing requires the `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` GitHub secrets with read/write access to both repositories. Each kit pins the matching image digest and is signed using GitHub Actions OIDC. See [the sandbox guide](sandbox/README.md) for the tool inventory, fnm/Node and Go defaults, local validation, dependency updates and rollout guidance.
