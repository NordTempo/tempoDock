# A bare "server" for e2e/fresh-install.sh: a distribution with systemd as
# PID 1 and what a minimal cloud image has (curl, CA certificates, sudo),
# but no Docker and no iptables, so deploy/install.sh installs them as it
# would on a new machine. Started privileged, like e2e/vm/Dockerfile's.
#
#   docker build -f e2e/vm/bare.Dockerfile --build-arg BASE=ubuntu:24.04 -t e2e-bare:ubuntu-24.04 e2e/vm
ARG BASE=debian:trixie
FROM ${BASE}
ENV container=docker DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus ca-certificates curl sudo procps iproute2 \
    && rm -rf /var/lib/apt/lists/* \
    && systemctl mask systemd-logind getty.target console-getty.service
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
