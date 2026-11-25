# The public demo's image (ADR 0100): a released dock, dock-demo and the
# demo's content on distroless static (CA roots, /tmp and a nonroot user,
# nothing else). dock-demo runs `dock serve` on the fake runtime, seeds it
# and starts it again from the seed every hour. Built by the release
# workflow from the repository, after the release's binaries are in dist/:
#
#   docker buildx build --platform linux/amd64,linux/arm64 -f deploy/demo/Dockerfile .
#
# Dock runs it as a service with port 7070 and the health check path
# /api/v1/auth/demo, which answers once the demo is seeded.

# dock-demo, cross-compiled on the build machine's platform: no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/dock-demo cmd/dock-demo
COPY internal internal
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -buildid=" -o /out/dock-demo ./cmd/dock-demo
# An empty releases directory and store file the demo's config names
# ([updates] source, [analytics] store_binary), so it never downloads.
RUN mkdir -p /out/demo/releases && touch /out/demo/no-store

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.source=https://github.com/NordTempo/tempoDock \
      org.opencontainers.image.title=dock-demo \
      org.opencontainers.image.description="Tempo Dock's public demo: the real UI and API on a fake runtime, reset every hour"
ARG TARGETARCH
COPY dist/dock-linux-${TARGETARCH} /usr/local/bin/dock
COPY --from=build /out/dock-demo /usr/local/bin/dock-demo
COPY --from=build /out/demo /demo
COPY deploy/demo/config.toml deploy/demo/logs.txt deploy/demo/findings.jsonl /demo/
# The daemon and dock-demo share the service's memory limit.
ENV GOMEMLIMIT=192MiB
EXPOSE 7070
ENTRYPOINT ["/usr/local/bin/dock-demo", "-config", "/demo/config.toml", "-dock", "/usr/local/bin/dock", "-content", "/demo"]
