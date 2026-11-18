#!/usr/bin/env bash
# The nightly end-to-end run: a safety net that catches regressions before
# a release. Each night (a systemd user timer, 03:00 Europe/Oslo) it
#
#   1. checks out main's newest commit whose ci run passed, runs its Go
#      benchmarks strictly against scripts/perf/baseline.json (make
#      perf-bench: CI judges a pull request's timings only against its base,
#      so a slowdown spread over several changes shows here), and builds it
#      into a directory release (the shape `dock update` reads, ADR 0028);
#   2. starts fresh servers in their own e2e/vm slot (e2e-nightly: a control
#      plane and two nodes) and installs the latest published release on
#      them, as a user would;
#   3. updates them to that build with `dock update apply` while a hammer
#      sends requests to an app on each server: zero may fail;
#   4. runs the e2e scenarios (SUITE below), each on its own, without retries,
#      then e2e/fresh-install.sh on bare servers of its own and e2e/recover.sh
#      (a lost server, ADR 0098) on a control plane of its own;
#   5. tears the servers down.
#
# A failure opens (or comments on) one GitHub issue labelled `nightly` and
# POSTs a summary to the webhook in ~/.config/dock-nightly/webhook-url; the
# first night that passes closes the issue. A pass only adds a line to
# ~/.cache/dock-nightly/history.log. See e2e/README.md.
#
#   e2e/nightly.sh --now [--here] [--keep] [--no-report] [scenario...]
#   e2e/nightly.sh install | pause | resume | status
#
#   --now        run a cycle now (the timer runs exactly this)
#   --here       use this checkout (its HEAD, scripts included) instead of
#                main's newest green commit, to try a change to the run
#   --keep       leave the servers up afterwards (e2e/vm/down.sh removes them;
#                E2E_SLOT=e2e-nightly)
#   --no-report  log only: no issue, no webhook
#   scenario     run only these (e.g. run/apps features/lock), not SUITE
#
# Logs: ~/.cache/dock-nightly/<date>/ (nightly.log, one log per step and
# scenario, results.tsv). Scenarios listed in e2e/nightly.quarantine are
# skipped, with the reason in the log.
set -uo pipefail

REPO=NordTempo/tempoDock
HOME_DIR=${NIGHTLY_HOME:-${XDG_CACHE_HOME:-$HOME/.cache}/dock-nightly}
SRC=$HOME_DIR/src
CONF=${XDG_CONFIG_HOME:-$HOME/.config}/dock-nightly
# The CLI never falls back to a context from the user's config (on the dev
# VM the default one is production): until up.sh's env file sets the
# nightly control plane, DOCK_URL points at nothing.
export DOCK_URL=http://127.0.0.1:9 DOCK_TOKEN=none DOCK_CONTEXT=
export E2E_SLOT=e2e-nightly E2E_SUBNET=10.250.251.0/24 E2E_VM_MEMORY=${NIGHTLY_VM_MEMORY:-5g}
NODES=2
MIN_FREE_GB=${NIGHTLY_MIN_FREE_GB:-8}  # MemAvailable needed to start
MAX_RUNTIME=${NIGHTLY_MAX_RUNTIME:-4h} # the whole cycle, teardown excluded
LOCK_WAIT=${NIGHTLY_LOCK_WAIT:-5400}   # seconds to wait for /tmp/dock-heavy.lock
SCENARIO_TIMEOUT=45m
# SUITE is every scenario a night runs, in order: run.sh's, features.sh's,
# release.sh's, then each app template (templates/*, listed at run time),
# restore.sh (volume backups, in the slot's local repository),
# pitr.sh (after the others: it moves the slot's backups into its S3 store),
# and last drain.sh, which drains node2 with whatever the others left there.
# Left out: traces.sh and metrics.sh (they start their own servers; release.sh's
# traces covers tracing), analytics-*.sh (fixed container names and a
# headless browser) and upgrade/run.sh (the update step here is the upgrade test).
SUITE=(
	run/apps run/pg run/zerodowntime run/failures run/perms run/daemon
	features/predeploy features/stopstart features/stopnode features/run features/lock
	features/logs features/snapshot features/move features/apply features/health
	features/nodedown features/backup features/alerts
	release/traces release/status release/vulns release/data release/diagnose release/sleep
	templates/all
	restore/all
	pitr/all
	drain/node2
)

say() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }
die() { say "error: $*" >&2; exit 2; }

# ---- the timer ------------------------------------------------------------

install_timer() {
	local units=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
	mkdir -p "$units" "$CONF"
	[[ -d $SRC/.git ]] || git clone -q "https://github.com/$REPO.git" "$SRC"
	cat >"$units/dock-nightly.service" <<EOF
[Unit]
Description=Tempo Dock nightly e2e run (e2e/nightly.sh)

[Service]
Type=oneshot
# The tools (go, pnpm, gh, docker) as found when the timer was installed.
Environment=PATH=$PATH
ExecStart=$SRC/e2e/nightly.sh --now
# MAX_RUNTIME caps the cycle; this leaves room for reporting and teardown.
TimeoutStartSec=5h
Nice=10
EOF
	cat >"$units/dock-nightly.timer" <<EOF
[Unit]
Description=Tempo Dock nightly e2e run at 03:00 Europe/Oslo

[Timer]
OnCalendar=*-*-* 03:00:00 Europe/Oslo
Persistent=false
Unit=dock-nightly.service

[Install]
WantedBy=timers.target
EOF
	systemctl --user daemon-reload
	systemctl --user enable --now dock-nightly.timer
	systemctl --user list-timers dock-nightly.timer --no-pager
}

# history LINE appends the one line a night leaves behind.
history() { printf '%s %s\n' "$(date '+%F %R')" "$*" >>"$HOME_DIR/history.log"; }

# inner runs as `nightly.sh --now --inner` under timeout, from the code
# being tested: build, servers up, update with the soak, scenarios.
inner() {
	local dir latest nv sha
	dir=$(cd "$(dirname "$0")/.." && pwd)
	cd "$dir" || exit 2
	sha=$(git rev-parse HEAD)
	# The latest release (no drafts or pre-releases), anonymously.
	latest=$(curl --proto '=https' --proto-redir '=https' -fsSL "https://api.github.com/repos/$REPO/releases/latest" | jq -er .tag_name) ||
		die "can't read the latest release"
	# It goes into versions, URLs and paths: vX.Y.Z, nothing else.
	[[ $latest =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "the latest release's tag isn't vX.Y.Z"
	# A version newer than the latest release, so `dock update` takes it:
	# v0.54.2 -> v0.54.3-nightly.20261117.g3c5dc9a.
	nv=$(awk -F. -v d="$(date +%Y%m%d)" -v s="${sha:0:7}" '{printf "%s.%s.%d-nightly.%s.g%s", $1, $2, $3 + 1, d, s}' <<<"$latest")
	printf 'commit\t%s\nfrom\t%s\nto\t%s\n' "$sha" "$latest" "$nv" >"$RUN_DIR/versions"
	say "testing $sha as $nv, installed over $latest"

	export TMPDIR=$HOME_DIR/tmp GOTMPDIR=$HOME_DIR/tmp
	mkdir -p "$TMPDIR"
	nightly_key || die "can't make the nightly signing key"
	# Timing budgets enforced, as on a quiet machine (the heavy lock is
	# held); a failure is reported like a scenario's, and the night goes on.
	((${#ONLY[@]})) || step perf-bench perf_bench
	step build build_release "$nv" || return 1
	step down e2e/vm/down.sh
	step up env E2E_RELEASE="$latest" e2e/vm/up.sh "$NODES" || return 1
	# shellcheck disable=SC1090
	source "/tmp/$E2E_SLOT/env"
	[[ $DOCK_URL == http://10.250.251.* ]] || die "refusing: $DOCK_URL isn't the nightly slot"
	source e2e/lib.sh
	step update update_with_soak "$nv" || return 1
	scenarios
	# The build installed fresh on bare Debian and Ubuntu servers of their
	# own (slot e2e-nightly-fresh), piped as the README says.
	step fresh-install env E2E_RELEASE_DIR="$RUN_DIR/release/$nv" DOCK_TRUSTED_KEYS="$(nightly_pub)" \
		E2E_FRESH_SLOT=e2e-nightly-fresh E2E_FRESH_SUBNET=10.250.252.0/24 timeout 45m e2e/fresh-install.sh
	# A lost server recovered from its bucket (ADR 0098), in a slot of its
	# own: the drill removes its control plane.
	step recover recover_drill "$RUN_DIR/release/$nv/dock-linux-amd64"
}

# recover_drill BINARY runs e2e/recover.sh on a control plane of its own
# (slot e2e-nightly-recover) installed from BINARY, and removes the slot.
recover_drill() (
	export E2E_SLOT=e2e-nightly-recover E2E_SUBNET=10.250.253.0/24 DOCK_BINARY=$1 DOCK_BIN=$1
	e2e/vm/down.sh
	e2e/vm/up.sh 0 || { e2e/vm/down.sh; exit 1; }
	# shellcheck disable=SC1090
	source "/tmp/$E2E_SLOT/env"
	timeout -k 30 "$SCENARIO_TIMEOUT" e2e/recover.sh </dev/null
	rc=$?
	((rc == 0)) || docker exec "$E2E_SLOT-cp" journalctl -u dock --no-pager >"$RUN_DIR/recover.$E2E_SLOT-cp.journal" 2>&1
	e2e/vm/down.sh
	exit $rc
)

# step NAME CMD... runs CMD with its output in NAME.log and a row in results.tsv.
step() {
	local name=$1 t0=$SECONDS rc
	shift
	say "step $name"
	"$@" >"$RUN_DIR/$name.log" 2>&1
	rc=$?
	result "step/$name" "$rc" "$((SECONDS - t0))" "$name.log"
	((rc == 0)) || say "step $name failed ($rc), see $name.log"
	return $rc
}

# result NAME RC SECONDS LOG records a row: pass, fail or timeout.
result() {
	local st=pass
	(($2 == 0)) || st=fail
	(($2 == 124)) && st=timeout
	printf '%s\t%s\t%s\t%s\n' "$1" "$st" "$3" "$4" >>"$RESULTS"
}

# perf_bench runs the Go benchmarks against the baseline with timing
# budgets enforced, then repeats the rows over budget, so the report's
# last lines of the log name them.
perf_bench() {
	local out=$RUN_DIR/perf-bench.out rc
	make perf-bench PERF_FLAGS="-base-rev ''" >"$out" 2>&1
	rc=$?
	cat "$out"
	printf '\nover budget:\n'
	grep -E '  (FAIL|warn)$' "$out"
	return $rc
}

# build_release VERSION builds this checkout into $RUN_DIR/release/VERSION
# with the assets `dock update` needs for amd64 servers (scripts/release/build.sh
# makes the same three, plus other platforms), and the analytics store for
# release.sh's traces.
build_release() {
	local v=$1 out=$RUN_DIR/release/$1 stage
	make web && make build VERSION="$v" && make analytics-binary VERSION="$v" || return 1
	mkdir -p "$out"
	cp bin/dock "$out/dock-linux-amd64"
	stage=$(mktemp -d)
	cp -R deploy "$stage/deploy"
	rm -rf "$stage/deploy/tempobase"
	printf '%s\n' "$v" >"$stage/deploy/VERSION"
	tar --sort=name --owner=0 --group=0 --numeric-owner -C "$stage" -czf "$out/dock-install-$v.tar.gz" deploy
	rm -rf "$stage"
	(cd "$out" && sha256sum -- * >SHA256SUMS)
	# Signed with the nightly key, which the servers trust through
	# [updates] trusted_keys (ADR 0088's amendment).
	RELEASE_SIGNING_KEY=$(cat "$CONF/release-key.pem") TRUSTED_KEYS=$(nightly_pub) go run ./scripts/release/sign "$out" "$v" || return 1
	ls -l "$out"
}

# nightly_key makes the VM-local key the nightly signs its builds with,
# once: ~/.config/dock-nightly/release-key.pem, mode 600.
nightly_key() {
	[[ -s $CONF/release-key.pem ]] && return
	mkdir -p "$CONF"
	(umask 077 && openssl genpkey -algorithm ed25519 -out "$CONF/release-key.pem")
}

# nightly_pub prints the nightly key's public half, base64 as in
# [updates] trusted_keys.
nightly_pub() { openssl pkey -in "$CONF/release-key.pem" -pubout -outform DER | tail -c 32 | base64; }

# set_updates CONTAINER LINES replaces the server's [updates] section.
set_updates() {
	docker exec -i "$1" bash -c 'f=/etc/dock/config.toml
		sed -i "/^\[updates\]/,/^\[/{/^\[updates\]/d;/^\[/!d}" $f
		{ printf "\n[updates]\n"; cat; } >>$f' <<<"$2"
}

# update_with_soak VERSION deploys go-lab on each server, points the control
# plane's release source at the build and updates the platform with
# `dock update apply` while a hammer per app checks no request fails.
update_with_soak() {
	local v=$1 i p srv hp pids=() hosts=() procs=() ok=true
	# use_proxy_of sets E2E_PROXY, which the env file exports: keep the
	# scenarios' default (the control plane) out of its reach.
	local E2E_PROXY=$E2E_PROXY
	local hammer=$TMPDIR/hammer-$$
	go build -o "$hammer" ./e2e/hammer || return 1
	docker exec "$E2E_SLOT-cp" mkdir -p /srv/dock-releases
	docker cp "$RUN_DIR/release/$v" "$E2E_SLOT-cp:/srv/dock-releases/$v"
	# The control plane takes releases from the build's directory; every
	# server trusts the nightly key that signed it.
	local keys="trusted_keys = [\"$(nightly_pub)\"]"
	set_updates "$E2E_SLOT-cp" "source = \"/srv/dock-releases\"
$keys" || return 1
	for i in $(seq "$NODES"); do set_updates "$E2E_SLOT-node$i" "$keys" || return 1; done
	docker exec "$E2E_SLOT-cp" systemctl restart dock
	wait_for 60 curl -fsS -o /dev/null "$DOCK_URL/api/v1/health" || { echo "API down after setting the release source"; return 1; }
	# One app per server: placement spreads new projects over the servers,
	# so make projects until every server has one (at most 6 tries).
	declare -A placed=()
	for i in $(seq 6); do
		((${#placed[@]} == NODES + 1)) && break
		p=soak-$i
		dk project create "$p" >/dev/null || return 1
		srv=$(api GET "/projects/$p/environments" | jq -r '.[] | select(.name == "production") | .server')
		[[ -z ${placed[$srv]:-} ]] || continue
		placed[$srv]=$p
		ensure_service "$p" web '{"health_check_path":"/healthz"}'
		hp=$(generate_domain "$p" web)
		local n; n=$(deploy_source "$p" web e2e/apps/go-lab 2>/dev/null) || { echo "deploying $p failed"; return 1; }
		[[ $(wait_release "$p" web "$n" 600) == active ]] || { echo "$p/web release $n: $(release "$p" web "$n" | jq -c '{status, error}')"; return 1; }
		use_proxy_of "$p"
		wait_http "$hp" / go-lab 30 || { echo "$hp doesn't serve go-lab on $srv"; return 1; }
		hosts+=("$srv $hp $E2E_PROXY")
	done
	echo "soak apps: ${hosts[*]}"
	for i in "${!hosts[@]}"; do
		read -r srv hp proxy <<<"${hosts[$i]}"
		"$hammer" -url "http://$hp/" -connect "$proxy" -c 4 -every 20ms -d 30m >"$RUN_DIR/soak-$srv.out" &
		pids+=($!) procs+=("$srv")
	done
	sleep 2
	dk update --refresh
	dk update apply "$v" || ok=false
	sleep 5
	for i in "${!pids[@]}"; do
		kill -INT "${pids[$i]}"; wait "${pids[$i]}"
		echo "soak on ${procs[$i]}: $(head -1 "$RUN_DIR/soak-${procs[$i]}.out")"
		grep -q ' failed=0 ' <(head -1 "$RUN_DIR/soak-${procs[$i]}.out") || { ok=false; cat "$RUN_DIR/soak-${procs[$i]}.out"; }
	done
	dk update --json | jq -r '.servers[]? | "\(.name) \(.version) \(.state)"'
	# Every server runs the build now.
	dk update --json | jq -e --arg v "$v" '.version == $v and all(.servers[]?; .version == $v)' >/dev/null ||
		{ echo "not every server runs $v"; ok=false; }
	$ok
}

# scenarios runs SUITE (or the ones given), each in its own process and
# log, skipping the quarantined ones.
scenarios() {
	local s suite name t0 rc q list=("${ONLY[@]}")
	((${#list[@]})) || list=("${SUITE[@]}")
	for s in "${list[@]}"; do
		if [[ $s == templates/all ]]; then
			mapfile -t t < <(dk template list --json | jq -r '.[].name')
			list+=("${t[@]/#/templates/}")
		fi
	done
	export E2E_RUN=n$(date +%H%M)
	for s in "${list[@]}"; do
		[[ $s == templates/all ]] && continue
		suite=${s%%/*} name=${s#*/}
		q=$(awk -v s="$s" '$1 == s {$1 = ""; print substr($0, 2)}' e2e/nightly.quarantine 2>/dev/null)
		if [[ -n $q ]]; then
			say "skip $s (quarantined: $q)"
			printf '%s\tquarantined\t0\t%s\n' "$s" "$q" >>"$RESULTS"
			continue
		fi
		say "scenario $s"
		t0=$SECONDS
		# Fresh hostnames per scenario need no state from another, except
		# release.sh's, which share one project by E2E_RUN.
		timeout -k 30 "$SCENARIO_TIMEOUT" "e2e/$suite.sh" "$name" >"$RUN_DIR/${s//\//-}.log" 2>&1 </dev/null
		rc=$?
		result "$s" "$rc" "$((SECONDS - t0))" "${s//\//-}.log"
		((rc == 0)) || { say "$s failed ($rc)"; journals "${s//\//-}" "$((SECONDS - t0))"; }
	done
}

# journals NAME SECONDS keeps what dock logged on every server of the slot
# in the last SECONDS, as NAME.<server>.journal next to the scenario's
# log: the servers are removed after the run, and a failure that only the
# daemon's log explains (a forward that stopped, a refused dial) was
# otherwise gone by morning. They stay on this machine, never in a report.
journals() {
	local c
	for c in $(docker ps --format '{{.Names}}' | grep -E "^$E2E_SLOT-(cp|node[0-9]+)$"); do
		docker exec "$c" journalctl -u dock --since "-$(($2 + 30))s" --no-pager >"$RUN_DIR/$1.$c.journal" 2>&1
	done
}

# skip REASON ends the night without a run: logged, not reported.
skip() { say "skipped: $*"; history "skipped: $*"; exit 0; }

# outer is the cycle around inner: its lock, the RAM check, the checkout,
# the heavy lock and runtime cap, teardown and the report.
outer() {
	exec 9>"$HOME_DIR/run.lock"
	flock -n 9 || { say "another nightly run is in progress" | tee -a "$LOG"; exit 0; }
	exec > >(tee -a "$LOG") 2>&1

	free_gb=$(awk '/MemAvailable/ {printf "%d", $2 / 1048576}' /proc/meminfo)
	((free_gb >= MIN_FREE_GB)) || skip "only ${free_gb} GB of RAM available, ${MIN_FREE_GB} needed"
	docker info >/dev/null 2>&1 || skip "Docker isn't running"
	# Every slot's systemd containers share root's inotify instances; at the
	# default 128, a second slot's units fail with "Too many open files".
	(($(sysctl -n fs.inotify.max_user_instances) >= 512)) ||
		skip "fs.inotify.max_user_instances is under 512; see e2e/README.md"

	if $HERE; then
		CODE=$(cd "$(dirname "$0")/.." && pwd)
	else
		sha=$(gh run list -R "$REPO" -w ci -b main -e push -s success -L 1 --json headSha -q '.[0].headSha') ||
			skip "can't ask GitHub for main's newest green commit"
		[[ -n $sha ]] || skip "no green ci run on main"
		[[ -d $SRC/.git ]] || git clone -q "https://github.com/$REPO.git" "$SRC" || skip "can't clone $REPO"
		git -C "$SRC" fetch -q origin main && git -C "$SRC" checkout -q --detach "$sha" && git -C "$SRC" clean -qfdx -e node_modules -e bin ||
			skip "can't check out $sha"
		CODE=$SRC
		[[ -x $CODE/e2e/nightly.sh ]] || skip "main's newest green commit ${sha:0:9} predates e2e/nightly.sh"
	fi
	say "nightly run in $RUN_DIR (code $(git -C "$CODE" rev-parse --short HEAD))"

	say "waiting for /tmp/dock-heavy.lock (up to ${LOCK_WAIT}s)"
	t0=$SECONDS
	# The inner run holds the heavy lock throughout: the build is CPU-heavy and
	# the scenarios measure timings that other suites' load would turn into noise.
	flock -E 75 -w "$LOCK_WAIT" /tmp/dock-heavy.lock \
		timeout -k 120 "$MAX_RUNTIME" "$CODE/e2e/nightly.sh" --now --inner "$@"
	rc=$?
	case $rc in
	75) skip "waited ${LOCK_WAIT}s for /tmp/dock-heavy.lock" ;;
	124 | 137) printf 'cycle\ttimeout\t%s\tnightly.log\n' "$((SECONDS - t0))" >>"$RESULTS" ;;
	esac

	if ! $KEEP; then
		(cd "$CODE" && E2E_SLOT=$E2E_SLOT e2e/vm/down.sh) >/dev/null 2>&1
	fi

	commit=$(awk '$1 == "commit" {print $2}' "$RUN_DIR/versions" 2>/dev/null)
	from=$(awk '$1 == "from" {print $2}' "$RUN_DIR/versions" 2>/dev/null)
	failed=$(awk -F'\t' '$2 == "fail" || $2 == "timeout" {print $1}' "$RESULTS" 2>/dev/null)
	total=$(wc -l <"$RESULTS" 2>/dev/null || echo 0)
	took="$(((SECONDS - t0) / 60))m"

	if [[ -z $failed && $rc == 0 ]]; then
		history "pass ${commit:0:9} over $from: $total steps and scenarios in $took"
		say "passed"
		$REPORT && "$CODE/e2e/nightly-report.sh" pass "$RUN_DIR"
		exit 0
	fi
	[[ -n $failed ]] || failed="the run itself (exit $rc)"
	history "FAIL ${commit:0:9} over $from: $(tr '\n' ' ' <<<"$failed")(${RUN_DIR/#$HOME/\~})"
	say "failed: $(tr '\n' ' ' <<<"$failed")"
	$REPORT && "$CODE/e2e/nightly-report.sh" fail "$RUN_DIR"
	exit 1
}

# main dispatches the command; everything lives in functions, so bash has
# parsed the whole file before it runs (the checkout may replace it).
main() {
	case ${1:-} in
	install) install_timer; exit ;;
	pause) systemctl --user disable --now dock-nightly.timer && say "paused; e2e/nightly.sh resume turns it back on"; exit ;;
	resume) systemctl --user enable --now dock-nightly.timer && systemctl --user list-timers dock-nightly.timer --no-pager; exit ;;
	status) systemctl --user list-timers dock-nightly.timer --no-pager; tail -5 "$HOME_DIR/history.log" 2>/dev/null; exit ;;
	--now) ;;
	*) sed -n '2,33p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
	esac
	shift

	HERE=false KEEP=false REPORT=true ONLY=() INNER=false
	for a in "$@"; do
		case $a in
		--here) HERE=true ;;
		--keep) KEEP=true ;;
		--no-report) REPORT=false ;;
		--inner) INNER=true ;;
		-*) die "unknown flag $a" ;;
		*) ONLY+=("$a") ;;
		esac
	done

	DAY=$(date +%F)
	RUN_DIR=${NIGHTLY_RUN_DIR:-$HOME_DIR/$DAY}
	[[ -n ${NIGHTLY_RUN_DIR:-} || ! -e $RUN_DIR ]] || RUN_DIR=$HOME_DIR/$DAY-$(date +%H%M)
	export NIGHTLY_RUN_DIR=$RUN_DIR
	mkdir -p "$RUN_DIR"
	LOG=$RUN_DIR/nightly.log
	RESULTS=$RUN_DIR/results.tsv

	if $INNER; then
		inner
	else
		outer "$@"
	fi
}

main "$@"
exit $?
