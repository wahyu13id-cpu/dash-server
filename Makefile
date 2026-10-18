# Server Dashboard — build/test/dev helpers
# Usage: make help
SHELL := /bin/bash
GO ?= go
NPM ?= npm
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X dashboard/internal/httpapi.Version=$(VERSION)

export GOFLAGS := -mod=mod

.PHONY: help
help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## Download and tidy Go modules
	$(GO) mod tidy

.PHONY: build
build: ## Build the Go server into ./bin/dashboard
	@mkdir -p bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/dashboard ./cmd/server

.PHONY: build-frontend
build-frontend: ## Install deps and build the React app into frontend/dist
	cd frontend && $(NPM) ci --no-audit --no-fund 2>/dev/null || (cd frontend && $(NPM) install --no-audit --no-fund)
	cd frontend && $(NPM) run build

.PHONY: build-all
build-all: build-frontend build ## Build frontend then backend

.PHONY: run
run: ## Run the server locally (reads .env if present)
	$(GO) run ./cmd/server

.PHONY: dev-frontend
dev-frontend: ## Run the Vite dev server (proxies /api to :8080)
	cd frontend && $(NPM) run dev

.PHONY: test
test: ## Run all Go tests
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run Go tests with the race detector
	$(GO) test -race ./...

.PHONY: test-security
test-security: ## Run the security-focused and regression test suites only
	$(GO) test -run 'Traversal|Symlink|Escape|Auth|Origin|Csrf|CSRF|Credential|Root|Race|Audit|SessionCap|Lifecycle' ./internal/... -v

.PHONY: cover
cover: ## Produce a coverage profile and summary
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: vet ## Lint backend + typecheck frontend
	cd frontend && $(NPM) run lint

.PHONY: fmt
fmt: ## Format Go sources
	$(GO) fmt ./...

.PHONY: quick-install
quick-install: ## One-command production install (run as root: sudo make quick-install)
	@bash install.sh

.PHONY: quick-install-dry-run
quick-install-dry-run: ## Show the installer plan without changing the system
	@bash install.sh --dry-run

.PHONY: installer-self-test
installer-self-test: ## Verify installer helper functions (no system changes)
	@bash install.sh --self-test

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin coverage.out
	rm -rf frontend/dist
