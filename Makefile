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
#   make engine-embed       pull the latest release's go-librespot Spotify
#                           engine into the embed slot so a LOCAL desktop
#                           build can push it to the box after an OTA. A clean
#                           checkout ships a 0-byte stub, so without this a
#                           fleet roll from a dev build leaves boxes without
#                           Spotify. Restore the stub before committing.
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
# Do not try to keep symbols in the desktop app by removing -s -w here: Wails
# appends its own "-w -s" after ours whenever it builds in production mode
# (wails v2.13.0 pkg/commands/build/base.go:255), so the flags come back and the
# binary is byte for byte identical. Measured 2026-08-17 while looking for ways
# to reduce antivirus false positives on the Windows build.
APP_LDFLAGS := -s -w -X main.appVersion=$(VERSION) -X main.appBuild=$(BUILD_STAMP)
GO          ?= go

# Some Windows make builds do not pass the inherited environment into recipe
# sub-shells: TMP/TEMP vanish (Go dies with "mkdir C:\WINDOWS\go-buildN:
# Zugriff verweigert") and even USERPROFILE/GOPATH disappear ("module cache
# not found"). Recurring since 2026-07-26. Two-part durable fix: pin Go's
# scratch dir to a repo-local folder that needs no environment at all, and
# force-export the variables Go derives its defaults from. Explicitly
# exported make variables DO reach the sub-shell even when plain inherited
# ones do not. Harmless on Linux/macOS/CI (values pass through unchanged).
export GOTMPDIR := $(CURDIR)/.gotmp
$(shell mkdir -p $(CURDIR)/.gotmp)
export HOME USERPROFILE TMP TEMP APPDATA LOCALAPPDATA GOPATH GOMODCACHE GOCACHE GOFLAGS PATH

# Embed targets — must exist before go:embed in desktop-app/agentbin
# and sticksetup respectively. CI overwrites the empty stubs that
# are checked in; these targets do the same locally.
WINFORMAT_OUT := sticksetup/embedded/winformat.exe
AGENT_EMBED_OUT := desktop-app/agentbin/streborn-armv7l
ENGINE_EMBED_OUT := desktop-app/agentbin/go-librespot-armv7l

.PHONY: all build build-arm build-arm64 build-all \
        winformat-embed agent-embed engine-embed winres wails-dev wails-build \
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

# Pull the latest release's go-librespot Spotify engine into the embed slot so a
# LOCAL wails build can re-deliver it to a box after an OTA, exactly like a
# release build does. Only CI fills this slot normally; a clean checkout keeps a
# 0-byte stub, so a fleet roll from a dev build otherwise leaves every box with
# Spotify missing (the agent OTA drops the ~16 MB engine to fit and the dev app
# has nothing to push back). Run this once before `make wails-build` /
# `make wails-dev` when you want a fleet-capable dev build; agent-embed rebuilds
# only the agent, so the fetched engine survives later wails builds.
#
# The filled binary is a tracked stub like the agent one: RESTORE IT with
# `git checkout -- $(ENGINE_EMBED_OUT)` before committing (the release-skill
# triage does this too). Needs the gh CLI and a network connection.
engine-embed:
	@command -v gh >/dev/null 2>&1 || { echo "engine-embed needs the gh CLI (https://cli.github.com)"; exit 1; }
	@tag=$$(gh release view --repo JRpersonal/streborn --json tagName --jq .tagName 2>/dev/null); \
	if [ -z "$$tag" ]; then echo "engine-embed: could not read the latest release tag (is gh authenticated?)"; exit 1; fi; \
	echo "engine-embed: fetching go-librespot-armv7l from release $$tag"; \
	gh release download "$$tag" --repo JRpersonal/streborn -p go-librespot-armv7l -D $(dir $(ENGINE_EMBED_OUT)) --clobber
	@echo "embedded $$(stat -c %s $(ENGINE_EMBED_OUT) 2>/dev/null || stat -f %z $(ENGINE_EMBED_OUT)) bytes into $(ENGINE_EMBED_OUT) -- restore the stub with 'git checkout -- $(ENGINE_EMBED_OUT)' before committing"

# Run the desktop app in dev mode with embedded helpers freshly
# built so format and OTA features actually work locally. The
# `-reloaddirs ..` flag makes wails dev rebuild the Go backend
# when a file in the root module (discovery, internal, cmd)
# changes — not just the desktop-app dir.
wails-dev: winformat-embed agent-embed
	cd desktop-app && wails dev \
		-ldflags "$(APP_LDFLAGS)" \
		-reloaddirs ".."

# Generate the Windows resource ourselves: icon, manifest AND the version
# block. Wails writes the first two and silently skips the third, which left
# every released Windows binary with no publisher, product name or version at
# all. That is what the first-run warning shows, and it is one of the things an
# antivirus heuristic weighs. The Go linker refuses two resource sections, so
# this replaces the Wails one and the build below passes -nopackage. Windows
# only: on macOS packaging is what builds the .app bundle.
winres:
	cd desktop-app && $(GO) run ./cmd/winresgen \
		-out rsrc_windows_amd64.syso \
		-version "$(VERSION)" \
		-comments "SoundTouch Reborn. Unofficial, not affiliated with or endorsed by Bose."

# Production-style local build. Embed slots populated, version
# stamps wired in. Outputs to desktop-app/build/bin/.
wails-build: winformat-embed agent-embed winres
	cd desktop-app && wails build \
		-ldflags "$(APP_LDFLAGS)" \
		-trimpath \
		-clean \
		-nopackage

# Regenerate the supplemental root certificates the agent embeds, from the
# tracked Mozilla bundle. The pinned list lives in the script, so every change
# to what STR trusts is a reviewed edit rather than a silent refresh.
ca-roots:
	python internal/tlsgen/extractroots.py


test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)
	rm -rf desktop-app/build/bin
