# Contributing to Radar

Radar is a Go project. Contributions should include relevant tests and follow the existing project conventions.

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
