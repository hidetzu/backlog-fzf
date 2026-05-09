BINARY  := bkfz
PKG     := ./cmd/bkfz
BIN_DIR := bin

.PHONY: help build run test vet fmt lint tidy clean

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?##/ {printf "  %-8s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the binary into $(BIN_DIR)/$(BINARY)
	go build -o $(BIN_DIR)/$(BINARY) $(PKG)

run: ## Run via go run; pass args with ARGS="..."
	go run $(PKG) $(ARGS)

test: ## Run tests
	go test ./...

vet: ## go vet
	go vet ./...

fmt: ## gofmt -w on all sources
	gofmt -w .

lint: ## Run golangci-lint
	golangci-lint run

tidy: ## go mod tidy
	go mod tidy

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

.DEFAULT_GOAL := help
