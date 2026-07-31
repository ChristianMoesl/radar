.PHONY: build build-radar notifier install test dist release clean-dist clean

GO ?= go
BINARY := radar
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
LIBEXECDIR ?= $(PREFIX)/libexec/radar
BUILD_DIR ?= build
NOTIFIER_APP := $(BUILD_DIR)/RadarNotifier.app
DIST_DIR ?= dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
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
	scripts/build-notifier-app.sh "$(NOTIFIER_APP)" "$(VERSION)" "$(HOST_GOARCH)"

install: build
	install -d $(BINDIR)
	install -m 0755 $(BINARY) $(BINDIR)/$(BINARY)
	@if [ "$(HOST_OS)" = "Darwin" ]; then \
		rm -rf "$(LIBEXECDIR)/RadarNotifier.app"; \
		install -d "$(LIBEXECDIR)"; \
		cp -R "$(NOTIFIER_APP)" "$(LIBEXECDIR)/RadarNotifier.app"; \
		"/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister" \
			-f "$(LIBEXECDIR)/RadarNotifier.app"; \
	fi

test:
	$(GO) test ./...

dist: clean-dist
	@set -eu; \
	for target in $(DIST_TARGETS); do \
		goos=$${target%/*}; \
		goarch=$${target#*/}; \
		name="$(BINARY)_$(VERSION)_$${goos}_$${goarch}"; \
		dir="$(DIST_DIR)/$${name}"; \
		mkdir -p "$${dir}/bin"; \
		echo "building $${name}"; \
		GOOS=$${goos} GOARCH=$${goarch} CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags "$(LDFLAGS)" -o "$${dir}/bin/$(BINARY)" ./cmd/radar; \
		if [ "$${goos}" = darwin ]; then \
			if [ "$(HOST_OS)" != Darwin ]; then \
				echo "Darwin release archives must be built on macOS" >&2; \
				exit 1; \
			fi; \
			scripts/build-notifier-app.sh "$${dir}/libexec/radar/RadarNotifier.app" "$(VERSION)" "$${goarch}"; \
		fi; \
		cp README.md "$${dir}/README.md"; \
		cp scripts/install.sh "$${dir}/install.sh"; \
		chmod 0755 "$${dir}/install.sh"; \
		tar -C "$(DIST_DIR)" -czf "$(DIST_DIR)/$${name}.tar.gz" "$${name}"; \
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
