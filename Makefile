# bur - common developer commands. Enter the toolchain with `nix develop`.

BINARY  := bur
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)
ARGS    ?=

.DEFAULT_GOAL := help

.PHONY: help build run test vet fmt tidy clean base-image nix-build

help: ## List available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## /{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the bur binary (set BUR_BASE_IMAGE to run a dev build)
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/bur

run: ## Run from source, e.g. make run ARGS="bash"
	go run -ldflags '$(LDFLAGS)' ./cmd/bur $(ARGS)

test: ## Run the test suite
	go test ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format Go source
	gofmt -w .

tidy: ## Tidy go.mod and go.sum
	go mod tidy

clean: ## Remove build artifacts
	rm -f $(BINARY)

base-image: ## Build the bur-base image and load it into podman
	podman load -i $$(nix build --no-link --print-out-paths '.#bur.baseImage')

nix-build: ## Build the release package via the flake
	nix build
