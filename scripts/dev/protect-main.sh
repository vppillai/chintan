#!/usr/bin/env bash
# protect-main.sh — make GitHub enforce the merge rule the repository states
# (owner decision D1 in docs/reviews/2026-10-01/platform-review.md):
#
#   * a ruleset on `main`: changes arrive by pull request, squash-merged, with
#     the one `CI passed` check from ci.yaml green on a head that is at main's
#     current tip (strict status checks: with no push-to-main CI run and no
#     deploy-time test, the pull request's run is the only test of the tree
#     that lands, so it must be of that tree); no force-push, no deletion,
#     no required reviewers and no bypass actors;
#   * the `production` environment keeps its wait timer and branch policy and
#     loses its required reviewer — the staging smoke test and rollback in
#     deploy-backend.yaml are the production gate; a reviewer a script approves
#     is not one.
#
# Why no bypass actor: `gh pr merge --squash` by an admin satisfies the rules
# when the check is green and is refused when it is not, which is the point.
# A bypass actor would be the way to merge red, so there is none. Rulesets do
# not exempt admins the way legacy branch protection did.
#
#   scripts/dev/protect-main.sh          dry run (default): current state, then what would be sent
#   scripts/dev/protect-main.sh --apply  create or update the ruleset, rewrite the environment
#
# Idempotent — run it after every edit to this file. `gh` must be logged in as
# a repository admin (rulesets are an admin API). The repository is the
# checkout's origin unless CHINTAN_GH_REPO (owner/name) says otherwise.
set -euo pipefail

cd "$(dirname "$0")/../.." || exit 1
REPO=${CHINTAN_GH_REPO:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}
RULESET_NAME=main
ENV_NAME=production
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

# The environment PUT is declarative: what it does not name is removed. The
# wait timer and the branch policy are carried over from the current state so
# only the reviewers change.
environment_body() {
    jq '{
        wait_timer: ([.protection_rules[]? | select(.type == "wait_timer") | .wait_timer] | first // 0),
        prevent_self_review: false,
        reviewers: [],
        deployment_branch_policy: (.deployment_branch_policy // {protected_branches: true, custom_branch_policies: false})
    }'
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

echo "== environment $ENV_NAME (before)"
env_before="$(gh api "repos/$REPO/environments/$ENV_NAME")"
echo "$env_before" | jq '{id, name, protection_rules: [.protection_rules[]? | {type, wait_timer, reviewers: [.reviewers[]? | .reviewer.login]}], deployment_branch_policy}'

echo "== ruleset payload"
ruleset_body | jq .
echo "== environment payload"
echo "$env_before" | environment_body | jq .

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
echo "== rewriting environment $ENV_NAME"
echo "$env_before" | environment_body | gh api --method PUT "repos/$REPO/environments/$ENV_NAME" --input - >/dev/null

echo "== ruleset #$ruleset_id (after)"
gh api "repos/$REPO/rulesets/$ruleset_id" | jq '{name, enforcement, conditions, bypass_actors, rules}'
echo "== environment $ENV_NAME (after)"
env_after="$(gh api "repos/$REPO/environments/$ENV_NAME")"
echo "$env_after" | jq '{id, name, protection_rules: [.protection_rules[]? | {type, wait_timer, reviewers: [.reviewers[]? | .reviewer.login]}], deployment_branch_policy}'
if echo "$env_after" | jq -e '[.protection_rules[]? | select(.type == "required_reviewers")] | length > 0' >/dev/null; then
    echo "WARNING: $ENV_NAME still lists a required reviewer; remove it in Settings → Environments → $ENV_NAME, then re-run this script to confirm." >&2
    exit 1
fi
echo "== done: main requires a pull request and \"$REQUIRED_CHECK\"; $ENV_NAME has no reviewer"
