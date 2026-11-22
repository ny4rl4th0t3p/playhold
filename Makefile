GOVULNCHECK_VERSION := v1.9.0
# The games the docs import their examples from, each at a commit or tag, checked out into docs/.<name>.
CRAZY_EIGHTS_REF := v0.5.0
CRAZY_EIGHTS_REPO := https://github.com/ny4rl4th0t3p/playhold-crazy-eights.git
TICTACTOE_REF := v0.5.0
TICTACTOE_REPO := https://github.com/ny4rl4th0t3p/playhold-tictactoe.git

# checkout clones repository $(2) into folder $(1), unless it already has commit or tag $(3), and checks that out.
checkout = @dir=$(1); \
	if ! git -C $$dir rev-parse --verify --quiet '$(3)^{commit}' >/dev/null 2>&1; then \
		rm -rf $$dir && git clone --quiet $(2) $$dir; \
	fi; \
	git -C $$dir -c advice.detachedHead=false checkout --quiet '$(3)'
BUF_VERSION := v1.75.0
BUF := go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
# What buf breaking compares with. CI sets this to the commit before the change.
BUF_AGAINST ?= .git\#branch=main

.DEFAULT_GOAL := help

.PHONY: help check build vet gamevet test integration docs crazy-eights tictactoe lint buflint bufbreaking vuln gen fmt tidy

help: ## Show the available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

check: build vet gamevet test docs lint buflint bufbreaking vuln ## Run every check that needs nothing running (see also: integration)

build: ## Build all packages, the wire module's too
	go build ./...
	cd wire && go build ./...

vet: ## Run go vet, on the wire module too
	go vet ./...
	cd wire && go vet ./...

gamevet: ## Run the determinism checks on code built on playhold
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	go build -o "$$tmp/playholdvet" ./cmd/playholdvet && go vet -vettool="$$tmp/playholdvet" ./...

test: ## Run tests with the race detector, without cached results. Needs nothing running
	PLAYHOLD_TEST_POSTGRES= go test -count=1 -race ./...

integration: ## Run the tests that need PostgreSQL, in a throwaway container
	@docker run --rm -d --name playhold-testdb -e POSTGRES_PASSWORD=test -p 127.0.0.1:55432:5432 postgres:18-alpine >/dev/null; \
	trap 'docker stop playhold-testdb >/dev/null' EXIT; \
	until docker exec playhold-testdb pg_isready -q -h 127.0.0.1 -U postgres; do sleep 1; done; \
	PLAYHOLD_TEST_POSTGRES='postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable' go test -count=1 -race ./store/postgres/...

docs: docs/node_modules crazy-eights tictactoe ## Build the documentation site; a dead link or a missing code snippet fails it
	npm --prefix docs run build

crazy-eights: ## Check out Crazy Eights at CRAZY_EIGHTS_REF in docs/.crazy-eights, for the docs' examples
	$(call checkout,docs/.crazy-eights,$(CRAZY_EIGHTS_REPO),$(CRAZY_EIGHTS_REF))

tictactoe: ## Check out the tutorial's game at TICTACTOE_REF in docs/.tictactoe, for the tutorial's pages
	$(call checkout,docs/.tictactoe,$(TICTACTOE_REPO),$(TICTACTOE_REF))

docs/node_modules: docs/package-lock.json
	npm --prefix docs ci
	touch docs/node_modules

lint: ## Run golangci-lint with the repo's config
	golangci-lint cache clean
	golangci-lint run ./...

buflint: ## Lint the .proto files
	$(BUF) lint

bufbreaking: ## Check the .proto files only changed in ways older clients can still read
	$(BUF) breaking --against '$(BUF_AGAINST)'

gen: ## Generate Go code from the .proto files
	$(BUF) generate

vuln: ## Check dependencies and the standard library for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

fmt: ## Format the code with golangci-lint's formatters
	golangci-lint fmt ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy