# tempoBase's image (ADR 0024): the release binary on distroless static,
# which has CA roots and a nonroot user (65532) and nothing else. Built by
# the release workflow from dist/ (scripts/release/build.sh) for both
# architectures:
#
#   docker buildx build --platform linux/amd64,linux/arm64 -f deploy/tempobase/Dockerfile dist
#
# No HEALTHCHECK: it would wake the server every interval. Whoever wants
# one can run `tempobase healthcheck`.

# An empty /data owned by the nonroot user, so a fresh volume is writable.
# Runs on the build machine's platform: no emulation for arm64.
FROM --platform=$BUILDPLATFORM busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e AS data
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.source=https://github.com/NordTempo/tempoDock \
      org.opencontainers.image.title=tempobase \
      org.opencontainers.image.description="tempoBase: browse, query and safely change Postgres data"
ARG TARGETARCH
COPY tempobase-linux-${TARGETARCH} /tempobase
COPY --from=data --chown=65532:65532 /data /data
ENV TEMPOBASE_LISTEN=0.0.0.0:7171 TEMPOBASE_DATA_DIR=/data GOMEMLIMIT=48MiB
VOLUME /data
EXPOSE 7171
ENTRYPOINT ["/tempobase"]
CMD ["serve"]
