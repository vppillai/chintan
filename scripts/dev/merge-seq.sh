#!/usr/bin/env bash
# merge-seq.sh "<pr>:<worktree-dir-name>:<be|fe|infra>" ... — for each pull
# request in order: rebase it if it conflicts with main (scripts/dev/
# resolve-known.py settles the append-only files), bring it up to date with
# main if it is behind, wait for CI on that head, squash-merge, pull main,
# drop the worktree.
#
# It merges only when every check has passed on a head that is at main's
# current tip: the ruleset on main (scripts/dev/protect-main.sh) requires
# up-to-date branches, so a pull request whose base moved reads BEHIND and is
# updated (`gh pr update-branch`, or the rebase path when GitHub refuses) and
# polled again. A check still pending when the polls run out is a STOP, not a
# merge; so is a base that keeps moving. There is no production gate to
# approve — the staging smoke test is the gate, and it needs nobody (owner
# decision D1). The <kind> is kept for the log and for stack-merge.sh's spec
# format.
#
# Paths, all overridable:
#   CHINTAN_WT_ROOT  the directory the worktrees live in   (default: three levels up from this script)
#   CHINTAN_REPO     the main checkout, pulled after a merge (default: $CHINTAN_WT_ROOT/chintan)
#   CHINTAN_CI_POLLS how many 30-second polls to give CI    (default: 40, twenty minutes)
# `gh` resolves the repository from the main checkout, which this script cds into.
set -u
T=$(cd "$(dirname "$0")" && pwd)
WT_ROOT=${CHINTAN_WT_ROOT:-$(cd "$T/../../.." && pwd)}
MAIN=${CHINTAN_REPO:-$WT_ROOT/chintan}
POLLS=${CHINTAN_CI_POLLS:-40}
cd "$MAIN" || exit 2

# Rebase pull request $1 onto origin/main in a worktree ($2 is the directory
# name to use when no worktree holds the branch) and force-push it. Exits 2
# through the caller on a conflict the resolver does not know.
rebase_pr() {
    local n=$1 wt=$2 branch W
    branch=$(gh pr view "$n" --json headRefName --jq .headRefName)
    # Prefer the worktree that already has the branch checked out (an implementer's); else the named one; else make one.
    W=$(git -C "$MAIN" worktree list --porcelain | awk -v b="refs/heads/$branch" '/^worktree /{w=$2} /^branch /{if($2==b)print w}' | head -1)
    [ -n "$W" ] || W=$WT_ROOT/$wt
    [ -d "$W" ] || git -C "$MAIN" worktree add -q "$W" "$branch" 2>/dev/null || git -C "$MAIN" worktree add -q -B "$branch" "$W" "origin/$branch" || {
        echo "STOP: no worktree for $n"
        return 2
    }
    (
        cd "$W" && git checkout -q "$branch" || exit 2
        # A worktree that cannot fast-forward holds commits origin does not: stop rather than rebase them away.
        git pull -q --ff-only origin "$branch" 2>/dev/null || {
            echo "STOP: $W is not at origin/$branch (unpushed commits?)"
            exit 2
        }
        git rebase origin/main >/dev/null 2>&1 || true
        while [ -d .git/rebase-merge ] || [ -d "$(git rev-parse --git-path rebase-merge)" ]; do
            python3 "$T/resolve-known.py" || {
                echo "STOP: manual conflict in $wt"
                git status --short | grep -E "^(UU|AA)"
                exit 2
            }
            GIT_EDITOR=true git rebase --continue >/dev/null 2>&1 || true
        done
        git push -q --force-with-lease origin "$branch" && echo "pushed rebased $wt"
    ) || return 2
    sleep 30
}

for spec in "$@"; do
    n=${spec%%:*}
    rest=${spec#*:}
    wt=${rest%%:*}
    kind=${rest#*:}
    echo "=== PR $n ($wt, $kind)"
    # Each round: conflicts → rebase; behind main → update; then poll CI on
    # the head that results. Main moving during a poll costs another round
    # (one CI run, ~9 min); three rounds without a merge is a STOP.
    ready=0
    for round in 1 2 3; do
        for attempt in 1 2 3; do
            git -C "$MAIN" fetch -q origin
            m=$(gh pr view "$n" --json mergeable --jq .mergeable)
            [ "$m" = "MERGEABLE" ] && break
            [ "$m" = "UNKNOWN" ] && {
                sleep 20
                continue
            }
            echo "conflicting, rebasing $wt (attempt $attempt)"
            rebase_pr "$n" "$wt" || exit 2
        done
        if [ "$(gh pr view "$n" --json mergeStateStatus --jq .mergeStateStatus)" = "BEHIND" ]; then
            echo "PR $n is behind main (round $round); updating its branch"
            if gh pr update-branch "$n" >/dev/null 2>&1; then
                sleep 30
            else
                echo "update-branch refused; rebasing instead"
                rebase_pr "$n" "$wt" || exit 2
            fi
            continue
        fi
        ready=0
        for _ in $(seq 1 "$POLLS"); do
            s=$(gh pr checks "$n" 2>/dev/null | awk -F'\t' '{print $2}' | sort | uniq -c | tr -s ' ' | tr '\n' ' ')
            case "$s" in
                *fail*)
                    echo "STOP: PR $n has failing checks"
                    gh pr checks "$n" | grep -i fail
                    exit 2
                    ;;
            esac
            # An empty list is CI not yet registered (every pull request gets `CI passed`), so it waits like pending.
            case "$s" in
                *pending* | "") sleep 30 ;;
                *)
                    ready=1
                    break
                    ;;
            esac
        done
        [ "$ready" = 1 ] || {
            echo "STOP: CI on PR $n still pending after $POLLS polls; not merging"
            gh pr checks "$n" 2>/dev/null | grep -i pending
            exit 2
        }
        # Green, but main may have moved during the poll: another round updates and re-polls.
        [ "$(gh pr view "$n" --json mergeStateStatus --jq .mergeStateStatus)" = "BEHIND" ] || break
        ready=0
    done
    [ "$ready" = 1 ] || {
        echo "STOP: PR $n fell behind main three times; not merging"
        exit 2
    }
    gh pr merge "$n" --squash --delete-branch >/dev/null 2>&1 || {
        echo "STOP: merge of $n failed"
        gh pr view "$n" --json mergeStateStatus --jq .mergeStateStatus
        exit 2
    }
    sleep 5
    git -C "$MAIN" pull -q --ff-only origin main
    sha=$(git -C "$MAIN" rev-parse --short HEAD)
    echo "merged $n as $sha"
    git -C "$MAIN" worktree remove --force "$WT_ROOT/$wt" 2>/dev/null
    # A backend deploy starts on its own for a merge that touched backend/**, infrastructure/** or the deploy scripts
    # (deploy-backend.yaml's paths), whatever the declared kind; `gh run list --workflow deploy-backend.yaml` follows it.
done
echo "SEQUENCE DONE"
