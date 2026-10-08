.PHONY: help build check agent-check agent-test agent-vet agent-fmt run serve install test-sandbox test-sandbox-linux bench-prepare bench-gate bench-smoke

.DEFAULT_GOAL := help

AGENT := apps/agent
BIN := $(CURDIR)/bin

# Package path for agent tests, relative to apps/agent. Example: make agent-test PKG=./runtime
PKG ?= ./...
# Extra arguments for go test. Example: make agent-test ARGS='-count=1 -v'
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
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build bin/quill and bin/quillbench
	go build -C $(AGENT) ./...
	go build -C $(AGENT) -o $(BIN)/quill ./cmd/quill
	go build -C $(AGENT) -o $(BIN)/quillbench ./cmd/quillbench

check: agent-check ## Run every check

agent-check: agent-fmt agent-vet agent-test ## Run gofmt, go vet and go test on the agent

agent-test: ## Run agent tests
	cd $(AGENT) && go test $(ARGS) $(PKG)

agent-vet: ## Run go vet on the agent
	cd $(AGENT) && go vet ./...

agent-fmt: ## Fail if any agent file is not gofmt-clean
	@out=$$(cd $(AGENT) && gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "$$out"; \
		echo "run: (cd $(AGENT) && gofmt -w .)"; \
		exit 1; \
	fi

# The binaries run from the repository root so the project .env and paths
# resolve here, not inside apps/agent.
run: build ## Run one prompt: make run PROMPT="..."
	@test -n "$(PROMPT)" || { echo 'usage: make run PROMPT="your prompt"'; exit 1; }
	$(BIN)/quill $(if $(MODEL),-model=$(MODEL),) $(if $(THREAD),-thread=$(THREAD),) -n "$(PROMPT)"

serve: build ## Run the agent server for the desktop app: make serve [ADDR=127.0.0.1:8123] [MODEL=...]
	$(BIN)/quill serve -addr=$(ADDR) $(if $(MODEL),-model=$(MODEL),)

install: ## Install quill into $(go env GOPATH)/bin
	go install -C $(AGENT) ./cmd/quill

test-sandbox: ## Run the shell sandbox integration tests on this machine (real sandbox-exec / bwrap)
	cd $(AGENT) && QUILL_SANDBOX_IT=1 go test -count=1 -run Sandbox ./backend/ossandbox/ ./backend/ ./code/agent/

test-sandbox-linux: ## Run the Linux (bubblewrap) sandbox integration tests in a Docker container
	$(AGENT)/scripts/test_sandbox_linux.sh

bench-prepare: build ## Fetch and verify SWE-bench Pro V2 tasks, fill bench_tasks (needs POSTGRES_CONN_STRING)
	$(BIN)/quillbench prepare

bench-gate: build ## Grade gold (oracle) and empty (nop) patches on smoke50; all must pass / fail
	$(BIN)/quillbench gate

bench-smoke: build ## Run quill on smoke50: make bench-smoke [MODEL=provider:model]
	$(BIN)/quillbench run -subset smoke50 $(if $(MODEL),-model=$(MODEL),)
