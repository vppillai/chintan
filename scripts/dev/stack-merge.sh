#!/usr/bin/env bash
# stack-merge.sh "<pr>[,<pr>...]:<kind>" ... — merge each stack bottom-up. The
# bottom merges via merge-seq.sh; every upper layer is rebased --onto
# origin/main from the previous layer's pre-merge tip (drops the squashed
# commits), force-pushed, retargeted to main, then merged via merge-seq.sh.
# Kinds: be|fe|infra (for the log; nothing is gated by kind any more).
#
# It reuses the implementer's worktree, asserts a new HEAD before pushing a
# rebuilt upper, and retargets the uppers to main BEFORE the bottom merges,
# because GitHub closes a pull request whose base branch is deleted.
#
# Paths: CHINTAN_WT_ROOT and CHINTAN_REPO as in merge-seq.sh.
set -u
T=$(cd "$(dirname "$0")" && pwd)
WT_ROOT=${CHINTAN_WT_ROOT:-$(cd "$T/../../.." && pwd)}
MAIN=${CHINTAN_REPO:-$WT_ROOT/chintan}
cd "$MAIN" || exit 2

for spec in "$@"; do
    prs=${spec%%:*}
    kind=${spec#*:}
    prev_tip=""
    IFS=, read -r -a layers <<<"$prs"
    if [ "${#layers[@]}" -gt 1 ]; then
        for u in "${layers[@]:1}"; do
            gh pr edit "$u" --base main >/dev/null 2>&1 || echo "WARN: could not retarget $u to main"
        done
    fi
    for n in "${layers[@]}"; do
        branch=$(gh pr view "$n" --json headRefName --jq .headRefName)
        tip=$(gh pr view "$n" --json headRefOid --jq .headRefOid)
        if [ -n "$prev_tip" ]; then
            echo "=== rebuilding PR $n ($branch) onto main, dropping $prev_tip.."
            git -C "$MAIN" fetch -q origin
            # Use the worktree that already has the branch checked out (an implementer's), else make one.
            W=$(git -C "$MAIN" worktree list --porcelain | awk -v b="refs/heads/$branch" '/^worktree /{w=$2} /^branch /{if($2==b)print w}' | head -1)
            if [ -z "$W" ]; then
                W=$WT_ROOT/wt-stack-$n
                git -C "$MAIN" worktree add -q "$W" "$branch" 2>/dev/null || git -C "$MAIN" worktree add -q -B "$branch" "$W" "origin/$branch" || {
                    echo "STOP: cannot make a worktree for $n"
                    exit 2
                }
            fi
            [ -d "$W" ] || {
                echo "STOP: no worktree dir for $n"
                exit 2
            }
            (
                set -e
                cd "$W" && git checkout -q "$branch" && git reset -q --hard "origin/$branch" && (
                    git rebase --onto origin/main "$prev_tip" "$branch" >/dev/null 2>&1 || {
                        while [ -d "$(git rev-parse --git-path rebase-merge)" ]; do
                            python3 "$T/resolve-known.py" || {
                                echo "STOP: manual conflict rebuilding $n"
                                git status --short | grep -E "^(UU|AA)"
                                exit 2
                            }
                            GIT_EDITOR=true git rebase --continue >/dev/null 2>&1 || true
                        done
                    }
                )
                [ "$(git rev-parse HEAD)" != "$tip" ] || {
                    echo "STOP: rebuild of $n produced no new commit"
                    exit 2
                }
                git push -q --force-with-lease origin "$branch" && echo "pushed rebuilt $n at $(git rev-parse --short HEAD)"
            ) || {
                echo "STOP: rebuild failed for $n"
                exit 2
            }
            gh pr edit "$n" --base main >/dev/null 2>&1 || true
            sleep 90
        fi
        echo "=== merging PR $n ($kind)"
        "$T/merge-seq.sh" "$n:wt-stack-$n:$kind" | grep -v "^remote" | grep -E "^===|merged|STOP|SEQUENCE|NEEDS|rebasing|pending" || true
        if ! gh pr view "$n" --json state --jq .state | grep -q MERGED; then
            echo "STOP: $n did not merge"
            exit 2
        fi
        prev_tip=$tip
        [ -d "$WT_ROOT/wt-stack-$n" ] && git -C "$MAIN" worktree remove --force "$WT_ROOT/wt-stack-$n" 2>/dev/null
        git -C "$MAIN" worktree prune
    done
done
echo "STACKS DONE"
