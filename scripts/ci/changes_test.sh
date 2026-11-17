#!/bin/sh
# Checks changes.sh's classification. Run by `make lint` and CI's lint job.
#
# First a few paths and the go/web/site each must set. Then every file Go
# reads that isn't Go itself must count as Go, so a change to it runs the
# Go jobs: what //go:embed embeds, and what tests read by a relative path
# ("../../web/src/routes", or filepath.Join("..", "..", "web", ...)). A
# file found here that changes.sh files under web or docs only fails this.
set -eu
dir=$(dirname "$0")
root=$(cd "$dir/../.." && pwd)
cd "$root"
fail=0
classify() {
	printf '%s\n' "$1" | CHANGES_FILES=- GITHUB_OUTPUT=/dev/stdout "$dir/changes.sh" | tr '\n' ' '
}
while read -r file want; do
	got=$(classify "$file")
	if [ "$got" != "$want " ]; then
		echo "changes.sh: $file sets '$got', want '$want'" >&2
		fail=1
	fi
done <<EOF2
internal/core/core.go go=true web=false site=false
web/src/features/service/tabs.ts go=false web=true site=false
web/src/routes/_app.\$org.inbox.tsx go=true web=true site=false
web/src/features/deploy/pack.ts go=true web=true site=false
web/src/features/help/topics.ts go=true web=true site=false
internal/api/api.go go=true web=true site=false
docs/guide/agents.md go=true web=false site=true
docs/guide/guide_test.go go=true web=false site=false
docs/decisions/0001-one-binary.md go=false web=false site=false
site/src/pages/index.astro go=false web=false site=true
EOF2

# read lists the tracked files Go reads: embedded ones, and test reads by
# relative path, each resolved against its Go file's directory.
read_by_go() {
	git ls-files '*.go' | while read -r f; do
		d=$(dirname "$f")
		# //go:embed patterns ("all:" read as the directory).
		sed -n 's#^[[:space:]]*//go:embed[[:space:]]##p' "$f" | tr ' ' '\n' | sed 's#^all:##' | while read -r p; do
			[ -n "$p" ] && git ls-files "$d/$p"
		done
		case "$f" in *_test.go) ;; *) continue ;; esac
		{
			grep -oE '"(\.\./)+[^"]+"' "$f" | tr -d '"'
			grep -oE '"\.\.", ("\.\.", )*"[^"]+"(, "[^"]+")*' "$f" | sed 's/", "/\//g' | tr -d '"'
		} | while read -r rel; do
			p=$(realpath -m --relative-to="$root" "$d/$rel")
			case "$p" in ../*) continue ;; esac
			git ls-files "$p" | head -n 1
		done
	done | sort -u
}
for f in $(read_by_go); do
	case $(classify "$f") in
	*go=true*) ;;
	*)
		echo "changes.sh: Go reads $f, but a change to it runs no Go job; add it to changes.sh's Go list" >&2
		fail=1
		;;
	esac
done
exit $fail
