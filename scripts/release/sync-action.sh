#!/usr/bin/env bash
# Publishes contrib/github-action as NordTempo/deploy-action, the GitHub
# Action's own repository (ADR 0094): the directory as committed at HEAD
# becomes the repository's root, with this repository's LICENSE. When that
# changes the repository, it commits, tags the next minor version of its
# major (v0.2.0, v0.3.0, ...) and moves the major tag (v0) there, which
# workflows use.
#
#   scripts/release/sync-action.sh VERSION [--dry-run]
#
# VERSION is the Dock release it comes from, named in the commit. git must
# be able to push to the repository (`gh auth setup-git` with GH_TOKEN, a
# token with Contents and Workflows write on NordTempo/deploy-action). With
# --dry-run it prints what would change and pushes nothing.
# DEPLOY_ACTION_REMOTE replaces the repository's URL (tests).
#
# The action stays on v0 while Dock is 0.x, keeping its inputs compatible
# within it. When Dock reaches 1.0, tag the synced commit v1.0.0 (and v1)
# by hand once: the next syncs follow the newest major, as they would a
# later breaking change's v2.0.0.
set -euo pipefail

repo=NordTempo/deploy-action
version="${1:?usage: sync-action.sh VERSION [--dry-run]}"
dry_run="${2:-}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

git clone -q --no-checkout "${DEPLOY_ACTION_REMOTE:-https://github.com/$repo.git}" "$work/repo" 2> /dev/null
cd "$work/repo"
# main, whatever the remote's HEAD names; none yet in a new repository.
if git rev-parse -q --verify origin/main > /dev/null; then
  git checkout -q -B main origin/main
else
  git checkout -q --orphan main
fi
# The repository's files become the directory's, nothing else.
git rm -rq --ignore-unmatch . > /dev/null
git -C "$root" archive HEAD:contrib/github-action | tar -x
git -C "$root" show HEAD:LICENSE > LICENSE
git add -A
if git diff --cached --quiet; then
  echo "$repo is up to date with contrib/github-action"
  exit 0
fi
git diff --cached --stat

last="$(git tag --list 'v*.*.*' --sort=-version:refname | head -n 1)"
if [ -z "$last" ]; then
  next=v0.1.0
else
  IFS=. read -r major minor _ <<< "${last#v}"
  next="v$major.$((minor + 1)).0"
fi
major_tag="${next%%.*}"
if [ "$dry_run" = --dry-run ]; then
  echo "would commit, tag $next and move $major_tag (dry run)"
  exit 0
fi

commit="$(git -C "$root" rev-parse --short HEAD)"
git -c user.name="github-actions[bot]" -c user.email="41898282+github-actions[bot]@users.noreply.github.com" \
  commit -q -m "sync from NordTempo/tempoDock $version ($commit)"
git tag "$next"
git tag -f "$major_tag" > /dev/null
# main and the new tag together or not at all, then the moved major tag.
git push -q --atomic origin main "$next"
git push -q -f origin "$major_tag"
echo "published $repo $next ($major_tag) from tempoDock $version"
