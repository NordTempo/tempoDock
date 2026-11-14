# dock-analytics, Dock Analytics' store (ADR 0037), built for the
# container Dock runs it in: gcr.io/distroless/cc-debian12 with the binary
# mounted read-only (internal/node/docker/analytics.go). It links DuckDB
# with CGO, so it builds on Debian 12, whose C library the base image has;
# a binary built on a newer system may not start there. The prebuilt
# DuckDB library comes with the Go module, nothing is compiled from C++.
#
# From the repository root, for this machine's platform:
#
#   make analytics-binary     # bin/dock-analytics-linux-<arch>
#
# The release builds one per platform, each on its own runner.

FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
# The module replaces the main module with ../.., which needs its go.mod.
COPY go.mod go.sum ./
COPY cmd/dock-analytics/go.mod cmd/dock-analytics/go.sum cmd/dock-analytics/
RUN cd cmd/dock-analytics && go mod download
COPY internal/analytics internal/analytics
COPY cmd/dock-analytics cmd/dock-analytics
ARG VERSION=dev
ARG TARGETARCH
RUN cd cmd/dock-analytics && CGO_ENABLED=1 go build -trimpath \
      -ldflags "-s -w -buildid= -X main.version=${VERSION}" -o /out/dock-analytics-linux-${TARGETARCH} .

FROM scratch AS binary
COPY --from=build /out/ /
