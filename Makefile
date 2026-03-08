.DEFAULT_GOAL := help

BINARY := octog

.PHONY: all help build test test-integration test-browser coverage lint fmt generate clean docs docs-check check

all: build

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*?##/ \
	  { printf "  %-10s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the binary
	go build -o $(BINARY) ./cmd/octog

test: ## Run tests
	go test ./...

test-integration: ## Run integration tests (no browser)
	go test -tags=integration ./...

test-browser: ## Run browser integration tests (requires Chrome)
	go test -tags='integration browser' ./...

coverage: ## Run tests with coverage and check threshold
	go test -coverprofile=coverage.out ./...
	@echo "--- Per-function coverage ---"
	@go tool cover -func=coverage.out | grep -v '^total:' | awk '{print $$NF, $$1}' | sort -n | column -t
	@echo "--- Total ---"
	@go tool cover -func=coverage.out | awk '/^total:/ { print "Coverage: " $$3; pct = $$3+0; if (pct < 50) { print "Coverage " $$3 " below 50% threshold"; exit 1 } }'

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

check: ## Run all pre-push checks (build, test, lint, docs)
	$(MAKE) build
	$(MAKE) test
	$(MAKE) lint
	$(MAKE) docs-check

clean: ## Remove built binary
	rm -f $(BINARY)
