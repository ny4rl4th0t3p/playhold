GOVULNCHECK_VERSION := v1.9.0

.DEFAULT_GOAL := help

.PHONY: help check build vet test lint vuln fmt tidy

help: ## Show the available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z]+:.*## / {printf "  %-8s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

check: build vet test lint vuln ## Run everything CI runs

build: ## Build all packages
	go build ./...

vet: ## Run go vet
	go vet ./...

test: ## Run tests with the race detector, without cached results
	go test -count=1 -race ./...

lint: ## Run golangci-lint with the repo's config
	golangci-lint cache clean
	golangci-lint run ./...

vuln: ## Check dependencies and the standard library for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

fmt: ## Format the code with golangci-lint's formatters
	golangci-lint fmt ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy