.PHONY: clean build test e2e

GOBIN ?= $(shell go env GOPATH)/bin

clean:
	go clean ./...
	rm -f $(GOBIN)/*

build:
	go install ./...

test:
	go test ./... -count=1
	go vet ./...

# hand-run smoke suite; requires live zrok and agora environments.
# see docs/current/smoke-suite.md
e2e:
	go vet -tags e2e ./e2e/
	go test -tags e2e -count=1 -v -timeout 30m ./e2e/
