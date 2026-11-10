#!/usr/bin/env bash
# test-rebase-pr.sh — the merge drivers' rebase_pr (merge-seq.sh) against a
# scratch repository with a stub `gh`: a stacked rebuild drops the lower
# layer's commits, a modify/delete conflict STOPs, and a worktree holding a
# commit nobody pushed STOPs instead of pushing it into the pull request.
# Touches nothing outside a temporary directory. CI runs it.
case "${1:-}" in -h | --help)
    awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"
    exit 0
    ;;
esac
set -euo pipefail
SRC=$(cd "$(dirname "$0")/../.." && pwd)
S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
fail() {
    echo "FAIL: $*" >&2
    exit 1
}

mkdir -p "$S/bin" "$S/root"
# Pull requests 1, 2, 3 and 4 are these branches.
printf '#!/bin/sh\ncase "$3" in 1) echo lower ;; 2) echo upper ;; 3) echo moddel ;; 4) echo ahead ;; esac\n' >"$S/bin/gh"
chmod +x "$S/bin/gh"
git init -q --bare "$S/origin.git"
git init -q -b main "$S/root/chintan"
cd "$S/root/chintan"
mkdir -p scripts docs
cp -R "$SRC/scripts/dev" scripts/
cp "$SRC/scripts/backlog-view.py" scripts/
cp "$SRC/docs/backlog.md" "$SRC/docs/backlog-open.md" docs/
echo base >f.txt
echo keep >g.txt
git add -A && git commit -qm base
git remote add origin "$S/origin.git" && git push -q origin main
git checkout -qb lower && echo L >>f.txt && git commit -qam L && git push -q origin lower
lower=$(git rev-parse HEAD)
git checkout -qb upper && echo U >u.txt && git add -A && git commit -qm U && git push -q origin upper
git checkout -q main && git checkout -qb moddel && echo edit >>g.txt && git commit -qam edit && git push -q origin moddel
git checkout -q main && git checkout -qb ahead && echo A >a.txt && git add -A && git commit -qm A && git push -q origin ahead
git checkout -q main && echo L >>f.txt && git rm -q g.txt && git commit -qam "squash of lower; delete g" && git push -q origin main
# The implementer's worktree, one commit ahead of what was pushed.
git worktree add -q "$S/root/wt-ahead" ahead
(cd "$S/root/wt-ahead" && echo secret >UNPUSHED.txt && git add -A && git commit -qm unpushed)
pushed=$(git rev-parse origin/ahead)

sleep() { :; }
export CHINTAN_WT_ROOT="$S/root" PATH="$S/bin:$PATH"
# shellcheck source-path=SCRIPTDIR source=merge-seq.sh
. "$S/root/chintan/scripts/dev/merge-seq.sh"
git fetch -q origin

rc=0
rebase_pr 2 wt-upper "$lower" >/dev/null || rc=$?
[ "$rc" = 0 ] || fail "the stacked rebuild returned $rc"
[ "$(git -C "$S/origin.git" log --format=%s upper | tr '\n' ',')" = "U,squash of lower; delete g,base," ] ||
    fail "the rebuilt upper layer still carries the lower layer's commit"

rc=0
out=$(rebase_pr 3 wt-moddel) || rc=$?
[ "$rc" = 2 ] || fail "a modify/delete conflict returned $rc, want 2"
case "$out" in *"NEEDS HUMAN"*STOP*) ;; *) fail "a modify/delete conflict did not say NEEDS HUMAN and STOP: $out" ;; esac

rc=0
out=$(rebase_pr 4 wt-ahead) || rc=$?
[ "$rc" = 2 ] || fail "a worktree holding an unpushed commit returned $rc, want 2"
[ "$(git -C "$S/origin.git" rev-parse ahead)" = "$pushed" ] || fail "the unpushed commit was pushed"
case "$out" in *STOP*unpushed*) ;; *) fail "the unpushed-commit STOP did not say so: $out" ;; esac

echo "rebase_pr self-test ok"
