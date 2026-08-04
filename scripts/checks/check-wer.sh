#!/usr/bin/env bash
#
# Golden-fixture WER regression (§12, §11A.8, §0.5A). Active: Phase 2.
#
# What it prevents: a capture or cleanup change quietly degrading accuracy.
#
# The fixture set is 10 real recordings OF THE PRIMARY USER'S OWN VOICE with
# hand-verified transcripts (§12). This constraint is not incidental: published
# WER figures are dominated by US and UK English, error rates on Indian-accented
# English are materially higher, and a model scoring well on LibriSpeech tells you
# nothing useful here (§1.2, G-045). Public datasets and other speakers measure
# the wrong thing and are not substitutes.
#
# At least two fixtures must contain code-switching if it occurs in normal use —
# Whisper handles mid-sentence language switches poorly, and it is the primary
# user's own speech that triggers it (§12, §1.2).
#
# Runs on every change to the capture or cleanup path and fails the build on
# regression against the baseline recorded for the current release tag (§11A.8).
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
AS_JSON="${1:-}"
cd "$REPO_ROOT" || exit 1

FIXTURES=tests/fixtures/golden-audio

if [ ! -d "$FIXTURES" ]; then
    no_subject_yet "golden-fixture WER" 2 "$FIXTURES"
    finish_check "golden-fixture WER regression (§12)" "$AS_JSON"
    exit 0
fi

# Once fixtures exist the check is real, and its first job is to assert the
# fixture set actually satisfies §12 — a WER suite built on the wrong recordings
# reports a confident number about nothing.
count="$(find "$FIXTURES" -name '*.opus' -o -name '*.wav' -o -name '*.mp3' 2>/dev/null | wc -l | tr -d ' ')"
if [ "$count" -lt 10 ]; then
    violation "$FIXTURES holds $count recordings; §12 requires 10 of the primary user's own voice"
fi
if [ ! -f "$FIXTURES/MANIFEST.md" ]; then
    violation "$FIXTURES has no MANIFEST.md recording speaker, conditions, and which fixtures contain code-switching (§12, §1.2)"
elif ! grep -qi 'code-switch\|code-mix' "$FIXTURES/MANIFEST.md"; then
    violation "$FIXTURES/MANIFEST.md does not state which fixtures contain code-switching; §12 requires at least two if it occurs in normal use"
fi

# The WER computation itself lives in the admin binary, not in bash: it is real
# logic and belongs in tested application code (§11.2).
if ! (cd backend && go run ./cmd/chintanctl metrics wer --fixtures "../$FIXTURES" --compare-baseline); then
    violation "golden-fixture WER regressed against the recorded baseline (§11A.8)"
fi

finish_check "golden-fixture WER regression (§12)" "$AS_JSON"
