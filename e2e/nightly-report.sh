#!/usr/bin/env bash
# Reports a nightly run (e2e/nightly.sh) to GitHub and the coordinator
# (with issues turned off in the repository, the webhook alone):
#
#   e2e/nightly-report.sh fail RUN_DIR   # open the `nightly` issue, or comment
#                                        # on the open one; POST the webhook
#   e2e/nightly-report.sh pass RUN_DIR   # close the open `nightly` issue
#
# RUN_DIR holds the run's versions, results.tsv and logs. The repository is
# public: everything quoted from a log goes through redact first (IPs,
# tokens, passwords, the home directory). The webhook URL is read from
# ~/.config/dock-nightly/webhook-url; without it only the issue is written.
set -uo pipefail

REPO=NordTempo/tempoDock
LABEL=nightly
WEBHOOK_FILE=${XDG_CONFIG_HOME:-$HOME/.config}/dock-nightly/webhook-url
MODE=${1:?usage: nightly-report.sh pass|fail RUN_DIR}
DIR=${2:?usage: nightly-report.sh pass|fail RUN_DIR}

say() { printf '[%s] report: %s\n' "$(date '+%F %T')" "$*"; }
field() { awk -v k="$1" '$1 == k {print $2}' "$DIR/versions" 2>/dev/null; }

# redact keeps secrets and internal addresses out of public text.
redact() {
	sed -E -e 's/\x1b\[[0-9;]*m//g' \
		-e 's/\b([0-9]{1,3}\.){3}[0-9]{1,3}\b/<ip>/g' \
		-e 's/dock(join[0-9]*)?_[A-Za-z0-9_-]{8,}/dock_<redacted>/g' \
		-e 's/(Bearer|bearer) [^ "]+/\1 <redacted>/g' \
		-e 's/((password|PASSWORD|secret|SECRET|token|TOKEN)[A-Za-z_]*["]?[=: ]+["]?)[^ ",]+/\1<redacted>/g' \
		-e "s#$HOME#~#g"
}

date=$(basename "$DIR")
commit=$(field commit) from=$(field from) to=$(field to)
log_dir=${DIR/#$HOME/\~}
issue=$(gh issue list -R "$REPO" -l "$LABEL" -s open -L 1 --json url -q '.[0].url' 2>/dev/null)

if [[ $MODE == pass ]]; then
	[[ -n $issue ]] || exit 0
	gh issue close -R "$REPO" "$issue" --comment "The nightly run passed on $date at $commit (installed $from, updated to $to). Closing." >/dev/null &&
		say "closed $issue"
	exit 0
fi

failed=$(awk -F'\t' '$2 == "fail" || $2 == "timeout" {print $1 " (" $2 ")"}' "$DIR/results.tsv" 2>/dev/null)
[[ -n $failed ]] || failed="the run itself; see nightly.log"
quarantined=$(awk -F'\t' '$2 == "quarantined" {print $1}' "$DIR/results.tsv" 2>/dev/null | paste -sd, -)
passed=$(awk -F'\t' '$2 == "pass"' "$DIR/results.tsv" 2>/dev/null | wc -l)

# tails prints the last lines of the first three failures' logs.
tails() {
	local name log n=0
	while IFS=$'\t' read -r name st _ log; do
		[[ $st == fail || $st == timeout ]] || continue
		((n++ < 3)) || break
		printf '\n<details><summary>%s: last lines of %s</summary>\n\n```\n' "$name" "$log"
		grep -v '^\s*$' "$DIR/$log" 2>/dev/null | tail -40 | redact
		printf '```\n</details>\n'
	done <"$DIR/results.tsv"
}

body=$(
	cat <<EOF
The nightly e2e run failed on **$date**.

- Commit: $commit (main's newest green commit)
- Installed $from, updated to $to with \`dock update apply\`
- Failed: $(paste -sd, - <<<"$failed" | sed 's/,/, /g')
- Passed: $passed${quarantined:+; quarantined: $quarantined}
- Logs on the dev VM: \`$log_dir\`

$(tails)

_Posted by e2e/nightly.sh. The first passing night closes this issue._
EOF
)

if [[ $(gh api "repos/$REPO" -q .has_issues 2>/dev/null) != true ]]; then
	say "issues are off in $REPO; only the webhook reports this night"
else
	if ! gh label list -R "$REPO" --search "$LABEL" --json name -q '.[].name' | grep -qx "$LABEL"; then
		gh label create "$LABEL" -R "$REPO" --color 5319e7 --description "Nightly e2e run failures (e2e/nightly.sh)" >/dev/null
	fi
	if [[ -n $issue ]]; then
		gh issue comment -R "$REPO" "$issue" --body "$body" >/dev/null && say "commented on $issue"
	else
		issue=$(gh issue create -R "$REPO" -l "$LABEL" --title "Nightly e2e run is failing" --body "$body") && say "opened $issue"
	fi
fi

if [[ -s $WEBHOOK_FILE ]]; then
	first=$(awk -F'\t' '$2 == "fail" || $2 == "timeout" {print $4; exit}' "$DIR/results.tsv")
	tail_lines=$(grep -v '^\s*$' "$DIR/${first:-nightly.log}" 2>/dev/null | tail -15 | redact)
	jq -nc --arg date "$date" --arg commit "$commit" --arg from "$from" --arg to "$to" \
		--arg failed "$(paste -sd, - <<<"$failed")" --arg issue_url "$issue" --arg log_path "$log_dir" --arg summary "$tail_lines" \
		'{date: $date, commit: $commit, from: $from, to: $to, failed: $failed, issue_url: $issue_url, log_path: $log_path, summary: $summary}' |
		curl -fsS -m 30 -o /dev/null -H 'Content-Type: application/json' --data-binary @- "$(cat "$WEBHOOK_FILE")" &&
		say "notified the webhook" || say "the webhook POST failed"
else
	say "no $WEBHOOK_FILE; not notifying"
fi
