.DEFAULT_GOAL := help

BINARY := octog

.PHONY: all help build test test-integration lint fmt generate clean docs docs-check

all: build

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*?##/ \
	  { printf "  %-10s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the binary
	go build -o $(BINARY) ./cmd/octog

test: ## Run tests
	go test ./...

test-integration: ## Run integration tests
	go test -tags=integration ./...

lint: ## Run golangci-lint (full module)
	golangci-lint run ./...

generate: ## Run go generate
	go generate ./...

fmt: ## Format with gci + gofumpt
	golangci-lint fmt ./...

docs: ## Sync embedded code in docs
	go run github.com/campoy/embedmd/v2@latest -w docs/*.md

docs-check: ## Verify docs are in sync with code
	go run github.com/campoy/embedmd/v2@latest -d docs/*.md

clean: ## Remove built binary
	rm -f $(BINARY)
