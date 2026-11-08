#!/bin/sh
# Writes site=true|false and rest=true|false to $GITHUB_OUTPUT: whether a
# change touches site/ (the landing page) and whether it touches anything
# else, so ci.yml runs only the jobs a change can affect. A change to
# .github/ runs everything, and so does anything this can't diff.
#
# A pull request's checkout is its merge commit, whose first parent is the
# base branch; a push diffs against the commit before it (event.before).
set -eu

out=${GITHUB_OUTPUT:-/dev/stdout}

all() {
	printf 'site=true\nrest=true\n' >>"$out"
	exit 0
}

case "${GITHUB_EVENT_NAME:-}" in
pull_request) base=HEAD^1 ;;
push)
	case "${BEFORE:-}" in "" | 0000000000000000000000000000000000000000) all ;; esac
	git fetch -q --depth=1 origin "$BEFORE" || all
	base=$BEFORE
	;;
*) all ;;
esac

files=$(git diff --name-only "$base" HEAD) || all
[ -n "$files" ] || all

site=false
rest=false
while IFS= read -r f; do
	case "$f" in
	site/*) site=true ;;
	.github/*) all ;;
	*) rest=true ;;
	esac
done <<EOF2
$files
EOF2
printf 'site=%s\nrest=%s\n' "$site" "$rest" >>"$out"
