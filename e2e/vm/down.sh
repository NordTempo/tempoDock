#!/usr/bin/env bash
# Removes everything e2e/vm/up.sh and the scenarios made on the host for
# one slot (E2E_SLOT, default e2e2): the $SLOT-* containers with their
# volumes, the $SLOT-net network and, with --images, the e2e2-* images
# (shared by every slot). Never touches anything else.
set -uo pipefail
SLOT=${E2E_SLOT:-e2e2}
[[ $SLOT =~ ^[a-z0-9][a-z0-9-]*$ ]] || { echo "bad E2E_SLOT $SLOT" >&2; exit 1; }
ids=$(docker ps -aq --filter name="^$SLOT-")
[[ -z $ids ]] || docker rm -fv $ids >/dev/null
docker network rm "$SLOT-net" >/dev/null 2>&1 || true
if [[ ${1:-} == --images ]]; then
	imgs=$(docker images --format '{{.Repository}}:{{.Tag}}' | grep '^e2e2-' || true)
	[[ -z $imgs ]] || docker rmi $imgs >/dev/null
fi
rm -rf "/tmp/$SLOT"
echo "$SLOT servers removed"
