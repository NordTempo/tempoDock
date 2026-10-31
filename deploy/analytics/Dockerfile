# dock-analytics, Dock Analytics' store (ADR 0037): DuckDB behind a small
# HTTP API, run by Dock as an infrastructure container on the analytics
# server. Built from the repository root:
#
#   docker build -f deploy/analytics/Dockerfile -t dock-analytics:dev .
#
# It links DuckDB with CGO, so it builds on the target's own platform
# (arm64 under emulation with buildx); the prebuilt DuckDB library comes
# with the Go module, nothing is compiled from C++ here. The result needs
# libstdc++, which distroless cc has.
#
# No HEALTHCHECK: it would wake the store every interval. Dock asks
# /v1/health when it needs to know.

FROM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS build
WORKDIR /src
# The module replaces the main module with ../.., which needs its go.mod.
COPY go.mod go.sum ./
COPY cmd/dock-analytics/go.mod cmd/dock-analytics/go.sum cmd/dock-analytics/
RUN cd cmd/dock-analytics && go mod download
COPY internal/analytics internal/analytics
COPY cmd/dock-analytics cmd/dock-analytics
ARG VERSION=dev
RUN cd cmd/dock-analytics && CGO_ENABLED=1 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" -o /out/dock-analytics . \
 && mkdir /out/data

FROM gcr.io/distroless/cc-debian12:nonroot@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f
LABEL org.opencontainers.image.source=https://github.com/NordTempo/tempoDock \
      org.opencontainers.image.title=dock-analytics \
      org.opencontainers.image.description="Dock Analytics' store: DuckDB behind Dock's loopback API"
COPY --from=build /out/dock-analytics /dock-analytics
# An empty /data owned by the nonroot user, so a fresh volume is writable.
COPY --from=build --chown=65532:65532 /out/data /data
ENV DOCK_ANALYTICS_DB=/data/analytics.duckdb DOCK_ANALYTICS_LISTEN=:7076 GOMEMLIMIT=48MiB
VOLUME /data
EXPOSE 7076
ENTRYPOINT ["/dock-analytics"]
CMD ["serve"]
