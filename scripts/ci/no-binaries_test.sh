#!/bin/sh
# Checks scripts/ci/no-binaries.sh on a throwaway repository: each case
# commits one file over a base commit and expects the check to pass or
# fail. Run by `make lint` and CI's lint job.
set -eu

script=$(cd "$(dirname "$0")" && pwd)/no-binaries.sh
repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT
cd "$repo"
git init -q -b main
git config user.email ci@example.com
git config user.name ci
git commit -q --allow-empty -m base

failures=0
# expect pass|fail NAME: commits what the caller staged, runs the check
# against base, and resets to base.
expect() {
	git commit -q -m "$2"
	if "$script" HEAD^1 >/dev/null 2>&1; then got=pass; else got=fail; fi
	if [ "$got" != "$1" ]; then
		echo "FAIL: $2: $got, want $1" >&2
		failures=$((failures + 1))
	fi
	git reset -q --hard HEAD^1
}

printf '\177ELF\002\001\001\000rest' >dock && git add dock && expect fail "an ELF binary at the root"
printf '\317\372\355\376rest' >tool && git add tool && expect fail "a Mach-O binary"
# git calls a file binary when it has a NUL byte: random bytes alone lack
# one about once in 3,000 runs, so start with one.
{ printf '\000'; head -c 2048 /dev/urandom; } >blob.bin && git add blob.bin && expect fail "an unknown binary file"
head -c 2048 /dev/urandom >font.woff2 && git add font.woff2 && expect pass "a font"
head -c 2048 /dev/urandom >shot.png && git add shot.png && expect pass "an image"
head -c 1100000 /dev/zero | tr '\0' 'a' >big.txt && git add big.txt && expect fail "a text file over 1 MB"
mkdir -p web && head -c 1100000 /dev/zero | tr '\0' 'a' >web/openapi.json && git add web && expect pass "the generated OpenAPI spec over 1 MB"
printf 'echo hi\n' >run && chmod +x run && git add run && expect fail "an executable without #!"
printf '#!/bin/sh\necho hi\n' >run.sh && chmod +x run.sh && git add run.sh && expect pass "an executable script"
printf 'MZ is how this note starts\n' >note.txt && git add note.txt && expect pass "a text file starting with MZ"

[ "$failures" -eq 0 ] && echo "no-binaries_test: ok"
exit "$failures"
