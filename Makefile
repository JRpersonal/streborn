# STR Makefile
# Ziele:
#   make build         lokales Binary für die aktuelle Plattform
#   make build-arm     armv7l (SoundTouch 10/20/30)
#   make build-arm64   arm64 (Reserve)
#   make build-all     alle Architekturen
#   make test          go test
#   make vet           go vet
#   make tidy          go mod tidy
#   make clean         bin/ aufräumen

BINARY      := streborn
PKG         := ./cmd/agent
BIN_DIR     := bin
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
GO          ?= go

.PHONY: all build build-arm build-arm64 build-all test vet tidy clean

all: build

build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(PKG)

build-arm:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
		$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY)-armv7l $(PKG)

build-arm64:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY)-arm64 $(PKG)

build-all: build build-arm build-arm64

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)
