# ---------------------------------------------------------------------------
# TripVault backend
#   make help
# ---------------------------------------------------------------------------

SHELL := /bin/bash
.DEFAULT_GOAL := help

APP        := tripvault-api
CMD_PKG    := ./cmd/server
BIN_DIR    := bin
BIN        := $(BIN_DIR)/$(APP)

# Load .env from the repository root when present.
ifneq (,$(wildcard ../.env))
include ../.env
export
endif

.PHONY: help
help: ## Show this help
	@# $(firstword $(MAKEFILE_LIST)) rather than $(MAKEFILE_LIST): this Makefile
	@# includes .env, which adds it to the list, and grep then prefixes every
	@# match with the filename. awk splits on the first colon, so that made the
	@# first column read "Makefile" for every target.
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(firstword $(MAKEFILE_LIST)) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ------------------------------ build --------------------------------------

.PHONY: build
build: ## Compile the API server
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags "-s -w" -o $(BIN) $(CMD_PKG)
	@echo "built $(BIN)"

.PHONY: run
run: ## Run the API server (hot reload optional: make watch)
	go run $(CMD_PKG)

.PHONY: watch
watch: ## Run with hot reload (requires `air`)
	@command -v air >/dev/null 2>&1 || { echo "air not installed: go install github.com/air-verse/air@latest"; exit 1; }
	air

# ------------------------------ quality ------------------------------------

.PHONY: fmt
fmt: ## Format Go source
	gofmt -s -w .

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: test
test: ## Run all tests (unit + integration, skips integration without Docker)
	go test ./... -count=1

.PHONY: test-strict
test-strict: ## Run all tests and fail if any were skipped
	@# The integration tests skip themselves when PostgreSQL is unreachable, which
	@# is right for a developer without Docker but makes `go test` report "ok" for
	@# a package where nothing actually ran. A green build that skipped the tests
	@# that matter is the failure mode worth engineering against, so this runs
	@# them and then refuses to call it a pass if anything was skipped.
	go test ./... -count=1
	@skipped=$$(go test ./... -count=1 -v 2>&1 | grep -cE '^\s*--- SKIP' || true); \
	if [ "$$skipped" -ne 0 ]; then \
		echo "ERROR: $$skipped test(s) were skipped."; \
		echo "       Start the infrastructure first: make up"; \
		go test ./... -count=1 -v 2>&1 | grep -E '^\s*--- SKIP' | head -20; \
		exit 1; \
	fi; \
	echo "no skipped tests"

.PHONY: test-race
test-race: ## Run all tests with the race detector
	go test ./... -count=1 -race

.PHONY: cover
cover: ## Run tests and write coverage.out
	@# -coverpkg is the whole module, not just each package. Without it Go only
	@# attributes coverage to a package's own statements from that package's own
	@# tests, and since the auth and user logic is exercised through the
	@# integration suite in internal/server, almost all of it reads as 0%. That
	@# understates the real figure and hides genuine gaps in the same breath.
	go test ./... -count=1 -coverpkg=./... -coverprofile=coverage.out -covermode=atomic
	@go tool cover -func=coverage.out | tail -n 1

.PHONY: lint
lint: ## Static analysis (vet + gofmt check + optional golangci-lint)
	@gofmt -l . | grep . && { echo "gofmt: files above need formatting"; exit 1; } || echo "gofmt: clean"
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed, skipped"; fi

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum
	go mod tidy

# ------------------------------ database -----------------------------------

.PHONY: migrate-up
migrate-up: ## Apply pending migrations to $DATABASE_URL
	go run $(CMD_PKG) -migrate-only=up

.PHONY: migrate-down
migrate-down: ## Roll back the most recent migration
	go run $(CMD_PKG) -migrate-only=down

.PHONY: migrate-status
migrate-status: ## Print applied / pending migrations
	go run $(CMD_PKG) -migrate-only=status

.PHONY: migrate-reset
migrate-reset: ## Drop the schema and re-apply every migration (DESTRUCTIVE)
	@read -p "This DROPS the TripVault schema on $$DATABASE_URL. Type 'yes' to continue: " c; [ "$$c" = yes ] || { echo aborted; exit 1; }
	go run $(CMD_PKG) -migrate-only=reset

# ------------------------------ misc ---------------------------------------

.PHONY: gen-jwt-secrets
gen-jwt-secrets: ## Print strong random JWT signing secrets
	@openssl rand -base64 48; openssl rand -base64 48

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out coverage.html
