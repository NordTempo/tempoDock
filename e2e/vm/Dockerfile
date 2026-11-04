# A throwaway "server" for e2e runs: Debian 13 with systemd as PID 1 and
# Docker Engine inside, so deploy/install.sh and dock.service run as on a
# real machine. Started privileged by e2e/vm/up.sh.
FROM debian:trixie
ENV container=docker DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus ca-certificates curl gnupg iptables iproute2 \
      procps sudo jq less sqlite3 \
    && install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc \
    && echo "deb [signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable" \
       > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends docker-ce docker-ce-cli containerd.io \
    && rm -rf /var/lib/apt/lists/* \
    && systemctl enable docker \
    && systemctl mask systemd-logind getty.target console-getty.service
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
