#!/usr/bin/env bash
# stack-merge.sh "<pr>[,<pr>...]:<kind>" ... — merge each stack bottom-up. The
# bottom merges via merge-seq.sh; every upper layer is rebased --onto
# origin/main from the previous layer's pre-merge tip (drops the squashed
# commits), force-pushed, retargeted to main, then merged via merge-seq.sh.
# Kinds: be|fe|infra (for the log; nothing is gated by kind any more).
#
# The rebuild is merge-seq.sh's rebase_pr with the previous tip to drop, so it
# reuses the implementer's worktree and STOPs, as that does, on a worktree
# holding unpushed commits or a conflict resolve-known.py does not settle. The
# uppers are retargeted to main BEFORE the bottom merges, because GitHub closes
# a pull request whose base branch is deleted. It takes no flags and acts at
# once: there is no dry run.
#
# Paths: CHINTAN_WT_ROOT and CHINTAN_REPO as in merge-seq.sh.
case "${1:-}" in -h | --help)
    awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"
    exit 0
    ;;
esac
set -u
# T, WT_ROOT, MAIN and rebase_pr; it also cds into the main checkout.
# shellcheck source-path=SCRIPTDIR source=merge-seq.sh
. "$(dirname "$0")/merge-seq.sh"

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
        tip=$(gh pr view "$n" --json headRefOid --jq .headRefOid)
        if [ -n "$prev_tip" ]; then
            echo "=== rebuilding PR $n onto main, dropping $prev_tip.."
            git -C "$MAIN" fetch -q origin
            rebase_pr "$n" "wt-stack-$n" "$prev_tip" || {
                echo "STOP: rebuild failed for $n"
                exit 2
            }
            [ "$(gh pr view "$n" --json headRefOid --jq .headRefOid)" != "$tip" ] || {
                echo "STOP: rebuild of $n produced no new commit"
                exit 2
            }
            gh pr edit "$n" --base main >/dev/null 2>&1 || true
            sleep 60
        fi
        echo "=== merging PR $n ($kind)"
        "$T/merge-seq.sh" "$n:wt-stack-$n:$kind" | grep -v "^remote" | grep -E "^===|merged|STOP|SEQUENCE|NEEDS|WARN|kept|rebasing|pending" || true
        if ! gh pr view "$n" --json state --jq .state | grep -q MERGED; then
            echo "STOP: $n did not merge"
            exit 2
        fi
        prev_tip=$tip
        git -C "$MAIN" worktree prune
    done
done
echo "STACKS DONE"
