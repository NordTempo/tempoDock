#!/bin/sh
# Fails when a change adds or modifies a file that doesn't belong in git:
#
#   - a compiled program (ELF, Mach-O or PE magic), anywhere;
#   - any other file git sees as binary, unless it is a font or an image;
#   - a file over 1 MB, unless it is one of the generated API files or
#     the design sources listed below;
#   - an executable bit on a file without a #! line.
#
# `go build ./cmd/dock` or `go build ./scripts/perf` at the root writes a
# binary next to go.mod; two got committed that way (dock in #339/#340,
# perf in #386, 11 MB each) and stay in the history for good, since
# release tags can't move. .gitignore lists those names; this catches the
# rest.
#
# A pull request's checkout is its merge commit, whose first parent is the
# base branch; a push compares with the commit before it (BEFORE).
#
#   scripts/ci/no-binaries.sh                 # in CI
#   scripts/ci/no-binaries.sh origin/main     # locally, against a base
set -eu

max_bytes=1048576

base=${1:-}
if [ -z "$base" ]; then
	case "${GITHUB_EVENT_NAME:-}" in
	pull_request) base=HEAD^1 ;;
	push)
		case "${BEFORE:-}" in "" | 0000000000000000000000000000000000000000) exit 0 ;; esac
		git fetch -q --depth=1 origin "$BEFORE"
		base=$BEFORE
		;;
	*) echo "no-binaries: pass the base revision" >&2; exit 2 ;;
	esac
fi

# big_ok PATH: may be over max_bytes (generated, reviewed by its generator).
big_ok() {
	case "$1" in
	web/openapi.json | web/src/api/schema.d.ts | web/tempobase/openapi.json | web/tempobase/src/api/schema.d.ts) return 0 ;;
	docs/design/source/*.html) return 0 ;;
	esac
	return 1
}

# binary_ok PATH: a binary file that belongs in the repository.
binary_ok() {
	case "$1" in
	*.woff2 | *.woff | *.png | *.jpg | *.jpeg | *.webp | *.gif | *.ico | *.avif) return 0 ;;
	esac
	return 1
}

bad=0
fail() {
	echo "no-binaries: $1" >&2
	bad=1
}

# Added, copied, modified or renamed files, with git's binary verdict:
# numstat prints "-	-	path" for a binary file.
git diff --numstat --diff-filter=ACMR --no-renames "$base" HEAD >"${TMPDIR:-/tmp}/no-binaries.$$"
while IFS="$(printf '\t')" read -r added _ path; do
	[ -f "$path" ] || continue
	magic=$(head -c 4 "$path" | od -An -tx1 | tr -d ' \n')
	case "$magic" in
	7f454c46 | feedface | feedfacf | cefaedfe | cffaedfe | cafebabe | 4d5a*)
		# MZ is PE's magic, but also how a text file can start.
		if [ "${magic#4d5a}" = "$magic" ] || [ "$added" = - ]; then
			fail "$path is a compiled program; build outputs stay out of git (see .gitignore)"
			continue
		fi
		;;
	esac
	if [ "$added" = - ] && ! binary_ok "$path"; then
		fail "$path is a binary file; only fonts and images are committed (scripts/ci/no-binaries.sh)"
	fi
	size=$(wc -c <"$path")
	if [ "$size" -gt "$max_bytes" ] && ! big_ok "$path"; then
		fail "$path is $size bytes, over 1 MB; large files stay out of git (or add it to big_ok in scripts/ci/no-binaries.sh, with a reason)"
	fi
	if [ -x "$path" ] && [ "$(head -c 2 "$path")" != '#!' ]; then
		fail "$path is executable but isn't a script (no #! line): chmod -x it"
	fi
done <"${TMPDIR:-/tmp}/no-binaries.$$"
rm -f "${TMPDIR:-/tmp}/no-binaries.$$"
exit "$bad"
