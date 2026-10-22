VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/NordTempo/tempoDock/internal/version.Version=$(VERSION)

.PHONY: build build-cli web test test-integration lint generate dev perf perf-baseline

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

## perf: check performance budgets (docs/performance.md); builds the UI and binary first
perf: web build
	go run ./scripts/perf $(PERF_FLAGS)

## perf-baseline: re-record binary size and benchmarks in scripts/perf/baseline.json
perf-baseline: web build
	go run ./scripts/perf -update-baseline $(PERF_FLAGS)
