.DEFAULT_GOAL := help

BINARY := octog

.PHONY: all help build test lint fmt clean

all: build

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*?##/ \
	  { printf "  %-10s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the binary
	go build -o $(BINARY) ./cmd/octog

test: ## Run tests
	go test ./...

lint: ## Run golangci-lint (full module)
	golangci-lint run ./...

fmt: ## Format with gci + gofumpt
	golangci-lint fmt ./...

clean: ## Remove built binary
	rm -f $(BINARY)
