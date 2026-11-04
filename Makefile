GOVULNCHECK_VERSION := v1.9.0
BUF_VERSION := v1.75.0
BUF := go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)

.DEFAULT_GOAL := help

.PHONY: help check build vet gamevet test web lint buflint vuln gen fmt tidy

help: ## Show the available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z]+:.*## / {printf "  %-8s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

check: build vet gamevet test web lint buflint vuln ## Run everything CI runs

build: ## Build all packages
	go build ./...

vet: ## Run go vet
	go vet ./...

gamevet: ## Run the determinism checks on code built on playhold
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	go build -o "$$tmp/playholdvet" ./cmd/playholdvet && go vet -vettool="$$tmp/playholdvet" ./...

test: ## Run tests with the race detector, without cached results
	go test -count=1 -race ./...

web: sdk/ts/node_modules examples/cards/web/node_modules ## Type-check and test the TypeScript SDK and sample page
	npm --prefix sdk/ts run check
	npm --prefix examples/cards/web run check

sdk/ts/node_modules: sdk/ts/package-lock.json
	npm --prefix sdk/ts ci
	touch sdk/ts/node_modules

examples/cards/web/node_modules: examples/cards/web/package-lock.json
	npm --prefix examples/cards/web ci
	touch examples/cards/web/node_modules

lint: ## Run golangci-lint with the repo's config
	golangci-lint cache clean
	golangci-lint run ./...

buflint: ## Lint the .proto files
	$(BUF) lint
	cd examples/cards && $(BUF) lint

gen: sdk/ts/node_modules ## Generate Go and TypeScript code from the .proto files
	$(BUF) generate
	cd examples/cards && $(BUF) generate

vuln: ## Check dependencies and the standard library for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

fmt: ## Format the code with golangci-lint's formatters
	golangci-lint fmt ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy