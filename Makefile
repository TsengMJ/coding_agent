.PHONY: help test vet fmt build check run

.DEFAULT_GOAL := help

# Package path for tests. Example: make test PKG=./runtime
PKG ?= ./...
# Extra arguments for go test. Example: make test ARGS='-count=1 -v'
ARGS ?=
# Prompt passed to dcode. Example: make run PROMPT='summarize README.md'
PROMPT ?=
# Optional thread id for make run.
THREAD ?=
# Optional model for make run. Example: make run MODEL=openai:gpt-5.5 PROMPT='hi'
MODEL ?=

help: ## Show targets
	@echo "Usage: make <target> [PKG=./...] [ARGS='-v'] [PROMPT='...'] [THREAD=id] [MODEL=provider:model]"
	@echo ""
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-8s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: ## Run tests
	go test $(ARGS) $(PKG)

vet: ## Run go vet
	go vet ./...

fmt: ## Fail if any file is not gofmt-clean
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "$$out"; \
		echo "run: gofmt -w ."; \
		exit 1; \
	fi

build: ## Build all packages and ./dcode
	go build ./...
	go build -o dcode ./cmd/dcode

check: fmt vet test ## Run fmt, vet, and tests

run: ## Run one prompt: make run PROMPT="..."
	@test -n "$(PROMPT)" || { echo 'usage: make run PROMPT="your prompt"'; exit 1; }
	go run ./cmd/dcode $(if $(MODEL),-model=$(MODEL),) $(if $(THREAD),-thread=$(THREAD),) -n "$(PROMPT)"
