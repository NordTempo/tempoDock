VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/NordTempo/tempoDock/internal/version.Version=$(VERSION)

.PHONY: build probe build-cli build-tempobase tempobase-deps build-analytics test-analytics lint-analytics analytics-binary caddy-binary test-caddy lint-caddy analytics-deps release-local web test test-race test-integration lint generate dev perf perf-bench perf-artifacts perf-baseline agent-eval

## build: the single static binary (UI embedded if web/dist is built, and
## dock-probe)
build: probe
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/dock ./cmd/dock

## probe: dock-probe, the health probe dock embeds and Docker runs inside
## app containers (ADR 0043), and dock-sync, which copies volumes between
## servers in job containers (ADR 0063), for GOOS/GOARCH from the
## environment, gzipped (dock unpacks them on start-up)
probe:
	CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -buildid=' -o internal/node/probe/bin/dock-probe ./cmd/dock-probe
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags '-s -w -buildid=' -o internal/node/probe/bin/dock-sync ./cmd/dock-sync
	gzip -9nf internal/node/probe/bin/dock-probe internal/node/probe/bin/dock-sync

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

## build-analytics: dock-analytics, Dock Analytics' store (ADR 0037). Its
## own module, with CGO (DuckDB); needs a C compiler
build-analytics:
	cd cmd/dock-analytics && CGO_ENABLED=1 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o ../../bin/dock-analytics .

## test-analytics, lint-analytics: the store's module, which `go test ./...`
## and `golangci-lint run ./...` at the root don't enter
test-analytics:
	cd cmd/dock-analytics && go test ./...
lint-analytics:
	cd cmd/dock-analytics && golangci-lint run ./... && go vet ./...

## analytics-binary: bin/dock-analytics-linux-<arch>, the store as Dock
## runs it (built on Debian 12 in Docker, like the release's); point
## [analytics] store_binary at it to run a development build's store
analytics-binary:
	docker build -f deploy/analytics/Dockerfile --target binary --build-arg VERSION=$(VERSION) --output type=local,dest=bin .

## caddy-binary: bin/dock-caddy, Caddy with Dock's handlers (cmd/dock-caddy,
## ADR 0106), built as the release builds it; install it as caddy (on PATH,
## or /usr/local/bin/caddy) to run Dock's own handlers
caddy-binary:
	cd cmd/dock-caddy && CGO_ENABLED=0 go build -trimpath -buildvcs=false -tags nobadger,nomysql,nopgx -ldflags '-s -w' -o ../../bin/dock-caddy .
test-caddy:
	cd cmd/dock-caddy && go test ./...
lint-caddy:
	cd cmd/dock-caddy && golangci-lint run ./... && go vet ./...

## analytics-deps: fail if cmd/dock links DuckDB or CGO code, or if
## internal/analytics (which dock links) imports anything outside the
## standard library (ADR 0037). CI runs it.
analytics-deps:
	@deps="$$(CGO_ENABLED=0 go list -deps ./cmd/dock)" || exit 1; \
	if printf '%s\n' "$$deps" | grep -qi duckdb; then echo "cmd/dock links DuckDB (ADR 0037)" >&2; exit 1; fi; \
	bad="$$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/analytics | grep -v '^github.com/NordTempo/tempoDock/internal/analytics$$')"; \
	if [ -n "$$bad" ]; then printf 'internal/analytics imports more than the standard library:\n%s\n' "$$bad" >&2; exit 1; fi; \
	echo "analytics-deps: ok, dock links no DuckDB"

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

## test-race: the race detector on the most concurrent packages (CI's race
## jobs); core takes about 5 minutes under -race on a busy runner, so the
## timeout is past go test's 10 minute default. SHARD=i/n runs one of n
## shards of the tests, split by name (scripts/race-shard.sh), as CI does
## with n=2
RACE_PKGS := ./internal/core/... ./internal/api/... ./internal/mcp/...
test-race:
ifdef SHARD
	scripts/race-shard.sh $(SHARD) $(RACE_PKGS)
else
	go test -race -timeout 30m $(RACE_PKGS)
endif

## test-integration: tests against a real Docker Engine (and Caddy, BuildKit)
test-integration: probe
	go test -tags integration -count=1 ./...

lint:
	golangci-lint run ./...
	scripts/ci/suppressions.sh
	scripts/ci/changes_test.sh
	scripts/ci/no-binaries_test.sh

## generate: sqlc queries and the UI's API client
generate:
	go generate ./...
	cd web && pnpm gen:api && pnpm build:recorder

## dev: run the daemon locally on a fake runtime (no Docker), config in dev/
dev:
	DOCK_FAKE_RUNTIME=1 go run ./cmd/dock serve --config dev/config.toml

# The tempoBase binary, once cmd/tempobase exists (ADR 0024). Until then
# make perf reports its checks as not built yet.
PERF_TEMPOBASE := $(if $(wildcard cmd/tempobase),build-tempobase)

## agent-eval: how well AI agents use Dock (scripts/agenteval): real model
## runs through MCP against a fake daemon per case. Not in CI; it uses
## Claude Code's subscription. AGENT_EVAL_FLAGS='-mode cli -cases rollback'
agent-eval:
	go run ./scripts/agenteval $(AGENT_EVAL_FLAGS)

## perf: check performance budgets (docs/performance.md); builds the UIs and binaries first
perf: web build build-analytics $(PERF_TEMPOBASE)
	go run ./scripts/perf $(PERF_FLAGS)

## perf-bench: only the Go benchmarks against the baseline; needs no builds
perf-bench:
	go run ./scripts/perf -checks bench $(PERF_FLAGS)

## perf-artifacts: every check but the benchmarks (CI runs the two as jobs)
perf-artifacts: web build build-analytics $(PERF_TEMPOBASE)
	go run ./scripts/perf -skip bench $(PERF_FLAGS)

## perf-baseline: re-record binary sizes and benchmarks in scripts/perf/baseline.json
perf-baseline: web build build-analytics $(PERF_TEMPOBASE)
	go run ./scripts/perf -update-baseline $(PERF_FLAGS)
