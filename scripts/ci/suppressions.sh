#!/usr/bin/env bash
# Counts lint suppressions (//nolint, biome-ignore, @ts-ignore and the
# like) in the Go and TypeScript sources and fails unless the count is
# exactly the budget below. Suppressions spread: each one is an example the
# next change copies. A new one raises the budget in the same PR, with the
# reason in the PR description; removing one lowers it, so the budget never
# leaves room for another to slip in. Run by make lint and CI's lint job.
# third_party holds patched copies of dependencies, not our code.
set -euo pipefail

budget=49

cd "$(git rev-parse --show-toplevel)"
pattern='//[[:space:]]*nolint|#nosec|//lint:ignore|biome-ignore|@ts-(ignore|expect-error|nocheck)|eslint-disable'
found=$(git grep -nE "$pattern" -- '*.go' '*.ts' '*.tsx' ':!*.gen.ts' ':!third_party' || true)
count=$(grep -c . <<<"$found" || true)

if ((count > budget)); then
	echo "$found"
	echo "::error::$count lint suppressions, over the budget of $budget. Fix the code instead; if a suppression really is the right call, raise budget in scripts/ci/suppressions.sh and say why in the PR."
	exit 1
fi
if ((count < budget)); then
	echo "::error::$count lint suppressions, under the budget of $budget. Lower budget in scripts/ci/suppressions.sh to $count."
	exit 1
fi
echo "lint suppressions: $count (budget $budget)"
