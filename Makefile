.PHONY: build install test dist clean-dist clean

GO ?= go
BINARY := radar
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
DIST_DIR ?= dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X radar/internal/version.Number=$(VERSION) -X radar/internal/version.Commit=$(COMMIT) -X radar/internal/version.Date=$(DATE)
RELEASE_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

build:
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/radar

install: build
	install -d $(BINDIR)
	install -m 0755 $(BINARY) $(BINDIR)/$(BINARY)

test:
	$(GO) test ./...

dist: clean-dist
	@set -eu; \
	for target in $(RELEASE_TARGETS); do \
		goos=$${target%/*}; \
		goarch=$${target#*/}; \
		name="$(BINARY)_$(VERSION)_$${goos}_$${goarch}"; \
		dir="$(DIST_DIR)/$${name}"; \
		mkdir -p "$${dir}"; \
		echo "building $${name}"; \
		GOOS=$${goos} GOARCH=$${goarch} CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags "$(LDFLAGS)" -o "$${dir}/$(BINARY)" ./cmd/radar; \
		cp README.md "$${dir}/README.md"; \
		tar -C "$(DIST_DIR)" -czf "$(DIST_DIR)/$${name}.tar.gz" "$${name}"; \
		rm -rf "$${dir}"; \
	done; \
	cd "$(DIST_DIR)" && shasum -a 256 *.tar.gz > checksums.txt

clean-dist:
	rm -rf $(DIST_DIR)

clean: clean-dist
	rm -f $(BINARY)
