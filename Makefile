VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/NordTempo/platform/internal/version.Version=$(VERSION)

.PHONY: build web test test-integration lint generate dev

## build: the single static binary (UI embedded if web/dist is built)
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/platform ./cmd/platform

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
