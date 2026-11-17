#!/bin/sh
# A stand-in for curl in tests that must not reach the network: local.sh's
# version cases and the install scripts' tests in internal/update put it on
# PATH as `curl`. It answers the requests the scripts make:
#
#   <server>/api/v1/health        $FAKE_HEALTH, as given
#   https://github.com/PATH       the file $FAKE_GITHUB/PATH
#   https://api.github.com/PATH   the file $FAKE_GITHUB/api/PATH
#
# A missing file fails as `curl -f` does on a 404 (exit 22). With -w (the
# installer asks where .../releases/latest redirects to) the file holds
# that address. Every request is logged to $FAKE_CURL_LOG as
# "<--proto value, or any> <url>".
out="" fmt="" url="" proto=any
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift ;;
    -w) fmt=$2; shift ;;
    --proto) proto=$2; shift ;;
    --proto-redir | --max-time | --retry | -H | -K) shift ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
printf '%s %s\n' "$proto" "$url" >> "${FAKE_CURL_LOG:-/dev/null}"
case "$url" in
  */api/v1/health)
    [ -n "${FAKE_HEALTH:-}" ] || exit 7
    printf '%s' "$FAKE_HEALTH"
    exit 0
    ;;
  https://github.com/*) file="${FAKE_GITHUB:-/nonexistent}/${url#https://github.com/}" ;;
  https://api.github.com/*) file="${FAKE_GITHUB:-/nonexistent}/api/${url#https://api.github.com/}" ;;
  *) exit 6 ;;
esac
[ -f "$file" ] || exit 22
if [ -z "$fmt" ] && [ -n "$out" ]; then
  cat "$file" > "$out"
else
  cat "$file"
fi
