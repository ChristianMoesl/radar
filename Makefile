.PHONY: build build-radar notifier install test dist release clean-dist clean

GO ?= go
BINARY := radar
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
LIBEXECDIR ?= $(PREFIX)/libexec/radar
AGENT_INSTRUCTIONS_TEMPLATE := internal/pi/default-AGENTS.md
BUILD_DIR ?= build
NOTIFIER_APP := $(BUILD_DIR)/RadarNotifier.app
DIST_DIR ?= dist
VERSION ?= $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X radar/internal/version.Number=$(VERSION) -X radar/internal/version.Commit=$(COMMIT) -X radar/internal/version.Date=$(DATE)
RELEASE_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
DIST_TARGETS ?= $(RELEASE_TARGETS)
HOST_OS := $(shell uname -s)
HOST_GOARCH := $(shell $(GO) env GOARCH)

build: build-radar $(if $(filter Darwin,$(HOST_OS)),notifier)

build-radar:
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/radar

notifier:
	@if [ "$(HOST_OS)" != "Darwin" ]; then \
		echo "RadarNotifier.app can only be built on macOS" >&2; \
		exit 1; \
	fi
	scripts/build-notifier-app.sh "$(NOTIFIER_APP)" "$(HOST_GOARCH)"

install: build
	bash -c 'set -euo pipefail; source scripts/install-prerequisites.sh; radar_install_prerequisites'
	install -d "$(BINDIR)"
	install -m 0755 "$(BINARY)" "$(BINDIR)/$(BINARY)"
	install -d "$(PREFIX)/share/radar"
	install -m 0644 LICENSE "$(PREFIX)/share/radar/LICENSE"
	scripts/install-agent-instructions.sh "$(AGENT_INSTRUCTIONS_TEMPLATE)"
	@set -e; if [ "$(HOST_OS)" = "Darwin" ]; then \
		scripts/install-notifier.sh "$(NOTIFIER_APP)" "$(LIBEXECDIR)/RadarNotifier.app"; \
		if [ "$(BINDIR)" = "$(HOME)/.local/bin" ] && [ "$(LIBEXECDIR)" = "$(HOME)/.local/libexec/radar" ]; then rm -f "$(LIBEXECDIR)/install.json"; fi; \
	fi

# Subprocess/PTY fixtures share short deadlines. Bound package concurrency so
# release checks do not exhaust process-launch capacity on macOS.
test:
	$(GO) test -p 2 ./...

dist: clean-dist
	@set -eu; \
	for target in $(DIST_TARGETS); do \
		goos=$${target%/*}; \
		goarch=$${target#*/}; \
		name="$(BINARY)_$(VERSION)_$${goos}_$${goarch}"; \
		dir="$(DIST_DIR)/$${name}"; \
		mkdir -p "$${dir}/bin" "$${dir}/share/radar"; \
		echo "building $${name}"; \
		GOOS=$${goos} GOARCH=$${goarch} CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags "$(LDFLAGS)" -o "$${dir}/bin/$(BINARY)" ./cmd/radar; \
		if [ "$${goos}" = darwin ]; then \
			if [ "$(HOST_OS)" != Darwin ]; then \
				echo "Darwin release archives must be built on macOS" >&2; \
				exit 1; \
			fi; \
			if [ -n "$(NOTIFIER_ARTIFACT_DIR)" ]; then \
				mkdir -p "$${dir}/libexec/radar"; \
				cp -R "$(NOTIFIER_ARTIFACT_DIR)/$${goarch}/RadarNotifier.app" "$${dir}/libexec/radar/RadarNotifier.app"; \
			else \
				scripts/build-notifier-app.sh "$${dir}/libexec/radar/RadarNotifier.app" "$${goarch}"; \
			fi; \
		fi; \
		cp README.md "$${dir}/README.md"; \
		cp LICENSE "$${dir}/LICENSE"; \
		cp $(AGENT_INSTRUCTIONS_TEMPLATE) "$${dir}/share/radar/AGENTS.md"; \
		cp scripts/install-notifier.sh "$${dir}/install-notifier.sh"; \
		{ cat scripts/install-prerequisites.sh; sed '/^source /d' scripts/install.sh; } > "$${dir}/install.sh"; \
		cp scripts/install-agent-instructions.sh "$${dir}/install-agent-instructions.sh"; \
		chmod 0755 "$${dir}/install.sh" "$${dir}/install-agent-instructions.sh"; \
		COPYFILE_DISABLE=1 tar -C "$(DIST_DIR)" -czf "$(DIST_DIR)/$${name}.tar.gz" "$${name}"; \
		if [ "$${goos}" = darwin ]; then node scripts/release-metadata.mjs artifact "$${dir}" "$(DIST_DIR)/$${name}.tar.gz"; fi; \
		rm -rf "$${dir}"; \
	done; \
	cd "$(DIST_DIR)" && shasum -a 256 *.tar.gz > checksums.txt

release:
	@if [ "$(origin VERSION)" != "command line" ]; then \
		echo "usage: make release VERSION=vX.Y.Z" >&2; \
		exit 2; \
	fi
	@scripts/release.sh "$(VERSION)"

clean-dist:
	rm -rf $(DIST_DIR)

clean: clean-dist
	rm -rf $(BUILD_DIR)
	rm -f $(BINARY)
