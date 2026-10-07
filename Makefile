BINARY  := bkfz
PKG     := ./cmd/bkfz
BIN_DIR := bin
DEMO_DIR := tmp/demo

.PHONY: help build run test vet fmt lint tidy clean demo

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

demo: build ## Seed fictional data into $(DEMO_DIR) and record docs/demo.gif with VHS
	go run ./scripts/demo-seed "$(abspath $(DEMO_DIR))"
	BKFZ_DEMO_DIR="$(abspath $(DEMO_DIR))" PATH="$(CURDIR)/$(BIN_DIR):$$PATH" vhs docs/demo.tape

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) $(DEMO_DIR)

.DEFAULT_GOAL := help
