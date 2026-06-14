.PHONY: build install test clean

BINARY := radar
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

build:
	go build -o $(BINARY) ./cmd/radar

install: build
	install -d $(BINDIR)
	install -m 0755 $(BINARY) $(BINDIR)/$(BINARY)

test:
	go test ./...

clean:
	rm -f $(BINARY)
