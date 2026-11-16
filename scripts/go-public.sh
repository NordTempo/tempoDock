#!/usr/bin/env bash
# go-public.sh makes the repository public and turns on what GitHub's free
# plan only offers public repositories. Run it once, by hand, as a repo
# admin (gh auth status), on launch day: after the v1.0.0 release, with
# the launch checklist in ADR 0082 done. Every step is idempotent, so a
# failed run can simply be run again.
#
#   scripts/go-public.sh            asks before changing anything
#   scripts/go-public.sh --yes      doesn't ask
set -euo pipefail

repo=NordTempo/tempoDock

if [[ "${1:-}" != --yes ]]; then
	read -r -p "Make $repo public and apply its public settings? [y/N] " answer
	[[ "$answer" == [yY] ]] || exit 1
fi

step() { printf '\n== %s\n' "$*"; }
api() { gh api --silent "$@"; }

step "visibility: public"
gh repo edit "$repo" --visibility public --accept-visibility-change-consequences

# CI and releases move to GitHub-hosted runners, free now. ci.yml and
# release.yml fall back to them when these are unset.
step "runners: GitHub-hosted for CI and releases"
for v in CI_RUNNER RELEASE_RUNNER RELEASE_ARM_RUNNER; do
	gh variable delete "$v" -R "$repo" 2>/dev/null || echo "$v was not set"
done

step "security: private reporting, secret scanning, push protection, Dependabot"
api -X PUT "repos/$repo/private-vulnerability-reporting"
api -X PATCH "repos/$repo" --input - <<'EOF'
{"security_and_analysis": {
  "secret_scanning": {"status": "enabled"},
  "secret_scanning_push_protection": {"status": "enabled"}
}}
EOF
api -X PUT "repos/$repo/vulnerability-alerts"
api -X PUT "repos/$repo/automated-security-fixes"

# A first-time or outside contributor's pull request runs no workflow
# until a maintainer approves it.
step "actions: approval for every outside contributor, pinned actions only"
api -X PUT "repos/$repo/actions/permissions/fork-pr-contributor-approval" \
	-f approval_policy=all_external_contributors
api -X PUT "repos/$repo/actions/permissions" --input - <<'EOF'
{"enabled": true, "allowed_actions": "selected", "sha_pinning_required": true}
EOF
api -X PUT "repos/$repo/actions/permissions/selected-actions" --input - <<'EOF'
{"github_owned_allowed": true, "verified_allowed": false, "patterns_allowed": [
  "golangci/golangci-lint-action@*",
  "pnpm/action-setup@*",
  "contributor-assistant/github-action@*"
]}
EOF

# A published release can't be changed: its assets are what installs and
# the updater check SHA256SUMS against.
step "releases: immutable"
api -X PUT "repos/$repo/immutable-releases"

# main only changes through a pull request whose checks passed. The
# required checks are the jobs that report on every run (a job skipped by
# its if: counts as passed); the race shards are left out, as a skipped
# matrix job reports under its unexpanded name.
step "rulesets: main and release tags"
ruleset() {
	local name=$1 body=$2 id
	id=$(gh api "repos/$repo/rulesets" --jq ".[] | select(.name == \"$name\") | .id")
	if [[ -n "$id" ]]; then
		api -X PUT "repos/$repo/rulesets/$id" --input - <<<"$body"
	else
		api -X POST "repos/$repo/rulesets" --input - <<<"$body"
	fi
}
ruleset main '{
  "name": "main", "target": "branch", "enforcement": "active",
  "conditions": {"ref_name": {"include": ["~DEFAULT_BRANCH"], "exclude": []}},
  "rules": [
    {"type": "deletion"},
    {"type": "non_fast_forward"},
    {"type": "pull_request", "parameters": {
      "required_approving_review_count": 0,
      "dismiss_stale_reviews_on_push": false,
      "require_code_owner_review": false,
      "require_last_push_approval": false,
      "required_review_thread_resolution": false,
      "allowed_merge_methods": ["squash"]}},
    {"type": "required_status_checks", "parameters": {
      "strict_required_status_checks_policy": false,
      "required_status_checks": [
        {"context": "changes"}, {"context": "lint"}, {"context": "test"},
        {"context": "integration"}, {"context": "analytics"},
        {"context": "web"}, {"context": "site"}]}}
  ]
}'
# A v* tag publishes a release (release.yml), so only admins may push one.
ruleset release-tags '{
  "name": "release-tags", "target": "tag", "enforcement": "active",
  "conditions": {"ref_name": {"include": ["refs/tags/v*"], "exclude": []}},
  "bypass_actors": [{"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always"}],
  "rules": [{"type": "creation"}, {"type": "update"}, {"type": "deletion"}]
}'

step "done"
echo "Check https://github.com/$repo/settings/security_analysis and the Actions settings."
echo "Self-hosted runner groups stay closed to public repositories (GitHub's default): leave them so."
