VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/NordTempo/tempoDock/internal/version.Version=$(VERSION)

.PHONY: build build-cli build-tempobase tempobase-deps release-local web test test-integration lint generate dev perf perf-baseline

## build: the single static binary (UI embedded if web/dist is built)
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/dock ./cmd/dock

## build-cli: the client alone (`-tags dockcli`: no serve/admin/openapi or
## daemon dependencies), for this machine and for macOS and Linux
CLI_PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
build-cli:
	CGO_ENABLED=0 go build -tags dockcli -trimpath -ldflags '$(LDFLAGS)' -o bin/dock-cli ./cmd/dock
	@for p in $(CLI_PLATFORMS); do \
		echo "bin/dock-cli-$${p%/*}-$${p#*/}"; \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build -tags dockcli -trimpath -ldflags '$(LDFLAGS)' -o bin/dock-cli-$${p%/*}-$${p#*/} ./cmd/dock || exit 1; \
	done

## build-tempobase: tempoBase, the standalone Data panel server (ADR 0024),
## with its UI embedded if web/tempobase/dist is built
build-tempobase:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/tempobase ./cmd/tempobase

## tempobase-deps: fail if cmd/tempobase links Dock's daemon: core, api,
## store, node runtime, mcp, cli, Docker, BuildKit or gRPC (ADR 0024
## section 1). CI runs it. Skips while cmd/tempobase doesn't exist yet.
TEMPOBASE_FORBIDDEN = ^github.com/NordTempo/tempoDock/internal/(core|api|store|node|mcp|cli)(/|$$)|^github.com/(moby|docker|containerd|tonistiigi|railwayapp)/|^google.golang.org/grpc(/|$$)
tempobase-deps:
	@if [ ! -d cmd/tempobase ]; then echo "tempobase-deps: skipped, cmd/tempobase is not built yet"; exit 0; fi; \
	deps="$$(go list -deps ./cmd/tempobase)" || exit 1; \
	bad="$$(printf '%s\n' "$$deps" | grep -E '$(TEMPOBASE_FORBIDDEN)')"; \
	if [ -n "$$bad" ]; then printf 'cmd/tempobase links Dock daemon packages (ADR 0024 section 1):\n%s\n' "$$bad" >&2; exit 1; fi; \
	echo "tempobase-deps: ok, no Dock daemon packages"

## release-local: every release artifact into dist/ without publishing
## anything (scripts/release/build.sh): make release-local VERSION=v0.0.0-dev.
## The release workflow runs this on a v* tag and uploads dist/.
release-local: web
	scripts/release/build.sh $(VERSION)

## web: build both UIs: Dock's into web/dist, tempoBase's into
## web/tempobase/dist (`pnpm build` builds both, ADR 0024)
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

# The tempoBase binary, once cmd/tempobase exists (ADR 0024). Until then
# make perf reports its checks as not built yet.
PERF_TEMPOBASE := $(if $(wildcard cmd/tempobase),build-tempobase)

## perf: check performance budgets (docs/performance.md); builds the UIs and binaries first
perf: web build $(PERF_TEMPOBASE)
	go run ./scripts/perf $(PERF_FLAGS)

## perf-baseline: re-record binary sizes and benchmarks in scripts/perf/baseline.json
perf-baseline: web build $(PERF_TEMPOBASE)
	go run ./scripts/perf -update-baseline $(PERF_FLAGS)
