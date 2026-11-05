# A throwaway "server" for the upgrade scenario (run.sh): Debian 13 with
# systemd as PID 1 and its own Docker, so install.sh, dock.service and
# dock-update.path/.service run as they do on a real control plane.
# Run privileged, with a cgroup namespace and /var/lib/docker on a volume.
FROM debian:trixie
ENV container=docker DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends systemd systemd-sysv dbus docker.io docker-cli \
      iptables ca-certificates curl sqlite3 jq procps less \
 && apt-get clean && rm -rf /var/lib/apt/lists/* \
 && systemctl mask getty@.service systemd-logind.service console-getty.service \
 && systemctl enable docker.service
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
