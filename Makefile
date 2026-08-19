GOBIN := $(shell go env GOPATH)/bin
export PATH := $(PATH):$(GOBIN)

.PHONY: build test lint golden-update vet ci clean demo e2e

build:
	go build -o bin/gummi ./cmd/gummi
	go build -tags pin ./...

test:
	go test ./...

vet:
	go vet ./...

lint: vet
	golangci-lint run --timeout=5m

# Regenerate golden files for snapshot tests. Only packages that import
# x/exp/golden define the -update flag.
golden-update:
	go test ./internal/domain/... -update
	go test ./internal/ui/... -update

ci: build test lint

# Create a throwaway demo repo with gummi initialized.
demo: build
	./scripts/demo.sh

# Drive the real TUI end-to-end in a tmux PTY (needs tmux).
e2e: build
	./scripts/e2e.sh

clean:
	rm -rf bin
