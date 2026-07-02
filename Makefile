# STR Makefile
# Targets:
#   make build              local binary for the current platform
#   make build-arm          armv7l (SoundTouch 10/20/30, real target)
#   make build-arm64        arm64 (reserve)
#   make build-all          all architectures
#   make winformat-embed    cross-compile the FAT32 helper and drop
#                           it into sticksetup/embedded/ so go:embed
#                           picks it up. Cross-compiles from any host.
#   make agent-embed        same idea for the ARM stick agent that
#                           the desktop app embeds via go:embed.
#   make wails-dev          run the desktop app in dev mode with the
#                           embedded helpers freshly built. The one
#                           command you run for everyday work.
#   make wails-build        production build of the desktop app
#                           with embedded helpers and version stamp.
#   make test               go test ./...
#   make vet                go vet ./...
#   make tidy               go mod tidy
#   make clean              wipe build outputs (keeps stubs)

BINARY      := streborn
PKG         := ./cmd/agent
BIN_DIR     := bin
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_STAMP ?= $(shell date '+%Y-%m-%d-%H%M')
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.buildStamp=$(BUILD_STAMP)
APP_LDFLAGS := -s -w -X main.appVersion=$(VERSION) -X main.appBuild=$(BUILD_STAMP)
GO          ?= go

# Embed targets — must exist before go:embed in desktop-app/agentbin
# and sticksetup respectively. CI overwrites the empty stubs that
# are checked in; these targets do the same locally.
WINFORMAT_OUT := sticksetup/embedded/winformat.exe
AGENT_EMBED_OUT := desktop-app/agentbin/streborn-armv7l

.PHONY: all build build-arm build-arm64 build-all \
        winformat-embed agent-embed wails-dev wails-build \
        test vet tidy clean

all: build

build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(PKG)

# GOARM=5, not 7, on purpose. Some early SoundTouch units (seen on an
# older ST20 on 2017 firmware, issue #302) have a CPU/kernel without
# working VFP hardware float. A GOARM=6/7 binary emits VFP instructions
# and SIGILLs at the first stdlib float touch (os.init), crash-looping
# the agent and soft-bricking the box. GOARM=5 is pure software float
# with kernel-helper atomics: no ARMv7-optional instructions, so it runs
# on every SoundTouch CPU revision (a compat superset of GOARM=7). The
# agent does no heavy FP, so the softfloat cost is negligible. Keep this
# in sync with release.yml / build.yml (goarm matrix) and agent-embed.
build-arm:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm GOARM=5 CGO_ENABLED=0 \
		$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY)-armv7l $(PKG)

build-arm64:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY)-arm64 $(PKG)

build-all: build build-arm build-arm64

# Cross-compiled, no CGO — works from Windows, Linux or macOS host.
# Drops the real binary into the embed slot so the next `go build`
# of the package picks it up; without this the stub stays empty
# and sticksetup.formatVolume errors with "winformat Helper fehlt".
winformat-embed:
	@mkdir -p $(dir $(WINFORMAT_OUT))
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
		$(GO) build -trimpath -ldflags="-s -w" -o $(WINFORMAT_OUT) ./cmd/winformat
	@echo "embedded $$(stat -c %s $(WINFORMAT_OUT) 2>/dev/null || stat -f %z $(WINFORMAT_OUT)) bytes into $(WINFORMAT_OUT)"

# Cross-compile the stick agent for the real ARMv7l target and
# drop it into desktop-app/agentbin so the desktop app's go:embed
# picks it up. Required for OTA-from-app to actually push a binary.
agent-embed:
	@mkdir -p $(dir $(AGENT_EMBED_OUT))
	GOOS=linux GOARCH=arm GOARM=5 CGO_ENABLED=0 \
		$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(AGENT_EMBED_OUT) $(PKG)
	@echo "embedded $$(stat -c %s $(AGENT_EMBED_OUT) 2>/dev/null || stat -f %z $(AGENT_EMBED_OUT)) bytes into $(AGENT_EMBED_OUT)"

# Run the desktop app in dev mode with embedded helpers freshly
# built so format and OTA features actually work locally. The
# `-reloaddirs ..` flag makes wails dev rebuild the Go backend
# when a file in the root module (discovery, internal, cmd)
# changes — not just the desktop-app dir.
wails-dev: winformat-embed agent-embed
	cd desktop-app && wails dev \
		-ldflags "$(APP_LDFLAGS)" \
		-reloaddirs ".."

# Production-style local build. Embed slots populated, version
# stamps wired in. Outputs to desktop-app/build/bin/.
wails-build: winformat-embed agent-embed
	cd desktop-app && wails build \
		-ldflags "$(APP_LDFLAGS)" \
		-trimpath \
		-clean

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)
	rm -rf desktop-app/build/bin
