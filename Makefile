BINARY := opencode
MODULE := github.com/gajaai/opencode-go
GOFLAGS ?=

.PHONY: build test lint integration-test clean fmt vet

build:
	go build $(GOFLAGS) -o $(BINARY) ./cmd/opencode

test:
	go test ./... -count=1

lint: fmt vet
	@echo "lint passed"

fmt:
	gofumpt -l -w . 2>/dev/null || gofmt -l -w .

vet:
	go vet ./...

integration-test:
	go test ./internal/dockerrt/ -tags "integration,docker" -v -count=1 -timeout 120s

clean:
	rm -f $(BINARY)
	go clean -testcache
