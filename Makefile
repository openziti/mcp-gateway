.PHONY: clean build test

clean:
	go clean ./...
	rm -f $(GOPATH)/bin/mcp-*

build:
	go install ./...

test:
	go test ./... -count=1
	go vet ./...
