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
# the recorder script the UI builds into internal/analytics. So do the UI
# files Go tests read (routes, the browser's pack, the help topics). Docs
# a binary embeds (the guide, tempoBase's) set go; other markdown and
# docs/ set nothing. changes_test.sh checks all of it: every file Go
# embeds or a test reads by relative path must set go. A change to .github/
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
	# Go tests read these: the reserved project names cover the routes
	# (internal/core/reserved_test.go), the browser's pack matches the
	# CLI's (internal/pack/browser_test.go), the help topics' commands run
	# (internal/cli/help_topics_test.go, docs/guide/guide_test.go), the
	# sign-in failures have words for every code (internal/api/sso_test.go).
	web/src/routes/* | web/src/features/deploy/pack.ts | web/src/features/help/topics.ts | web/src/features/sso/failures.ts)
		go=true
		web=true
		;;
	*.go) go=true ;;
	# Embedded in the binaries (//go:embed): the in-app guide, tempoBase's
	# docs, the built UIs' directories.
	docs/guide/* | docs/tempobase/* | web/dist/* | web/tempobase/dist/*) go=true ;;
	docs/* | *.md) ;;
	web/*) web=true ;;
	*) go=true ;;
	esac
done <<EOF2
$files
EOF2
printf 'go=%s\nweb=%s\nsite=%s\n' "$go" "$web" "$site" >>"$out"
