BINARY    = pigeon
CMD       = ./cmd/pigeon
PREFIX   ?= $(HOME)
BINDIR    = $(PREFIX)/bin

GIT_SHA  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS   = -s -w -X main.version=$(GIT_SHA)

.PHONY: all build install clean test deps

all: build

build:
	@echo "▶ building $(BINARY) ($(GIT_SHA))..."
	go build -buildvcs=false -ldflags="$(LDFLAGS)" -o $(BINARY) $(CMD)
	@echo "✓ built ./$(BINARY)"

install: build
	mkdir -p $(BINDIR)
	cp $(BINARY) $(BINDIR)/$(BINARY)
	@echo "✓ installed to $(BINDIR)/$(BINARY)"

clean:
	rm -f $(BINARY)

test:
	go test -race ./... -timeout=60s

deps:
	go mod tidy
	go mod download
