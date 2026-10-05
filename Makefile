.PHONY: help test vet fmt build check run serve install web web-test bench-prepare bench-gate bench-smoke

.DEFAULT_GOAL := help

# Package path for tests. Example: make test PKG=./runtime
PKG ?= ./...
# Extra arguments for go test. Example: make test ARGS='-count=1 -v'
ARGS ?=
# Prompt passed to quill. Example: make run PROMPT='summarize README.md'
PROMPT ?=
# Optional thread id for make run.
THREAD ?=
# Optional model for make run / make serve. Example: make run MODEL=openai:gpt-5.5 PROMPT='hi'
MODEL ?=
# Listen address for make serve (loopback only).
ADDR ?= 127.0.0.1:8123

help: ## Show targets
	@echo "Usage: make <target> [PKG=./...] [ARGS='-v'] [PROMPT='...'] [THREAD=id] [MODEL=provider:model]"
	@echo ""
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

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

build: ## Build all packages and ./quill
	go build ./...
	go build -o quill ./cmd/quill

check: fmt vet test ## Run fmt, vet, and tests

run: ## Run one prompt: make run PROMPT="..."
	@test -n "$(PROMPT)" || { echo 'usage: make run PROMPT="your prompt"'; exit 1; }
	go run ./cmd/quill $(if $(MODEL),-model=$(MODEL),) $(if $(THREAD),-thread=$(THREAD),) -n "$(PROMPT)"

serve: ## Run the agent server: make serve [ADDR=127.0.0.1:8123] [MODEL=...]
	go run ./cmd/quill serve -addr=$(ADDR) $(if $(MODEL),-model=$(MODEL),)

install: ## Install quill into $(go env GOPATH)/bin
	go install ./cmd/quill

web: ## Rebuild the GUI bundle into code/gui/dist (needs Node)
	cd code/gui/web && npm ci && npm run build

web-test: ## Typecheck and test the GUI frontend (needs Node)
	cd code/gui/web && npm ci && npm run typecheck && npm test

bench-prepare: ## Fetch and verify SWE-bench Pro V2 tasks, fill bench_tasks (needs POSTGRES_CONN_STRING)
	go run ./cmd/quillbench prepare

bench-gate: ## Grade gold (oracle) and empty (nop) patches on smoke50; all must pass / fail
	go run ./cmd/quillbench gate

bench-smoke: ## Run quill on smoke50: make bench-smoke [MODEL=provider:model]
	go run ./cmd/quillbench run -subset smoke50 $(if $(MODEL),-model=$(MODEL),)
