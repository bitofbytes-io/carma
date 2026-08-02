-include local.mk

GO ?= go
PORT ?= 4700
DATABASE_URL ?= postgres://carma:carma@localhost:5435/carma?sslmode=disable
REGISTRY ?= registry.tail209cfc.ts.net
IMAGE_REPO ?= carma
TAG ?= dev
PLATFORMS ?= linux/arm64/v8
GOCACHE ?= /tmp/carma-go-cache

.PHONY: run run-postgres test test-integration check-entrypoints lint build migrate db-up db-down docker-build docker-buildx
run:
	APP_ENV=development AUTH_MODE=development DATA_STORE=memory PORT=$(PORT) ASSET_ROOT=.local/carma-assets $(GO) run ./cmd/carma

run-postgres:
	APP_ENV=development AUTH_MODE=development DATA_STORE=postgres DATABASE_URL='$(DATABASE_URL)' PORT=$(PORT) ASSET_ROOT=.local/carma-assets $(GO) run ./cmd/carma

check-entrypoints:
	@for file in cmd/carma/main.go cmd/carma-migrate/main.go cmd/carma-reminders/main.go; do \
		test -f "$$file" || { echo "missing $$file" >&2; exit 1; }; \
		if git check-ignore -q "$$file"; then echo "entrypoint is ignored: $$file" >&2; exit 1; fi; \
	done

test: check-entrypoints
	GOCACHE=$(GOCACHE) $(GO) test ./...

test-integration:
	docker compose -f compose.local.yml up -d --wait postgres
	CARMA_TEST_DATABASE_URL='$(DATABASE_URL)' GOCACHE=$(GOCACHE) $(GO) test -count=1 -run PostgresIntegration ./internal/repository

lint:
	GOCACHE=$(GOCACHE) $(GO) vet ./...

build:
	GOCACHE=$(GOCACHE) $(GO) build ./cmd/carma ./cmd/carma-migrate ./cmd/carma-reminders

migrate:
	APP_ENV=development AUTH_MODE=development DATA_STORE=postgres DATABASE_URL='$(DATABASE_URL)' $(GO) run ./cmd/carma-migrate

db-up:
	docker compose -f compose.local.yml up -d postgres

db-down:
	docker compose -f compose.local.yml down

docker-build:
	docker build -t $(REGISTRY)/$(IMAGE_REPO):$(TAG) .

docker-buildx:
	docker buildx build --platform $(PLATFORMS) -t $(REGISTRY)/$(IMAGE_REPO):$(TAG) -t $(REGISTRY)/$(IMAGE_REPO):latest --push .
