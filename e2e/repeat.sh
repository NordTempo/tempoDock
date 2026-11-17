#!/usr/bin/env bash
# Runs one scenario N times against the servers in the environment and
# prints a line per run: its exit code, the server its project landed on
# and its failures (with hammer timings, to see whether they sit at the
# traffic switch). For failures too rare for one run to show, e.g. run.sh's
# zerodowntime, which fails at about 1 in 50 switches:
#
#   source /tmp/e2e-nightly/env    # or another slot's (e2e/vm/up.sh)
#   e2e/repeat.sh 30 run/zerodowntime
#
# Each run gets its own E2E_RUN, so its own projects. Only run.sh's
# scenarios know their project's name (e2e-<run>-<scenario's suffix>);
# for other suites the server column is left empty.
set -uo pipefail
N=${1:?usage: repeat.sh N suite/scenario}
S=${2:?usage: repeat.sh N suite/scenario}
suite=${S%%/*} name=${S#*/}
source "$(dirname "$0")/lib.sh"
OUT=$(mktemp -d)
log "logs in $OUT"
declare -A suffix=([zerodowntime]=zd [failures]=fail [pg]=pg)
failed=0
for i in $(seq "$N"); do
	run=r$(date +%H%M%S)$i
	E2E_RUN=$run "$E2E_DIR/$suite.sh" "$name" >"$OUT/$i.log" 2>&1
	rc=$?
	srv=""
	if [[ $suite == run && -n ${suffix[$name]:-} ]]; then
		srv=$(api GET "/projects/e2e-$run-${suffix[$name]}/environments" 2>/dev/null | jq -r '.[] | select(.name == "production") | .server')
	fi
	fails=$(sed 's/\x1b\[[0-9;]*m//g' "$OUT/$i.log" | grep -A4 '^FAIL' | grep -E '^FAIL|failure|body' | tr '\n' ' ' | tr -s ' ')
	((rc == 0)) || failed=$((failed + 1))
	printf '%d rc=%d server=%s %s\n' "$i" "$rc" "${srv:--}" "${fails:-clean}"
done
log "$failed of $N runs failed"
((failed == 0))
