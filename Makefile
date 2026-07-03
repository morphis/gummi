GOBIN := $(shell go env GOPATH)/bin
export PATH := $(PATH):$(GOBIN)

.PHONY: build test lint golden-update vet ci clean

build:
	go build -o bin/gummi ./cmd/gummi
	go build -tags pin ./...

test:
	go test ./...

vet:
	go vet ./...

lint: vet
	golangci-lint run --timeout=5m

# Regenerate golden files for UI snapshot tests. Only packages that
# import x/exp/golden define the -update flag.
golden-update:
	go test ./internal/ui/... -update

ci: build test lint

clean:
	rm -rf bin
