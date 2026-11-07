#!/usr/bin/env bash
# Removes everything e2e/vm/up.sh and the scenarios made on the host: the
# e2e2-* containers with their volumes, the e2e2-net network and, with
# --images, the e2e2-* images. Never touches anything else.
set -uo pipefail
ids=$(docker ps -aq --filter name='^e2e2-')
[[ -z $ids ]] || docker rm -fv $ids >/dev/null
docker network rm e2e2-net >/dev/null 2>&1 || true
if [[ ${1:-} == --images ]]; then
	imgs=$(docker images --format '{{.Repository}}:{{.Tag}}' | grep '^e2e2-' || true)
	[[ -z $imgs ]] || docker rmi $imgs >/dev/null
fi
rm -rf /tmp/e2e2
echo "e2e2 servers removed"
