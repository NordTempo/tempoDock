#!/bin/sh
# Checks changes.sh's classification of a few paths: each line is a file
# and the go/web it must set. Run by `make lint`.
set -eu
dir=$(dirname "$0")
fail=0
while read -r file want; do
	got=$(printf '%s\n' "$file" | CHANGES_FILES=- GITHUB_OUTPUT=/dev/stdout "$dir/changes.sh" | grep -v '^site=' | tr '\n' ' ')
	if [ "$got" != "$want " ]; then
		echo "changes.sh: $file sets '$got', want '$want'" >&2
		fail=1
	fi
done <<EOF2
internal/core/core.go go=true web=false
web/src/features/service/tabs.ts go=false web=true
web/src/routes/_app.\$org.inbox.tsx go=true web=true
web/src/features/deploy/pack.ts go=true web=true
internal/api/api.go go=true web=true
docs/guide/agents.md go=false web=false
EOF2
exit $fail
