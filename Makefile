.PHONY: clean build test

GOBIN ?= $(shell go env GOPATH)/bin

clean:
	go clean ./...
	rm -f $(GOBIN)/*

build:
	go install ./...

test:
	go test ./... -count=1
	go vet ./...
