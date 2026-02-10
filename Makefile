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

.PHONY: all build install snapshot docker test clean help

## default: build all binaries into $(DIST_DIR)/
all: build

## build: compile all binaries into $(DIST_DIR)/ using go build
build:
	for cmd in $(CMDS); do \
		$(GO) build -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/$$cmd ./cmd/$$cmd; \
	done

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
