#!/usr/bin/env bash
#
# Trigger-additivity diff check (§5.2 rule 2, §0.5A). Active: Phase 8.
#
# What it prevents: the §5 abstraction guarantee being asserted rather than
# verified.
#
# §5.2 rule 2: "Adding Phase 8 hardware means: write the adapter file, add one
# line to the registry. No other file changes. If Phase 8 requires touching
# RecorderController, the abstraction was built wrong."
#
# §Phase 8 acceptance restates it as a diff assertion: "triggers.enabled gains the
# new adapters and no file outside triggers/ and the registry line was modified —
# the §5.2 rule, verified by diff."
#
# So this check compares a change against that constraint. It only engages for a
# change that adds a trigger adapter — an ordinary change elsewhere is not
# constrained by rule 2 — which is why it reads the diff rather than the tree.
#
# §Phase 1 acceptance already tests the same property from the other direction:
# "Adding a no-op third trigger adapter requires exactly one line changed outside
# its own file." That test proves the abstraction is right when it is built; this
# check proves it stayed right.
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
AS_JSON="${1:-}"
cd "$REPO_ROOT" || exit 1

TRIGGER_DIR=frontend/js/triggers

if [ ! -d "$TRIGGER_DIR" ]; then
    no_subject_yet "trigger additivity" 1 "$TRIGGER_DIR"
    finish_check "trigger-additivity diff check (§5.2 rule 2)" "$AS_JSON"
    exit 0
fi

# Compare against the merge base when CI supplies one, else the previous commit.
BASE="${CHINTAN_DIFF_BASE:-}"
if [ -z "$BASE" ]; then
    BASE="$(git merge-base HEAD origin/main 2>/dev/null || git rev-parse HEAD~1 2>/dev/null || echo '')"
fi
if [ -z "$BASE" ]; then
    dim "  no diff base available (shallow clone or first commit) — nothing to compare"
    finish_check "trigger-additivity diff check (§5.2 rule 2)" "$AS_JSON"
    exit 0
fi

mapfile -t changed < <(git diff --name-only "$BASE" HEAD -- 2>/dev/null || true)

# Does this change add a new adapter? Only then does rule 2 apply.
adds_adapter=0
for f in "${changed[@]}"; do
    case "$f" in
        "$TRIGGER_DIR"/*)
            if ! git cat-file -e "$BASE:$f" 2>/dev/null; then
                adds_adapter=1
            fi
            ;;
    esac
done

if [ "$adds_adapter" = "0" ]; then
    dim "  no new trigger adapter in this change — rule 2 does not apply"
    finish_check "trigger-additivity diff check (§5.2 rule 2)" "$AS_JSON"
    exit 0
fi

info "this change adds a trigger adapter; §5.2 rule 2 applies"
for f in "${changed[@]}"; do
    case "$f" in
        "$TRIGGER_DIR"/*) continue ;;               # the adapter itself
        frontend/js/triggerRegistry.ts) continue ;; # the one registry line
        config/instances/*.yaml) continue ;;        # triggers.enabled
        docs/* | CHANGELOG.md) continue ;;          # documentation
        tests/* | frontend/test/*) continue ;;      # its tests
        '') continue ;;
        *)
            violation "$f was modified while adding a trigger adapter — Phase 8 must touch only $TRIGGER_DIR and the registry line (§5.2 rule 2). If RecorderController needed changing, the abstraction was built wrong."
            ;;
    esac
done

# The registry must be a single line change, not a restructure.
if git cat-file -e "$BASE:frontend/js/triggerRegistry.ts" 2>/dev/null; then
    added="$(git diff --numstat "$BASE" HEAD -- frontend/js/triggerRegistry.ts 2>/dev/null | cut -f1)"
    if [ -n "$added" ] && [ "$added" -gt 2 ]; then
        violation "triggerRegistry.ts gained $added lines; §5.2 rule 2 allows one registry line per adapter"
    fi
fi

finish_check "trigger-additivity diff check (§5.2 rule 2)" "$AS_JSON"
