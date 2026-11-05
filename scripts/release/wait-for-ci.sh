#!/usr/bin/env bash
# Release gate (release.yml): succeeds once the ci workflow has passed for
# this exact commit on main, so a release reuses main's result instead of
# running every check again.
#
#   scripts/release/wait-for-ci.sh <sha> [max-wait-seconds]
#
# Needs GH_TOKEN (actions: read) and GITHUB_REPOSITORY. A run of ci for the
# sha that succeeded passes at once; one still queued or running (the tag
# usually follows the merge within minutes) is waited for, polling every
# 30 s up to max-wait-seconds (default 2400); otherwise it fails.
set -euo pipefail

sha=${1:?usage: wait-for-ci.sh <sha> [max-wait-seconds]}
max_wait=${2:-2400}
repo=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}
deadline=$((SECONDS + max_wait))

while :; do
	# Every push run of ci on main for this commit, re-runs included (a
	# re-run reuses its run's id and reports its latest attempt).
	runs=$(gh api "repos/$repo/actions/workflows/ci.yml/runs?head_sha=$sha&branch=main&event=push&per_page=20" \
		--jq '.workflow_runs[] | "\(.id) \(.status) \(.conclusion) \(.html_url)"')
	if grep -q ' completed success ' <<<"$runs"; then
		echo "ci passed on main for $sha: $(grep ' completed success ' <<<"$runs" | head -1 | cut -d' ' -f4)"
		exit 0
	fi
	# No run, or every run finished without success.
	if [ -z "$runs" ] || ! grep -qv ' completed ' <<<"$runs"; then
		echo "::error::ci has not passed on main for $sha. Tag a commit that passed CI on main, or re-run main's failed ci jobs and then this workflow."
		[ -z "$runs" ] || echo "$runs"
		exit 1
	fi
	if ((SECONDS >= deadline)); then
		echo "::error::ci on main for $sha is still running after ${max_wait}s. Re-run this workflow once it passes."
		echo "$runs"
		exit 1
	fi
	echo "waiting for ci on main for $sha:"
	echo "$runs"
	sleep 30
done
