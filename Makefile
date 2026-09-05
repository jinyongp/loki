GO ?= go
GOFMT ?= gofmt
BINARY ?= .tmp/bin/loki
PYTHON_REFERENCE ?= .tmp/python-baseline/bin/python

.PHONY: build fmt fmt-check test race vet check reference sandbox

build: fmt-check
	mkdir -p $(dir $(BINARY))
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BINARY) ./cmd/loki

fmt:
	$(GOFMT) -w cmd internal

fmt-check:
	@command -v "$(GOFMT)" >/dev/null
	@test -z "$$($(GOFMT) -l cmd internal)"

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

check: fmt-check vet test race build

# Required isolated namespace acceptance; unavailable confinement is a failure.
sandbox:
	LOKI_REQUIRE_SANDBOX_TESTS=1 $(GO) test -count=1 -v ./internal/action ./internal/service -run '^Test(SandboxExecutionAndGuards|ActionSandboxMCP|NodeActionSandbox|MaterializationSandbox|MaterializationMountGuards|MaterializationRecoveryAndPreservation|MaterializationSessionRecoveryAndCompletion|ActionLockProbe)$$'

# The reference is test-only; Go packages/builds do not require Python.
reference:
	@test -x "$(PYTHON_REFERENCE)" || { echo 'Create the isolated Python reference environment documented in docs/go-migration-plan.md'; exit 1; }
	LOKI_REFERENCE_PYTHON="$(abspath $(PYTHON_REFERENCE))" $(GO) test -count=1 -v ./internal/project ./internal/secret ./internal/process ./internal/action ./internal/skills ./internal/gitops -run '^TestPython0471.*Differential$$'
	GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 "$(PYTHON_REFERENCE)" -m pytest -q tests/test_project_state.py tests/test_task_mcp.py tests/test_process_limits.py
