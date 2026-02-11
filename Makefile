SHELL := bash
.SHELLFLAGS := -o pipefail -euc

# goreleaser uses ./dist by default
DIST_DIR     ?= dist
GO           ?= go
GORELEASER   ?= goreleaser
MODULE       := github.com/openziti/mcp-gateway
CMDS         := mcp-gateway mcp-bridge mcp-tools
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
HASH         ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE         ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS      := -s -w \
                -X $(MODULE)/build.Version=$(VERSION) \
                -X $(MODULE)/build.Hash=$(HASH) \
                -X $(MODULE)/build.Date=$(DATE) \
                -X $(MODULE)/build.BuiltBy=make

# release matrix matching goreleaser configs
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build build-all install snapshot docker test clean help

## default: build all binaries into $(DIST_DIR)/
all: build

## build: compile all binaries into $(DIST_DIR)/ using go build
build:
	for cmd in $(CMDS); do \
		$(GO) build -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/$$cmd ./cmd/$$cmd; \
	done

## build-all: cross-compile all binaries for every release platform
build-all:
	@for platform in $(PLATFORMS); do \
		os=$${platform%%/*}; arch=$${platform##*/}; \
		for cmd in $(CMDS); do \
			out=$(DIST_DIR)/$$os/$$arch/$$cmd; \
			printf "%-20s %s\n" "$$os/$$arch" "$$cmd"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
				$(GO) build -ldflags '$(LDFLAGS)' -o $$out ./cmd/$$cmd; \
		done; \
	done
	@echo "done: $$(find $(DIST_DIR) -type f | wc -l) artifacts in $(DIST_DIR)/"

## install: go install all commands into $GOPATH/bin
install:
	$(GO) install -ldflags '$(LDFLAGS)' ./...

## snapshot: goreleaser snapshot build for the native platform (tolerates dirty working copy)
snapshot:
	@cfg=".goreleaser-$$($(GO) env GOOS)-$$($(GO) env GOARCH).yml"; \
	[ -f "$$cfg" ] || cfg=".goreleaser-$$($(GO) env GOOS).yml"; \
	echo "$(GORELEASER) build --clean --snapshot --single-target --config $$cfg"; \
	$(GORELEASER) build --clean --snapshot --single-target --config "$$cfg"

DOCKER_REPO  ?= openziti/mcp-gateway
DOCKER_TAG   ?= local

## docker: build container image for the native platform
docker: build
	@arch=$$($(GO) env GOARCH); os=$$($(GO) env GOOS); \
	mkdir -p $(DIST_DIR)/$$arch/$$os; \
	for cmd in $(CMDS); do \
		cp -f $(DIST_DIR)/$$cmd $(DIST_DIR)/$$arch/$$os/$$cmd; \
	done; \
	docker build \
		--build-arg ARTIFACTS_DIR=./$(DIST_DIR) \
		-f docker/images/mcp-gateway/Dockerfile \
		-t $(DOCKER_REPO):$(DOCKER_TAG) .

## test: run all tests
test:
	$(GO) test -v ./...

## clean: remove build artifacts
clean:
	rm -rf $(DIST_DIR)

## help: show this help
help:
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':'
