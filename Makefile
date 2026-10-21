VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/NordTempo/tempoDock/internal/version.Version=$(VERSION)

.PHONY: build web test test-integration lint generate dev

## build: the single static binary (UI embedded if web/dist is built)
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/dock ./cmd/dock

## web: build the UI into web/dist
web:
	cd web && pnpm install --frozen-lockfile && pnpm build

## test: unit tests (no Docker needed)
test:
	go test ./...

## test-integration: tests against a real Docker Engine (and Caddy, BuildKit)
test-integration:
	go test -tags integration -count=1 ./...

lint:
	golangci-lint run ./...

## generate: sqlc queries and the UI's API client
generate:
	go generate ./...
	cd web && pnpm gen:api

## dev: run the daemon locally on a fake runtime (no Docker), config in dev/
dev:
	DOCK_FAKE_RUNTIME=1 go run ./cmd/dock serve --config dev/config.toml
