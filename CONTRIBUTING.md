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

## Pi extension

Use Node.js 24+ and install the development dependencies:

```sh
npm ci
npm run check
npm pack --dry-run
pi install /absolute/path/to/radar
```

The package manifest points to `extensions/pi-radar/index.ts`; Pi loads the TypeScript directly. `npm run check` typechecks it and runs unit tests plus an isolated installed-package startup/reload/session-switch test. The latter uses fake Radar commands, separate Pi settings and no model calls. `make test` remains the Go test suite. Run both suites before delivery.

For sandbox development, keep dependencies on the sandbox's local filesystem if the host-mounted filesystem cannot reliably extract npm packages. Do not commit environment-specific dependency paths or registry URLs.

## Build

```sh
make build
```

Install a local build:

```sh
make install
```

## Release

Releases are tag-driven. To publish versioned Linux and macOS binaries from a clean, up-to-date `main`:

```sh
make release VERSION=v0.1.0
```

The release script tests, builds the release archives, creates a signed annotated tag, and pushes it. The release workflow then publishes `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64` tarballs, plus `checksums.txt`, with generated notes from the changes since the previous tag.

Release assets should not be replaced after publishing. If a release is wrong, publish a new patch version.

The sandbox image is released separately because it packages frequently updated tools such as Node, pnpm, and gh. The sandbox image workflow runs weekly and can be triggered manually. It publishes:

```text
christianmoesl/radar-sandbox:YYYY.MM.DD
christianmoesl/radar-sandbox:latest
```

Publishing the sandbox image requires the `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` GitHub secrets.
