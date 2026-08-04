#!/usr/bin/env bash
#
# Admin script tests against the fake-AWS harness (§11.5).
#
# "Scripts that mutate production data are exactly the code that must not be
# untested." Passbook tests its admin tooling this way; the same approach applies
# here, with the requirements §11.5 sets out:
#
#   - every mutating script has a test exercising BOTH --dry-run and --apply
#   - tests run against the fake-AWS harness in CI, with no real AWS credentials
#   - dry-run output is asserted to describe precisely what --apply then does —
#     the assertion that matters most, because a dry-run that lies is worse than
#     no dry-run at all
#   - destructive scripts are tested for refusal when --tenant is omitted (I11)
#
# The fake AWS CLI is a shim earlier on PATH than the real one, so a script under
# test cannot reach a real account even if its arguments are wrong. That is the
# property that makes running these in CI safe.
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
cd "$REPO_ROOT" || exit 1

FAKE_AWS_DIR="$REPO_ROOT/scripts/test/fake-aws"
# The shim goes FIRST on PATH. A script under test that calls `aws` reaches the
# fake, never a real account (§11.5).
export PATH="$FAKE_AWS_DIR:$PATH"
FAKE_AWS_LOG="$(mktemp)"
export FAKE_AWS_LOG
trap 'rm -f "$FAKE_AWS_LOG"' EXIT
# Belt and braces: even with the shim first, unset any real credentials so a
# misconfigured PATH cannot reach an account.
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_PROFILE
export AWS_REGION=ca-central-1

PASS=0
FAIL=0

run_test() {
    local name="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        printf '%s  pass%s %s\n' "$C_GREEN" "$C_OFF" "$name" >&2
        PASS=$((PASS + 1))
    else
        printf '%s  FAIL%s %s\n' "$C_RED" "$C_OFF" "$name" >&2
        FAIL=$((FAIL + 1))
    fi
}

# expect_fail inverts the assertion: used for refusal tests, where the script
# doing nothing is the correct behaviour.
expect_fail() {
    local name="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        printf '%s  FAIL%s %s (succeeded, but should have refused)\n' "$C_RED" "$C_OFF" "$name" >&2
        FAIL=$((FAIL + 1))
    else
        printf '%s  pass%s %s\n' "$C_GREEN" "$C_OFF" "$name" >&2
        PASS=$((PASS + 1))
    fi
}

info "admin script tests (fake-AWS harness, no real credentials)"

# The read-only scripts are testable now; the mutating data scripts arrive with
# the phases that need them (§11.4) and their tests land with them.
run_test "guardrails-check.sh --local-only passes on a clean tree" \
    bash scripts/guardrails-check.sh --local-only

run_test "guardrails-check.sh --self-test proves it can fail (§0.5A)" \
    bash scripts/guardrails-check.sh --self-test

expect_fail "guardrails-check.sh rejects an unknown flag" \
    bash scripts/guardrails-check.sh --not-a-real-flag

# I11 via §11.3: no data operation runs untenanted. Asserted as a refusal,
# because the failure this prevents is a corpus-wide operation running because a
# flag was forgotten.
expect_fail "verify.sh refuses to run without --tenant (I11, §11.3)" \
    bash scripts/verify.sh

run_test "verify.sh --fixtures runs without a tenant (CI mode, §11.6)" \
    bash scripts/verify.sh --fixtures

# Every script must implement --help (§11.3). Checked mechanically rather than
# trusted, since a --help that errors is a script nobody can use at 2am.
while IFS= read -r script; do
    [ -f "$script" ] || continue
    case "$script" in
        */lib/* | */test/* | */dev.sh) continue ;;
    esac
    run_test "$(basename "$script") implements --help (§11.3)" bash "$script" --help
done < <(shell_files | grep -E '^scripts/[^/]+\.sh$')

log ""
if [ "$FAIL" -gt 0 ]; then
    err "$FAIL test(s) failed, $PASS passed"
    exit 1
fi
ok "$PASS test(s) passed"
