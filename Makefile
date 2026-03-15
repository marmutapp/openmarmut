BINARY := openmarmut
MODULE := github.com/marmutapp/openmarmut
GOFLAGS ?=

.PHONY: build test lint integration-test clean fmt vet install release release-dry

build:
	go build $(GOFLAGS) -o $(BINARY) ./cmd/openmarmut

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

install:
	go install ./cmd/openmarmut

release-dry:
	goreleaser release --snapshot --clean

release:
	goreleaser release --clean

clean:
	rm -f $(BINARY)
	go clean -testcache
