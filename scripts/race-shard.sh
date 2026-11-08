#!/usr/bin/env bash
# Runs one shard of the race tests (make test-race SHARD=i/n, CI's race
# jobs).
#
#   scripts/race-shard.sh <i>/<n> <packages...>
#
# Lists the top-level tests of every package, sorts the unique names and
# deals them round-robin into n shards; shard i runs its names in all the
# packages with one go test -run. Each test runs in exactly one shard, a
# new or renamed test included, and every shard gets an equal share of each
# package's tests, so the shards take about the same time.
set -euo pipefail

shard=${1:?usage: race-shard.sh <i>/<n> <packages...>}
shift
i=${shard%/*}
n=${shard#*/}
if ! [[ $i =~ ^[0-9]+$ && $n =~ ^[0-9]+$ ]] || ((i < 1 || i > n)); then
	echo "race-shard: SHARD must be i/n with 1 <= i <= n, not $shard" >&2
	exit 2
fi

# -race here too, so the test binaries this compiles are the ones the run
# below uses. Benchmarks don't run without -bench, so they're left out.
names=$(go test -race -list . "$@" | grep -E '^(Test|Example|Fuzz)' | LC_ALL=C sort -u)
mine=$(awk -v i="$i" -v n="$n" '(NR - 1) % n == i - 1' <<<"$names")
if [ -z "$mine" ]; then
	echo "race-shard: no tests in shard $shard"
	exit 0
fi
echo "race-shard: shard $shard runs $(wc -l <<<"$mine") of $(wc -l <<<"$names") tests"
# A shard takes under 10 minutes in CI, so a hung test fails in 15, not
# the 30 that all of them together need.
timeout=$((n == 1 ? 30 : 15))m
go test -race -timeout "$timeout" -run "^($(paste -sd'|' <<<"$mine"))\$" "$@"
