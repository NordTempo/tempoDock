#!/usr/bin/env bash
# Times pushing and pulling a ~300 MB image, then a one-layer change, to a
# registry:3 on local disk and to one on S3-compatible storage (ADR 0034).
# The numbers go in docs/experience.md.
#
#   scripts/registry-spike.sh                # local disk vs a MinIO container
#   S3_ENDPOINT=https://fsn1.your-objectstorage.com S3_REGION=fsn1 S3_BUCKET=dock-spike \
#   S3_ACCESS_KEY_ID=... S3_SECRET_ACCESS_KEY=... scripts/registry-spike.sh
#
# It runs its own registries (no auth) on loopback ports and removes them
# at the end. With S3_*, it writes under spike-<random>/ in the bucket and
# leaves it there; delete that folder afterwards. Run it on the server the
# registry would run on, so pushes and pulls cross the same network.
set -euo pipefail

REGISTRY_IMAGE="registry:3.1.2@sha256:be9a7062dd7e3316e5c0368e42740dc991a9ceb5d382317675a8738db0112af0"
MINIO_IMAGE="pgsty/minio:RELEASE.2026-08-04T00-00-00Z@sha256:b6bfe7239bfc83fb90d31612d9704d86039dd714f7904b3f1ad68f211e602372"
id="spike-$(head -c4 /dev/urandom | od -An -tx1 | tr -d ' \n')"
work="$(mktemp -d)"
containers=()
cleanup() {
  docker rm -fv "${containers[@]}" >/dev/null 2>&1 || true
  docker image ls --format '{{.Repository}}:{{.Tag}}' | grep "$id" | xargs -r docker rmi -f >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

if [[ -z "${S3_ENDPOINT:-}" ]]; then
  gw="$(docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}')"
  containers+=("$id-minio")
  docker run -d --name "$id-minio" -p "$gw::9000" --tmpfs /data \
    -e MINIO_ROOT_USER=spike -e MINIO_ROOT_PASSWORD=spike-secret "$MINIO_IMAGE" minio server /data >/dev/null
  port="$(docker port "$id-minio" 9000/tcp | head -1 | sed 's/.*://')"
  S3_ENDPOINT="http://$gw:$port" S3_REGION=us-east-1 S3_BUCKET=spike S3_ACCESS_KEY_ID=spike S3_SECRET_ACCESS_KEY=spike-secret
  for _ in $(seq 50); do
    # The registry creates no bucket: make it with MinIO's own client.
    docker exec "$id-minio" mc alias set local http://127.0.0.1:9000 spike spike-secret >/dev/null 2>&1 \
      && docker exec "$id-minio" mc mb -p local/spike >/dev/null 2>&1 && break
    sleep 0.2
  done
  store="MinIO on this machine"
else
  store="$S3_ENDPOINT/$S3_BUCKET"
fi

# registry NAME [s3] runs a registry and prints its host:port.
registry() {
  local name=$1 storage=(-e REGISTRY_STORAGE_FILESYSTEM_ROOTDIRECTORY=/var/lib/registry)
  if [[ "${2:-}" == s3 ]]; then
    storage=(-e REGISTRY_STORAGE=s3 -e REGISTRY_STORAGE_S3_REGIONENDPOINT="$S3_ENDPOINT"
      -e REGISTRY_STORAGE_S3_REGION="${S3_REGION:-us-east-1}" -e REGISTRY_STORAGE_S3_BUCKET="$S3_BUCKET"
      -e REGISTRY_STORAGE_S3_ACCESSKEY="$S3_ACCESS_KEY_ID" -e REGISTRY_STORAGE_S3_SECRETKEY="$S3_SECRET_ACCESS_KEY"
      -e REGISTRY_STORAGE_S3_FORCEPATHSTYLE=true -e REGISTRY_STORAGE_S3_ROOTDIRECTORY="/$id"
      -e REGISTRY_STORAGE_REDIRECT_DISABLE=true)
  fi
  containers+=("$name")
  docker run -d --name "$name" -p 127.0.0.1::5000 -e OTEL_TRACES_EXPORTER=none "${storage[@]}" "$REGISTRY_IMAGE" >/dev/null
  local hp
  hp="127.0.0.1:$(docker port "$name" 5000/tcp | head -1 | sed 's/.*://')"
  for _ in $(seq 50); do curl -fsS "http://$hp/v2/" >/dev/null 2>&1 && break; sleep 0.2; done
  printf '%s\n' "$hp"
}

# The image: node:24-slim and 200 MB that doesn't compress, ~300 MB pushed.
cat > "$work/Dockerfile" <<'DOCKERFILE'
FROM node:24-slim
RUN head -c 200000000 /dev/urandom > /payload
ARG CHANGE=0
RUN echo "$CHANGE" > /change
DOCKERFILE
docker build -q -t "$id/app:1" "$work" >/dev/null
docker build -q -t "$id/app:2" --build-arg CHANGE="$(date +%s%N)" "$work" >/dev/null
size="$(docker image inspect "$id/app:1" -f '{{.Size}}')"

# secs CMD... runs CMD quietly and prints how long it took, in seconds.
secs() { local t0; t0=$(date +%s.%N); "$@" >/dev/null; awk -v a="$t0" -v b="$(date +%s.%N)" 'BEGIN { print b - a }'; }

# blobs HOSTPORT REPO TAG prints the digests of the image's config and
# layers (following an index to its first manifest).
blobs() {
  local hp=$1 repo=$2 ref=$3 m accept
  accept='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'
  m="$(curl -fsS -H "Accept: $accept" "http://$hp/v2/$repo/manifests/$ref")"
  if [[ "$m" == *'"manifests"'* ]]; then
    ref="$(grep -o '"digest": *"sha256:[0-9a-f]*"' <<<"$m" | head -1 | grep -o 'sha256:[0-9a-f]*')"
    m="$(curl -fsS -H "Accept: $accept" "http://$hp/v2/$repo/manifests/$ref")"
  fi
  grep -o '"digest": *"sha256:[0-9a-f]*"' <<<"$m" | grep -o 'sha256:[0-9a-f]*'
}

# fetch HOSTPORT REPO DIGEST... downloads blobs, as a server without them
# pulls them (Docker's own cache would hide the registry's speed).
fetch() {
  local hp=$1 repo=$2 d
  shift 2
  for d in "$@"; do curl -fsS -o /dev/null -w '%{size_download}\n' "http://$hp/v2/$repo/blobs/$d"; done \
    | awk '{ n += $1 } END { printf "%d\n", n / 1000000 }' >"$work/fetched"
}

# measure HOSTPORT LABEL pushes both images, pulls the first whole and of
# the second only what changed, and prints a row.
measure() {
  local hp=$1 label=$2 repo="$id/app" push1 pull1 push2 pull2 one two changed mb1
  docker tag "$id/app:1" "$hp/$repo:1"
  docker tag "$id/app:2" "$hp/$repo:2"
  push1=$(secs docker push -q "$hp/$repo:1")
  push2=$(secs docker push -q "$hp/$repo:2")
  mapfile -t one < <(blobs "$hp" "$repo" 1)
  mapfile -t two < <(blobs "$hp" "$repo" 2)
  mapfile -t changed < <(printf '%s\n' "${two[@]}" | grep -vxF -f <(printf '%s\n' "${one[@]}"))
  pull1=$(secs fetch "$hp" "$repo" "${one[@]}")
  mb1=$(cat "$work/fetched")
  pull2=$(secs fetch "$hp" "$repo" "${changed[@]}")
  printf '| %s | %.2f s | %.2f s (%s MB) | %.2f s | %.2f s |\n' "$label" "$push1" "$pull1" "$mb1" "$push2" "$pull2"
}

echo "Image: $((size / 1000000)) MB; the one-layer change adds a few bytes. Store: $store"
echo "| registry storage | push 300 MB | pull 300 MB | push one-layer change | pull one-layer change |"
# Pulls fetch the blobs over the registry API (curl), as a server that has none of them would.
echo "|---|---|---|---|---|"
measure "$(registry "$id-local")" "local disk"
measure "$(registry "$id-s3" s3)" "S3 ($store)"
