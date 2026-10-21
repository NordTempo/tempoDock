#!/usr/bin/env bash
# Run ON the server: samples memory every INTERVAL seconds until killed.
# Columns: time, host available MB, dock daemon RSS MB, dock cgroup MB,
# caddy RSS MB, BuildKit container MB.
#   ssh host 'bash -s' < e2e/monitor.sh > mon.tsv
INTERVAL=${INTERVAL:-2}
rss() { local pid; pid=$(systemctl show -p MainPID --value "$1"); [[ $pid != 0 ]] && awk '/VmRSS/ {printf "%d", $2/1024}' "/proc/$pid/status" 2>/dev/null || echo -; }
printf 'time\tavail_mb\tdock_rss\tdock_cgroup\tcaddy_rss\tbuildkit_mb\n'
while true; do
	avail=$(awk '/MemAvailable/ {printf "%d", $2/1024}' /proc/meminfo)
	cg=$(systemctl show -p MemoryCurrent --value dock)
	[[ $cg =~ ^[0-9]+$ ]] && cg=$((cg >> 20)) || cg=-
	bk=$(docker stats --no-stream --format '{{.MemUsage}}' dock-buildkit 2>/dev/null | cut -d/ -f1 | tr -d ' ')
	printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$(date +%T)" "$avail" "$(rss dock)" "$cg" "$(rss dock-caddy)" "${bk:--}"
	sleep "$INTERVAL"
done
