# ---------------------------------------------------------------------------
# TripVault - convenience targets
#   make help
# ---------------------------------------------------------------------------

.DEFAULT_GOAL := help
SHELL := /bin/bash

ifneq (,$(wildcard .env))
include .env
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

.PHONY: up
up: ## Start Postgres, Redis and Mailpit
	docker compose up -d
	docker compose ps

.PHONY: down
down: ## Stop infrastructure
	docker compose down

.PHONY: clean-all
clean-all: ## Stop infrastructure and delete volumes
	docker compose down -v

.PHONY: logs
logs: ## Tail infrastructure logs
	docker compose logs -f

.PHONY: env
env: ## Create .env from .env.example if it does not exist, and fill JWT secrets
	@if [ ! -f .env ]; then \
		cp .env.example .env; \
		access=$$(openssl rand -base64 48 | tr -d '\n'); \
		refresh=$$(openssl rand -base64 48 | tr -d '\n'); \
		/usr/bin/sed -i '' "s|^JWT_ACCESS_SECRET=.*|JWT_ACCESS_SECRET=$$access|" .env; \
		/usr/bin/sed -i '' "s|^JWT_REFRESH_SECRET=.*|JWT_REFRESH_SECRET=$$refresh|" .env; \
		echo "created .env with generated JWT secrets"; \
	else \
		echo ".env already exists, leaving it alone"; \
	fi

.PHONY: migrate
migrate: ## Apply database migrations
	$(MAKE) -C backend migrate-up

.PHONY: run-api
run-api: ## Run the Go API
	$(MAKE) -C backend run

.PHONY: test
test: ## Run backend tests
	$(MAKE) -C backend test

.PHONY: test-mobile
test-mobile: ## Run mobile tests
	cd mobile && npm test

.PHONY: install-mobile
install-mobile: ## Install mobile dependencies
	cd mobile && npm install

.PHONY: mobile
mobile: ## Run the Expo dev server (default shows a QR code for a physical device; dev=ios|android|web opens a target directly)
	cd mobile && npm install && npx expo start $(if $(dev),--$(dev),)

.PHONY: check
check: ## Typecheck, lint and test both sides, failing on skipped tests
	$(MAKE) -C backend lint
	$(MAKE) -C backend test-strict
	cd mobile && npm run lint && npm run typecheck && npm test

.PHONY: docker-build
docker-build: ## Build the backend container image
	docker build -t tripvault-backend:dev -f backend/Dockerfile backend

# The overrides are needed because .env points at localhost, which inside a
# container is the container itself. On Linux, add
# --add-host=host.docker.internal:host-gateway if the name does not resolve.
DOCKER_ENV = -e DATABASE_URL=postgresql://postgres:postgres@host.docker.internal:5432/tripvault?sslmode=disable \
             -e REDIS_URL=redis://host.docker.internal:6379/0

.PHONY: docker-run
docker-run: ## Run the backend image against the compose services
	docker run --rm -p 8080:8080 --env-file .env $(DOCKER_ENV) tripvault-backend:dev

.PHONY: docker-migrate-status
docker-migrate-status: ## Print migration status using the backend image
	docker run --rm --env-file .env $(DOCKER_ENV) tripvault-backend:dev -migrate-only=status

