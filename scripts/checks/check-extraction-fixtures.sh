#!/usr/bin/env bash
#
# Extraction fixture assertions (§11A.8, §0.5A). Active: Phase 3.
#
# What it prevents: classification regressions, especially on `prompt`.
#
# A fixed set of brain dumps with expected item kinds, asserted on every change to
# the extraction path. The specific failure being watched for is a `prompt`
# misclassified as an `idea` — because an idea gets summarised, and summarising a
# prompt destroys the artifact (§3A.3, A4). §11A.4 requires prompt precision and
# recall to be tracked separately and weighted highest for exactly this reason.
#
# The fixture set is 30 brain dumps containing long verbatim architecture dumps
# mixed with actions and ideas (§Phase 3 entry gate).
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
AS_JSON="${1:-}"
cd "$REPO_ROOT" || exit 1

FIXTURES=tests/fixtures/extraction

if [ ! -d "$FIXTURES" ]; then
    no_subject_yet "extraction fixtures" 3 "$FIXTURES"
    finish_check "extraction fixture assertions (§11A.8)" "$AS_JSON"
    exit 0
fi

if ! (cd backend && go run ./cmd/chintanctl extract verify-fixtures --dir "../$FIXTURES"); then
    violation "extraction fixture assertions failed — check prompt precision first (§11A.4, A4)"
fi

finish_check "extraction fixture assertions (§11A.8)" "$AS_JSON"
