#!/usr/bin/env bash
#
# Typecheck the frontend.
#
# The frontend is TypeScript with no framework (docs/decisions/0004), so this is
# `tsc --noEmit` over the source. Dormant until Phase 1 creates it: §0.5A wires
# every check in Phase 0 even where it has nothing to inspect, and a check whose
# subject does not exist passes trivially rather than being skipped — so the day
# the subject appears, the check is already running.
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
cd "$REPO_ROOT" || exit 1

if ! compgen -G "frontend/js/*.ts" >/dev/null 2>&1 && ! compgen -G "frontend/js/**/*.ts" >/dev/null 2>&1; then
    no_subject_yet "frontend typecheck" 1 "frontend/js/*.ts"
    exit 0
fi

# bun ships the TypeScript compiler; --noEmit because bundling is build-frontend's
# job and a typecheck that also emits would race with it.
bun x tsc --noEmit --project frontend/tsconfig.json
ok "frontend typecheck clean"
