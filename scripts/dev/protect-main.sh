#!/usr/bin/env bash
# protect-main.sh — make GitHub enforce the merge rule the repository states
# (README → Operate → Release flow): a ruleset on `main` that admits only a
# squash-merged pull request with the one `CI passed` check green on a head at
# main's current tip (strict: that run is the only test of the tree that
# lands), with no force-push, no deletion, no required reviewers and no bypass
# actors. The `production` environment is scripts/setup.sh's, not this
# script's: one owner, so a fork's `--reviewer` is not undone from here.
#
# Why no bypass actor: `gh pr merge --squash` by an admin satisfies the rules
# when the check is green and is refused when it is not, which is the point.
# A bypass actor would be the way to merge red, so there is none. Rulesets do
# not exempt admins the way legacy branch protection did.
#
#   scripts/dev/protect-main.sh          dry run (default): current state, then what would be sent
#   scripts/dev/protect-main.sh --apply  create or update the ruleset
#
# Idempotent — run it after every edit to this file. `gh` must be logged in as
# a repository admin (rulesets are an admin API). The repository is the
# checkout's origin unless CHINTAN_GH_REPO (owner/name) says otherwise.
case "${1:-}" in -h | --help)
    awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"
    exit 0
    ;;
esac
set -euo pipefail

cd "$(dirname "$0")/../.." || exit 1
REPO=${CHINTAN_GH_REPO:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}
RULESET_NAME=main
REQUIRED_CHECK="CI passed"
GITHUB_ACTIONS_APP_ID=15368 # the GitHub Actions app: only its check counts

APPLY=0
case "${1:-}" in
    "" | --dry-run) ;;
    --apply) APPLY=1 ;;
    *)
        echo "usage: $0 [--dry-run|--apply]" >&2
        exit 2
        ;;
esac

ruleset_body() {
    jq -n --arg name "$RULESET_NAME" --arg check "$REQUIRED_CHECK" --argjson app "$GITHUB_ACTIONS_APP_ID" '{
        name: $name, target: "branch", enforcement: "active",
        conditions: {ref_name: {include: ["~DEFAULT_BRANCH"], exclude: []}},
        bypass_actors: [],
        rules: [
            {type: "deletion"},
            {type: "non_fast_forward"},
            {type: "pull_request", parameters: {
                required_approving_review_count: 0, dismiss_stale_reviews_on_push: false,
                require_code_owner_review: false, require_last_push_approval: false,
                required_review_thread_resolution: false, allowed_merge_methods: ["squash"]}},
            {type: "required_status_checks", parameters: {
                strict_required_status_checks_policy: true, do_not_enforce_on_create: false,
                required_status_checks: [{context: $check, integration_id: $app}]}}
        ]}'
}

echo "== repository: $REPO"
if [ "$(gh api "repos/$REPO" --jq .permissions.admin)" != "true" ]; then
    echo "run this with the repo admin's gh (the active account is not an admin of $REPO)" >&2
    exit 2
fi
echo "== rulesets on $REPO (before)"
rulesets="$(gh api "repos/$REPO/rulesets")"
echo "$rulesets" | jq .
ruleset_id="$(echo "$rulesets" | jq -r --arg n "$RULESET_NAME" '[.[] | select(.name == $n) | .id] | first // empty')"
if [ -n "$ruleset_id" ]; then
    echo "== ruleset \"$RULESET_NAME\" is #$ruleset_id (before)"
    gh api "repos/$REPO/rulesets/$ruleset_id" | jq '{name, enforcement, conditions, bypass_actors, rules}'
fi

echo "== ruleset payload"
ruleset_body | jq .
if [ "$APPLY" -ne 1 ]; then
    echo "== dry run: nothing changed. Re-run with --apply."
    exit 0
fi

if [ -n "$ruleset_id" ]; then
    echo "== updating ruleset #$ruleset_id"
    ruleset_body | gh api --method PUT "repos/$REPO/rulesets/$ruleset_id" --input - >/dev/null
else
    echo "== creating ruleset \"$RULESET_NAME\""
    ruleset_id="$(ruleset_body | gh api --method POST "repos/$REPO/rulesets" --input - --jq .id)"
fi
echo "== ruleset #$ruleset_id (after)"
gh api "repos/$REPO/rulesets/$ruleset_id" | jq '{name, enforcement, conditions, bypass_actors, rules}'
echo "== done: main requires a pull request and \"$REQUIRED_CHECK\""
