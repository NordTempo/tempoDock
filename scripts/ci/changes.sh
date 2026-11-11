#!/bin/sh
# Writes go=, web= and site=true|false to $GITHUB_OUTPUT: which parts of
# the repo a change touches, so ci.yml runs only the jobs it can affect.
#
#   go    Go code and everything else that builds, tests or ships Dock:
#         lint, test, race, integration, analytics, perf-bench, and the
#         full perf-artifacts.
#   web   the UI under web/: the web job, and perf-artifacts' binary and
#         UI checks (the binary embeds the UI).
#   site  the landing page under site/.
#
# The API's contract sets both: its Go types and handlers, the OpenAPI
# specs generated from them, the UI's clients generated from those, and
# the recorder script the UI builds into internal/analytics. Markdown and
# docs/ set nothing: no build or test reads them. A change to .github/
# runs everything, and so does anything this can't diff.
#
# A pull request's checkout is its merge commit, whose first parent is the
# base branch; a push diffs against the commit before it (event.before).
#
# Usage: scripts/ci/changes.sh, or with file names on stdin and
# CHANGES_FILES=- to classify a list (for trying it on a PR's files).
set -eu

out=${GITHUB_OUTPUT:-/dev/stdout}

all() {
	printf 'go=true\nweb=true\nsite=true\n' >>"$out"
	exit 0
}

if [ "${CHANGES_FILES:-}" = - ]; then
	files=$(cat)
else
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
fi
[ -n "$files" ] || all

go=false
web=false
site=false
while IFS= read -r f; do
	case "$f" in
	.github/*) all ;;
	site/*) site=true ;;
	internal/apitypes/* | internal/api/* | internal/tempobase/tbhttp/* | \
		web/openapi.json | web/src/api/* | web/tempobase/openapi.json | web/tempobase/src/api/* | \
		internal/analytics/ingest/recorder/*)
		go=true
		web=true
		;;
	docs/* | *.md) ;;
	web/*.go | web/tempobase/*.go) go=true ;;
	web/*) web=true ;;
	*) go=true ;;
	esac
done <<EOF2
$files
EOF2
printf 'go=%s\nweb=%s\nsite=%s\n' "$go" "$web" "$site" >>"$out"
