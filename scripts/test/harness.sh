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

# ---------------------------------------------------------------------------
# cleanup-aws.sh — the sweep (§11.4, Phase 0)
# ---------------------------------------------------------------------------
#
# The only mutating script that exists yet, and the one §11.5 was written for: it
# deletes infrastructure in an account shared with passbook, and IAM will not stop
# it if it gets the classification wrong (`aws:ResourceTag` is unsupported for
# authorization on nearly every service it touches — G-067). The refusal logic IS
# the control, so these tests are the only thing standing behind it.
#
# What each test below is really asserting, since a list of names hides it:
#   - the plan is exactly the orphans and nothing adjacent to them
#   - --apply calls exactly the APIs the plan named, once each
#   - every refusal happens BEFORE any delete, proven by an empty mutation log

CLEANUP_FIXTURES="$REPO_ROOT/scripts/test/fixtures"

# cleanup_test is run_test with the failure output kept. Swallowing a script's
# chatter is right; swallowing an assertion's diff is not — a plan comparison that
# fails is unreadable without both plans in front of you.
cleanup_test() {
    local name="$1"
    shift
    local out
    out="$(mktemp)"
    if "$@" >"$out" 2>&1; then
        printf '%s  pass%s %s\n' "$C_GREEN" "$C_OFF" "$name" >&2
        PASS=$((PASS + 1))
    else
        printf '%s  FAIL%s %s\n' "$C_RED" "$C_OFF" "$name" >&2
        sed 's/^/      /' "$out" >&2
        FAIL=$((FAIL + 1))
    fi
    rm -f "$out"
}

assert_eq() {
    local what="$1" want="$2" got="$3"
    if [ "$want" != "$got" ]; then
        printf 'assertion failed: %s\n--- want\n%s\n--- got\n%s\n' "$what" "$want" "$got"
        return 1
    fi
}

# sweep runs the script against a fixture set with a mutation log of its own, so a
# test asserts on exactly what its own invocation called. Stdout is the --json
# document. Stderr is deliberately NOT swallowed here: it carries the refusal
# messages, one test asserts on them, and cleanup_test only surfaces it when
# something has already failed.
#
# The scenario argument is a colon-separated search path of fixture directories,
# resolved against scripts/test/fixtures here so call sites read as scenario names:
# "cleanup-aws-root:cleanup-aws" overrides the caller identity and inherits every other
# response from the base scenario.
sweep() { # sweep <scenario[:base...]> <mutation-log> [flags...]
    local fixture="$1" mutlog="$2"
    shift 2
    local path="" part parts
    IFS=: read -ra parts <<<"$fixture"
    for part in "${parts[@]}"; do path="${path:+$path:}${CLEANUP_FIXTURES}/${part}"; done
    : >"$mutlog"
    FAKE_AWS_FIXTURES="$path" FAKE_AWS_LOG="$mutlog" \
        bash scripts/cleanup-aws.sh --json "$@"
}

# The three orphans in the cleanup-aws fixture set, written out here rather than
# derived: a test that computes its expectation from the same data the script reads
# would agree with the script about a shared misreading.
CLEANUP_EXPECTED_PLAN="cloudformation:stack arn:aws:cloudformation:ca-central-1:000000000000:stack/voicenotes-dev-failed/2222
lambda:function arn:aws:lambda:ca-central-1:000000000000:function:voicenotes-api-prod
lambda:function arn:aws:lambda:ca-central-1:000000000000:function:voicenotes-ingest-dev"

plan_of() { printf '%s' "$1" | jq -r '.plan[] | "\(.kind) \(.arn)"' | LC_ALL=C sort; }

# mutations canonicalises the fake CLI's log to one "service operation arn" line per
# mutating call. `cloudformation wait` is dropped: it polls for a deletion already
# issued, so counting it would make the call count disagree with the plan length for
# a reason that is not a defect.
mutations() {
    grep -v '^cloudformation wait ' "$1" 2>/dev/null |
        sed -E 's/^([a-z0-9-]+) ([a-z-]+) .*(arn:[^ ]+).*$/\1 \2 \3/' | LC_ALL=C sort || true
}

# expected_calls maps each plan entry to the API call it promises. The mapping lives
# in the test, deliberately: it is the assertion that "kind" is not decorative. A
# handler added to the script without a line here produces UNMAPPED and fails, which
# is §11.5's "every mutating script has a test" enforced rather than remembered.
expected_calls() {
    printf '%s' "$1" | jq -r '.plan[] |
        if   .kind == "cloudformation:stack" then "cloudformation delete-stack \(.arn)"
        elif .kind == "lambda:function"      then "lambda delete-function \(.arn)"
        else "UNMAPPED-KIND \(.kind)" end' | LC_ALL=C sort
}

t_cleanup_dry_run_default() {
    local mutlog json
    mutlog="$(mktemp)"
    json="$(sweep cleanup-aws "$mutlog")" || return 1
    assert_eq "plan under the default (dry) invocation" \
        "$(printf '%s' "$CLEANUP_EXPECTED_PLAN" | LC_ALL=C sort)" "$(plan_of "$json")" || return 1
    assert_eq "dry_run flag in --json" "true" "$(printf '%s' "$json" | jq -r .dry_run)" || return 1
    # The convention that matters most (§11.3): a mistaken invocation prints a plan.
    assert_eq "mutating calls made by a dry run" "" "$(mutations "$mutlog")" || return 1
    rm -f "$mutlog"
}

t_cleanup_apply_matches_dry_run() {
    local dry_log apply_log dry apply
    dry_log="$(mktemp)"
    apply_log="$(mktemp)"
    dry="$(sweep cleanup-aws "$dry_log")" || return 1
    apply="$(sweep cleanup-aws "$apply_log" --apply)" || return 1

    # §11.5: "dry-run output is asserted to describe precisely what --apply then
    # does." Three separate claims, because each can break alone: the same plan is
    # produced, the calls made are exactly that plan's calls, and the count matches
    # so an extra deletion cannot hide behind a correct-looking set.
    assert_eq "plan identical between dry run and --apply" "$(plan_of "$dry")" "$(plan_of "$apply")" || return 1
    assert_eq "API calls made by --apply" "$(expected_calls "$dry")" "$(mutations "$apply_log")" || return 1
    assert_eq "number of deletions" "$(printf '%s' "$dry" | jq -r '.plan | length')" \
        "$(printf '%s' "$apply" | jq -r .deleted)" || return 1
    assert_eq "delete failures" "0" "$(printf '%s' "$apply" | jq -r .delete_failures)" || return 1
    assert_eq "mutating calls made by the dry run" "" "$(mutations "$dry_log")" || return 1
    rm -f "$dry_log" "$apply_log"
}

# The resources whose deletion would be unrecoverable, plus the one saved only by
# Protected=true and the passbook function mistagged for this project. None may appear
# in a plan, and none may be touched by --apply. This is the test that has to hold
# when everything else fails.
#
# voicenotes-probe-dev earns its place: it is an unclaimed, correctly-prefixed Lambda
# — identical to the orphan that IS swept except for the Protected tag. Without it,
# deleting the Protected gate outright left every test green, because the table and
# bucket are refused a second time for holding the corpus.
t_cleanup_never_touches_protected() {
    local mutlog json calls
    mutlog="$(mktemp)"
    json="$(sweep cleanup-aws "$mutlog" --apply)" || return 1
    calls="$(cat "$mutlog")"
    local arn
    for arn in \
        "arn:aws:lambda:ca-central-1:000000000000:function:voicenotes-probe-dev" \
        "arn:aws:dynamodb:ca-central-1:000000000000:table/voicenotes-dev" \
        "arn:aws:s3:::voicenotes-dev-000000000000-ca-central-1" \
        "arn:aws:cognito-idp:ca-central-1:000000000000:userpool/ca-central-1_devpool01" \
        "arn:aws:lambda:ca-central-1:000000000000:function:passbook-notifier"; do
        assert_eq "refused, not planned: $arn" "" \
            "$(printf '%s' "$json" | jq -r --arg a "$arn" '.plan[] | select(.arn == $a) | .arn')" || return 1
        assert_eq "present in the refusal list: $arn" "$arn" \
            "$(printf '%s' "$json" | jq -r --arg a "$arn" '.refused[] | select(.arn == $a) | .arn')" || return 1
        case "$calls" in
            *"$arn"*)
                printf 'assertion failed: --apply called an API against %s\n%s\n' "$arn" "$calls"
                return 1
                ;;
        esac
    done
    # Name-based, not ARN-based: any call naming passbook at all is a failure,
    # whatever the resource. A shared account is what makes an over-broad sweep
    # catastrophic rather than annoying (§10.3).
    case "$calls" in
        *passbook*)
            printf 'assertion failed: --apply named passbook in a call\n%s\n' "$calls"
            return 1
            ;;
    esac
    rm -f "$mutlog"
}

t_cleanup_instance_filter() {
    local mutlog dev prod none
    mutlog="$(mktemp)"
    dev="$(sweep cleanup-aws "$mutlog" --instance dev)" || return 1
    prod="$(sweep cleanup-aws "$mutlog" --instance prod)" || return 1
    none="$(sweep cleanup-aws "$mutlog" --instance staging)" || return 1
    assert_eq "--instance dev plan" \
        "cloudformation:stack arn:aws:cloudformation:ca-central-1:000000000000:stack/voicenotes-dev-failed/2222
lambda:function arn:aws:lambda:ca-central-1:000000000000:function:voicenotes-ingest-dev" \
        "$(plan_of "$dev")" || return 1
    assert_eq "--instance prod plan" \
        "lambda:function arn:aws:lambda:ca-central-1:000000000000:function:voicenotes-api-prod" \
        "$(plan_of "$prod")" || return 1
    # An instance with nothing tagged for it plans nothing. The failure this catches
    # is a filter that matches everything when it matches nothing.
    assert_eq "unknown instance plans nothing" "0" "$(printf '%s' "$none" | jq -r .planned)" || return 1
    rm -f "$mutlog"
}

# A foreign stack this principal cannot read is tolerated rather than fatal, and the
# reason is load-bearing: §10.3 makes the project prefix mandatory, so another
# project's stack cannot own a voicenotes-* resource, and nothing without that prefix
# is ever deleted. The test pins both halves — the run proceeds, and it says so.
t_cleanup_foreign_unreadable_stack_tolerated() {
    local mutlog json
    mutlog="$(mktemp)"
    json="$(FAKE_AWS_UNREADABLE="passbook-prod/5555" sweep cleanup-aws "$mutlog")" || return 1
    assert_eq "plan is unchanged by an unreadable foreign stack" \
        "$(printf '%s' "$CLEANUP_EXPECTED_PLAN" | LC_ALL=C sort)" "$(plan_of "$json")" || return 1
    assert_eq "the unreadable stack is reported, not silently skipped" "passbook-prod" \
        "$(printf '%s' "$json" | jq -r '.unreadable_stacks[0]')" || return 1
    rm -f "$mutlog"
}

# refuses asserts three things about a refusal, and each one caught a real defect while
# these tests were being written:
#
#   the exit code       — 3 is "refused to run", 1 is "a deletion failed", 2 is "bad
#                         arguments". A test that only asserted non-zero would pass on
#                         a crash.
#   the reason          — three different branches exit 3, so the code alone does not
#                         identify which guard fired. Both the root and the in-flight
#                         tests passed with their guard deleted outright, because the
#                         run then refused on an unrelated branch with the same code.
#   an empty log        — the refusal came before any delete, not partway through them.
refuses() { # refuses <want-exit> <scenario> <unreadable-pattern|""> <reason-pattern> [flags...]
    local want="$1" fixture="$2" unreadable="$3" reason="$4"
    shift 4
    local mutlog errlog rc=0
    mutlog="$(mktemp)"
    errlog="$(mktemp)"
    (
        export FAKE_AWS_UNREADABLE="$unreadable"
        sweep "$fixture" "$mutlog" "$@" >/dev/null 2>"$errlog"
    ) || rc=$?
    assert_eq "exit code" "$want" "$rc" || return 1
    # -e, because a reason pattern legitimately starts with "--" (the --tenant refusal)
    # and grep would otherwise read it as a flag and fail with a usage error.
    if ! grep -qF -e "$reason" "$errlog"; then
        printf 'assertion failed: refused, but not for the expected reason\n--- want to find\n%s\n--- stderr\n%s\n' \
            "$reason" "$(cat "$errlog")"
        return 1
    fi
    assert_eq "mutating calls made before refusing" "" "$(cat "$mutlog")" || return 1
    rm -f "$mutlog" "$errlog"
}

info "cleanup-aws.sh (§11.4) — plan, apply, and every refusal"

cleanup_test "cleanup-aws.sh --dry-run is the default and changes nothing (§11.3)" \
    t_cleanup_dry_run_default

cleanup_test "cleanup-aws.sh --apply does exactly what the dry run described (§11.5)" \
    t_cleanup_apply_matches_dry_run

cleanup_test "cleanup-aws.sh never touches the corpus, identities, or passbook (I1, I2, §10.3)" \
    t_cleanup_never_touches_protected

cleanup_test "cleanup-aws.sh --instance narrows the sweep" \
    t_cleanup_instance_filter

cleanup_test "cleanup-aws.sh proceeds past a foreign stack it cannot read (§10.3)" \
    t_cleanup_foreign_unreadable_stack_tolerated

# The inverse of the usual --tenant test (§11.3): a LIFECYCLE script must not accept
# it. Passing it means the operator has mistaken this for a data script, and silently
# ignoring the flag is how a real one gets ignored later.
cleanup_test "cleanup-aws.sh rejects --tenant — infrastructure has no tenant (§11.3)" \
    refuses 2 cleanup-aws "" "--tenant is not accepted" --tenant t_personal

# Root bypasses every deny in the boundary (§9.4), so it is the one caller with no
# backstop under this script at all.
cleanup_test "cleanup-aws.sh refuses to sweep as the account root user (§9.4)" \
    refuses 3 "cleanup-aws-root:cleanup-aws" "" "refusing to sweep as the account root user"

# Fail-closed, three ways. Each of these makes some part of the inventory invisible,
# and treating any of them as "nothing is claimed" would put the live deployment in
# the plan — which is the one failure a sweep must not have.
cleanup_test "cleanup-aws.sh refuses when the stack inventory cannot be read" \
    refuses 3 cleanup-aws "cloudformation:describe-stacks" "cannot enumerate CloudFormation stacks"

cleanup_test "cleanup-aws.sh refuses when its own stack's resources cannot be read" \
    refuses 3 cleanup-aws "voicenotes-dev/1111" "cannot read the resources of voicenotes stack"

cleanup_test "cleanup-aws.sh refuses when the Project Resource Group cannot be queried" \
    refuses 3 cleanup-aws "resourcegroupstaggingapi:get-resources" "cannot query the voicenotes Resource Group"

# A resource created seconds ago carries its tags and is not yet claimed by its stack,
# so a sweep during a deploy deletes what the deploy is building. The scenario inherits
# the base tag query, which is what makes the test specific: with the guard removed the
# run produces a plan and exits 0 instead of refusing.
cleanup_test "cleanup-aws.sh refuses while a deploy is in flight" \
    refuses 3 "cleanup-aws-inflight:cleanup-aws" "" "a deploy is in flight"

# ---------------------------------------------------------------------------
# users.sh — Cognito user management (§11.4, Phase 0)
# ---------------------------------------------------------------------------
#
# §11.3 puts this script across the line: it takes --tenant and writes an audit record,
# "because [it] change[s] who can reach tenant data". So the tests here assert three
# things that no other script's tests can:
#
#   - a dry run writes the audit record and NOTHING else. That is the whole of what a
#     dry run of this script does, and it is the assertion that proves --dry-run cannot
#     reach Cognito.
#   - --apply calls exactly what the dry run's plan named, and records the action the
#     dry run said it would (§11.5 — a dry-run that lies is worse than no dry-run).
#   - no email address reaches the audit record (§9.2). The record is the
#     longest-retained store in the system and has no delete path, so an address in it
#     is not a leak that can be cleaned up.
#
# `add` re-run against an existing user is tested for the specific failure §Phase 0
# cares about: a second invite going out silently, which mints a new temporary password
# and breaks the one the user is part-way through using.

USERS_FIXTURES="$REPO_ROOT/scripts/test/fixtures"

# Own helpers rather than shared ones, deliberately: these blocks are added by separate
# agents and a shared assertion helper is a shared dependency between them.
users_test() {
    local name="$1"
    shift
    local out
    out="$(mktemp)"
    if "$@" >"$out" 2>&1; then
        printf '%s  pass%s %s\n' "$C_GREEN" "$C_OFF" "$name" >&2
        PASS=$((PASS + 1))
    else
        printf '%s  FAIL%s %s\n' "$C_RED" "$C_OFF" "$name" >&2
        sed 's/^/      /' "$out" >&2
        FAIL=$((FAIL + 1))
    fi
    rm -f "$out"
}

users_eq() {
    local what="$1" want="$2" got="$3"
    if [ "$want" != "$got" ]; then
        printf 'assertion failed: %s\n--- want\n%s\n--- got\n%s\n' "$what" "$want" "$got"
        return 1
    fi
}

# users_run invokes the script against a fixture set with its own mutation log, so each
# test asserts on exactly what its own invocation called. Stdout is the --json document;
# the human-readable progress on stderr is dropped.
users_run() { # users_run <fixture-dir> <mutation-log> [args...]
    local fixture="$1" mutlog="$2"
    shift 2
    : >"$mutlog"
    FAKE_AWS_FIXTURES="$USERS_FIXTURES/$fixture" FAKE_AWS_LOG="$mutlog" \
        bash scripts/users.sh --json --instance dev --tenant t-vp "$@" 2>/dev/null
}

# users_calls reduces the fake CLI's log to one "service operation" line per mutating
# call. Sorted, so a test asserts on the set rather than on an ordering the script is
# free to change.
users_calls() { awk '{ print $1, $2 }' "$1" 2>/dev/null | LC_ALL=C sort || true; }

USERS_SUBJECT="someone@example.com"
# The digest chintanctl derives from that address. Written out rather than computed here
# for the same reason cleanup-aws's expected plan is: a test that derives its
# expectation the same way the code does agrees with the code about a shared mistake.
# Reproduce it with: printf '%s' someone@example.com | sha256sum | cut -c1-16
USERS_DIGEST="72497f475e4f76d0"

t_users_dry_run_writes_only_the_audit_record() {
    local mutlog json
    mutlog="$(mktemp)"
    json="$(users_run users-absent "$mutlog" add "$USERS_SUBJECT")" || return 1

    users_eq "dry_run reported" "true" "$(printf '%s' "$json" | jq -r .dry_run)" || return 1
    users_eq "the plan --apply would execute" \
        "dynamodb put-item
cognito-idp admin-create-user" "$(printf '%s' "$json" | jq -r '.plan[]')" || return 1
    users_eq "what the dry run actually called" \
        "dynamodb put-item" "$(printf '%s' "$json" | jq -r '.executed[]')" || return 1
    # The action recorded is the read that happened, not the creation that did not.
    users_eq "action recorded by the dry run" "user.read" "$(printf '%s' "$json" | jq -r .audit_action)" || return 1
    users_eq "action --apply would record" "user.create" "$(printf '%s' "$json" | jq -r .apply_audit_action)" || return 1
    # The convention that matters most (§11.3), proven against the fake rather than
    # inferred from the script's own report.
    users_eq "AWS calls made by the dry run" "dynamodb put-item" "$(users_calls "$mutlog")" || return 1
    rm -f "$mutlog"
}

t_users_apply_matches_dry_run() {
    local dry_log apply_log dry apply
    dry_log="$(mktemp)"
    apply_log="$(mktemp)"
    dry="$(users_run users-absent "$dry_log" add "$USERS_SUBJECT")" || return 1
    apply="$(users_run users-absent "$apply_log" add "$USERS_SUBJECT" --apply)" || return 1

    users_eq "plan identical between dry run and --apply" \
        "$(printf '%s' "$dry" | jq -r '.plan[]')" "$(printf '%s' "$apply" | jq -r '.plan[]')" || return 1
    # §11.5's central assertion: the calls --apply made are exactly the ones the dry run
    # named, and no others.
    users_eq "AWS calls made by --apply" \
        "$(printf '%s' "$dry" | jq -r '.plan[]' | LC_ALL=C sort)" "$(users_calls "$apply_log")" || return 1
    users_eq "action --apply recorded" \
        "$(printf '%s' "$dry" | jq -r .apply_audit_action)" "$(printf '%s' "$apply" | jq -r .audit_action)" || return 1
    users_eq "dry run still made no Cognito call" "dynamodb put-item" "$(users_calls "$dry_log")" || return 1
    rm -f "$dry_log" "$apply_log"
}

# I13 and §9.2 together: the record exists, it is tenant-scoped, it names the subject by
# digest, and the address appears nowhere in it.
t_users_audit_record_is_scoped_and_carries_no_email() {
    local mutlog put
    mutlog="$(mktemp)"
    users_run users-absent "$mutlog" add "$USERS_SUBJECT" --apply >/dev/null || return 1

    put="$(grep '^dynamodb put-item ' "$mutlog" || true)"
    if [ -z "$put" ]; then
        printf 'assertion failed: no audit record was written (I13)\n%s\n' "$(cat "$mutlog")"
        return 1
    fi
    users_eq "exactly one audit record per invocation" "1" "$(grep -c '^dynamodb put-item ' "$mutlog")" || return 1
    case "$put" in
        *t-vp*) ;;
        *)
            printf 'assertion failed: the audit item is not tenant-scoped (I11)\n%s\n' "$put"
            return 1
            ;;
    esac
    case "$put" in
        *"$USERS_DIGEST"*) ;;
        *)
            printf 'assertion failed: the audit item does not name the subject digest\n%s\n' "$put"
            return 1
            ;;
    esac
    case "$put" in
        *"$USERS_SUBJECT"* | *@*)
            printf 'assertion failed: an email address reached the audit record (§9.2)\n%s\n' "$put"
            return 1
            ;;
    esac
    # Write-once (§6.3): the put must carry the condition that refuses an overwrite, or
    # a ULID collision would destroy an existing record.
    case "$put" in
        *attribute_not_exists*) ;;
        *)
            printf 'assertion failed: the audit put is not conditional; an append-only log must never overwrite (§6.3)\n%s\n' "$put"
            return 1
            ;;
    esac
    rm -f "$mutlog"
}

# The idempotency §Phase 0 asks for, and the specific thing it must not do.
t_users_add_existing_sends_no_second_invite() {
    local mutlog json
    mutlog="$(mktemp)"
    json="$(users_run users-existing "$mutlog" add "$USERS_SUBJECT" --apply)" || return 1

    users_eq "outcome" "exists" "$(printf '%s' "$json" | jq -r .outcome)" || return 1
    users_eq "no Cognito call planned — nothing to do" "0" \
        "$(printf '%s' "$json" | jq -r '.cognito_calls | length')" || return 1
    users_eq "action recorded when nothing was created" "user.read" "$(printf '%s' "$json" | jq -r .audit_action)" || return 1
    users_eq "AWS calls made" "dynamodb put-item" "$(users_calls "$mutlog")" || return 1
    rm -f "$mutlog"
}

t_users_resend_is_explicit() {
    local mutlog json
    mutlog="$(mktemp)"
    json="$(users_run users-existing "$mutlog" add "$USERS_SUBJECT" --resend --apply)" || return 1

    users_eq "operation" "resend" "$(printf '%s' "$json" | jq -r .operation)" || return 1
    users_eq "action recorded" "user.invite_resend" "$(printf '%s' "$json" | jq -r .audit_action)" || return 1
    users_eq "AWS calls made" "cognito-idp admin-create-user
dynamodb put-item" "$(users_calls "$mutlog")" || return 1
    rm -f "$mutlog"
}

t_users_remove_dry_run_and_apply() {
    local dry_log apply_log dry apply human
    dry_log="$(mktemp)"
    apply_log="$(mktemp)"
    dry="$(users_run users-existing "$dry_log" remove "$USERS_SUBJECT")" || return 1
    apply="$(users_run users-existing "$apply_log" remove "$USERS_SUBJECT" --apply)" || return 1

    users_eq "the plan --apply would execute" \
        "dynamodb put-item
cognito-idp admin-delete-user" "$(printf '%s' "$dry" | jq -r '.plan[]')" || return 1
    users_eq "AWS calls made by the dry run" "dynamodb put-item" "$(users_calls "$dry_log")" || return 1
    users_eq "AWS calls made by --apply" \
        "$(printf '%s' "$dry" | jq -r '.plan[]' | LC_ALL=C sort)" "$(users_calls "$apply_log")" || return 1
    users_eq "action --apply recorded" "user.delete" "$(printf '%s' "$apply" | jq -r .audit_action)" || return 1

    # The operator's most likely wrong assumption, so the script must say it rather than
    # leave them to infer it (§9.3). Asserted on the human output, because that is where
    # the operator reads it.
    human="$(FAKE_AWS_FIXTURES="$USERS_FIXTURES/users-existing" FAKE_AWS_LOG=/dev/null \
        bash scripts/users.sh --instance dev --tenant t-vp remove "$USERS_SUBJECT" 2>&1)" || return 1
    case "$human" in
        *"does NOT erase tenant data"*) ;;
        *)
            printf 'assertion failed: remove does not say that tenant data survives (§9.3)\n%s\n' "$human"
            return 1
            ;;
    esac
    case "$human" in
        *erase-tenant.sh*) ;;
        *)
            printf 'assertion failed: remove does not name the operation that DOES erase (§9.3)\n%s\n' "$human"
            return 1
            ;;
    esac
    rm -f "$dry_log" "$apply_log"
}

t_users_remove_absent_is_a_noop() {
    local mutlog json
    mutlog="$(mktemp)"
    json="$(users_run users-absent "$mutlog" remove "$USERS_SUBJECT" --apply)" || return 1
    users_eq "outcome" "absent" "$(printf '%s' "$json" | jq -r .outcome)" || return 1
    users_eq "AWS calls made" "dynamodb put-item" "$(users_calls "$mutlog")" || return 1
    rm -f "$mutlog"
}

t_users_reset_applies() {
    local mutlog json
    mutlog="$(mktemp)"
    json="$(users_run users-existing "$mutlog" reset "$USERS_SUBJECT" --apply)" || return 1
    users_eq "action recorded" "user.password_reset" "$(printf '%s' "$json" | jq -r .audit_action)" || return 1
    users_eq "AWS calls made" "cognito-idp admin-reset-user-password
dynamodb put-item" "$(users_calls "$mutlog")" || return 1
    rm -f "$mutlog"
}

# refuses asserts a refusal that happens BEFORE anything is called, which is the only
# kind worth having: an exit code alone would pass for a script that refused after
# deleting.
users_refuses() { # users_refuses <expected-exit> <fixture> <needle> [args...]
    local want_code="$1" fixture="$2" needle="$3"
    shift 3
    local mutlog out code=0
    mutlog="$(mktemp)"
    : >"$mutlog"
    out="$(FAKE_AWS_FIXTURES="$USERS_FIXTURES/$fixture" FAKE_AWS_LOG="$mutlog" \
        bash scripts/users.sh --instance dev "$@" 2>&1)" || code=$?
    users_eq "exit code" "$want_code" "$code" || return 1
    case "$out" in
        *"$needle"*) ;;
        *)
            printf 'assertion failed: refusal does not mention %q\n%s\n' "$needle" "$out"
            return 1
            ;;
    esac
    users_eq "AWS calls made by a refused invocation" "" "$(users_calls "$mutlog")" || return 1
    rm -f "$mutlog"
}

users_test "users.sh dry run writes the audit record and nothing else (§11.3, I13)" \
    t_users_dry_run_writes_only_the_audit_record

users_test "users.sh --apply does precisely what its dry run described (§11.5)" \
    t_users_apply_matches_dry_run

users_test "users.sh audit record is tenant-scoped and carries no email (I11, I13, §9.2)" \
    t_users_audit_record_is_scoped_and_carries_no_email

users_test "users.sh add on an existing user sends no second invite (§Phase 0)" \
    t_users_add_existing_sends_no_second_invite

users_test "users.sh add --resend resends only when asked" \
    t_users_resend_is_explicit

users_test "users.sh remove: dry run, --apply, and the erasure warning (§9.3)" \
    t_users_remove_dry_run_and_apply

users_test "users.sh remove of an absent user is idempotent" \
    t_users_remove_absent_is_a_noop

users_test "users.sh reset --apply triggers the password reset" \
    t_users_reset_applies

# I11 via §11.3, and the reason this script is audited at all: an invocation that
# changes who can reach a tenant's data must name the tenant. Exit 2 comes from
# require_tenant — a usage error, not an operational failure.
users_test "users.sh refuses to add without --tenant (I11, §11.3)" \
    users_refuses 2 users-absent "--tenant is required" add "$USERS_SUBJECT" --apply

users_test "users.sh refuses to remove without --tenant (I11, §11.3)" \
    users_refuses 2 users-existing "--tenant is required" remove "$USERS_SUBJECT" --apply

users_test "users.sh refuses to list without --tenant (I11, §11.3)" \
    users_refuses 2 users-existing "--tenant is required" list

users_test "users.sh refuses --apply on list, which is read-only (§11.3)" \
    users_refuses 2 users-existing "read-only" --tenant t-vp list --apply

users_test "users.sh refuses to reset a user who does not exist" \
    users_refuses 1 users-absent "no such user" --tenant t-vp reset "$USERS_SUBJECT" --apply

# The FORCE_CHANGE_PASSWORD case: Cognito refuses the reset, and the operation the
# operator wants is a fresh invite. Refused here, with that instruction, rather than
# passed through as a service-side InvalidParameterException that says neither.
users_test "users.sh refuses to reset a user who has never signed in" \
    users_refuses 1 users-invited "never signed in" --tenant t-vp reset "$USERS_SUBJECT" --apply

users_test "users.sh refuses a subject that is not an email address" \
    users_refuses 1 users-absent "not an email address" --tenant t-vp add "not-an-address" --apply

t_users_list_masks_emails() {
    local mutlog masked revealed
    mutlog="$(mktemp)"
    masked="$(users_run users-existing "$mutlog" list)" || return 1
    revealed="$(users_run users-existing "$mutlog" list --reveal)" || return 1

    users_eq "masked local part" "s***e@example.com" "$(printf '%s' "$masked" | jq -r '.users[0].email')" || return 1
    users_eq "masked flag" "true" "$(printf '%s' "$masked" | jq -r .masked)" || return 1
    users_eq "--reveal prints the address" "$USERS_SUBJECT" "$(printf '%s' "$revealed" | jq -r '.users[0].email')" || return 1
    # The default must not leak the address anywhere in the document, not merely in the
    # field the mask was applied to.
    case "$masked" in
        *"$USERS_SUBJECT"*)
            printf 'assertion failed: the default listing contains a full address (§9.2)\n%s\n' "$masked"
            return 1
            ;;
    esac
    users_eq "list made no mutating AWS call beyond its audit record" \
        "dynamodb put-item" "$(users_calls "$mutlog")" || return 1
    rm -f "$mutlog"
}

users_test "users.sh list masks addresses unless --reveal (§9.2)" \
    t_users_list_masks_emails

# ---------------------------------------------------------------------------
# usage.sh and audit.sh — observability (§11.4, Phase 0)
# ---------------------------------------------------------------------------
#
# Both are read-only, so §11.5's --dry-run/--apply pairing does not apply — §11.3 says
# read-only scripts have no --apply and need none, and one of these tests asserts exactly
# that by checking --apply is rejected rather than quietly ignored.
#
# What the rest assert, since a list of names hides it:
#   - neither runs untenanted (I11), and neither invents a default instance
#   - a report for one tenant contains nothing belonging to another, checked through the
#     wrapper and not only in the Go tests (§Phase 0 wants this at the data layer)
#   - the exit codes that carry findings survive the whole path: a month over the §10.7
#     ceiling reaches the caller as 4, not as the 1 that `go run` would have flattened it to
#   - audit.sh's own audit record (I13) is written and then excluded from its own answer
#
# The records come from a JSON fixture through an in-memory repository, so these run in CI
# with no AWS credentials and cannot reach an account.

OBS_FIXTURE="$REPO_ROOT/scripts/test/fixtures/observability/records.json"
OBS_FIXTURE_OVER="$REPO_ROOT/scripts/test/fixtures/observability/over-ceiling.json"

# Built once here rather than per invocation: the wrappers build chintanctl themselves when
# CHINTANCTL is unset, and eight invocations rebuilding it is the slowest thing in this file.
# A build failure is left to surface inside the first test rather than aborting the harness.
if (cd "$REPO_ROOT/backend" && go build -o "$REPO_ROOT/build/chintanctl" ./cmd/chintanctl) 2>/dev/null; then
    export CHINTANCTL="$REPO_ROOT/build/chintanctl"
fi

# obs_exit asserts a SPECIFIC exit code, which run_test cannot: the whole point of the
# 3-and-4 codes is that a caller distinguishes a finding from a failure, and "non-zero"
# would pass for either.
obs_exit() {
    local want="$1" name="$2"
    shift 2
    local status=0
    "$@" >/dev/null 2>&1 || status=$?
    if [ "$status" = "$want" ]; then
        printf '%s  pass%s %s\n' "$C_GREEN" "$C_OFF" "$name" >&2
        PASS=$((PASS + 1))
    else
        printf '%s  FAIL%s %s (exit %s, want %s)\n' "$C_RED" "$C_OFF" "$name" "$status" "$want" >&2
        FAIL=$((FAIL + 1))
    fi
}

expect_fail "usage.sh refuses to run without --tenant (I11, §11.3)" \
    bash scripts/usage.sh

expect_fail "audit.sh refuses to run without --tenant (I11, §11.3)" \
    bash scripts/audit.sh

# An instance is required rather than defaulted: usage records and audit records live in the
# instance's own table, so a default would answer from the wrong environment (§6.3).
expect_fail "usage.sh refuses without --instance or --fixtures" \
    bash scripts/usage.sh --tenant t-alpha

expect_fail "audit.sh refuses without --instance or --fixtures" \
    bash scripts/audit.sh --tenant t-alpha

# §11.3: read-only scripts have no --apply. Asserted as a rejection, because a flag silently
# ignored teaches an operator that --apply is harmless here.
expect_fail "usage.sh has no --apply and says so (§11.3)" \
    bash scripts/usage.sh --tenant t-alpha --fixtures "$OBS_FIXTURE" --apply

expect_fail "audit.sh has no --apply and says so (§11.3)" \
    bash scripts/audit.sh --tenant t-alpha --fixtures "$OBS_FIXTURE" --apply

run_test "usage.sh reports from fixtures" \
    bash scripts/usage.sh --tenant t-alpha --fixtures "$OBS_FIXTURE" --since 2026-07 --until 2026-08

run_test "audit.sh queries from fixtures" \
    bash scripts/audit.sh --tenant t-alpha --fixtures "$OBS_FIXTURE"

# I11 through the wrapper. The other tenant's single record is $9.00 — past the §10.7
# ceiling — so a leak would change both the reported total and the budget verdict rather
# than hiding inside a sum.
t_usage_is_tenant_scoped() {
    local out
    out="$(bash scripts/usage.sh --tenant t-alpha --fixtures "$OBS_FIXTURE" \
        --since 2026-07 --until 2026-08 --json 2>/dev/null)" || return 1
    printf '%s' "$out" | jq -e '.tenant == "t-alpha"' >/dev/null || return 1
    printf '%s' "$out" | jq -e '.cost_micros == 114059' >/dev/null || return 1
    printf '%s' "$out" | jq -e '.budget == "within_target"' >/dev/null || return 1
    printf '%s' "$out" | jq -e '[.months[].rows[].cost_micros] | max < 9000000' >/dev/null || return 1
    # And the other tenant sees its own record and only its own.
    out="$(bash scripts/usage.sh --tenant t-beta --fixtures "$OBS_FIXTURE" \
        --since 2026-07 --until 2026-08 --json 2>/dev/null)" || true
    printf '%s' "$out" | jq -e '.cost_micros == 9000000' >/dev/null || return 1
}
run_test "usage.sh report is tenant-scoped (I11)" t_usage_is_tenant_scoped

t_audit_is_tenant_scoped() {
    local out
    out="$(bash scripts/audit.sh --tenant t-alpha --fixtures "$OBS_FIXTURE" --json 2>/dev/null)" || return 1
    printf '%s' "$out" | jq -e '[.records[].actor] | index("user:u-beta") == null' >/dev/null || return 1
    printf '%s' "$out" | jq -e '.count == 4' >/dev/null || return 1
}
run_test "audit.sh query is tenant-scoped (I11)" t_audit_is_tenant_scoped

# I13 and the recursion §11.3 creates for this script specifically: reading the audit log
# appends to the audit log. The record must exist and must not be the answer.
t_audit_records_itself_and_excludes_it() {
    local out
    out="$(bash scripts/audit.sh --tenant t-alpha --fixtures "$OBS_FIXTURE" --json 2>/dev/null)" || return 1
    printf '%s' "$out" | jq -e '.own_audit_record | length > 0' >/dev/null || return 1
    printf '%s' "$out" | jq -e '.own_record_excluded == true' >/dev/null || return 1
    printf '%s' "$out" | jq -e '[.records[].id] | index($own) == null' \
        --arg own "$(printf '%s' "$out" | jq -r '.own_audit_record')" >/dev/null || return 1
    # A previous invocation's record is a different matter: it stays, because reads of the
    # log are accesses worth seeing.
    printf '%s' "$out" | jq -e '[.records[].action] | index("usage.report") != null' >/dev/null || return 1
}
run_test "audit.sh records its own invocation and excludes only that record (I13)" \
    t_audit_records_itself_and_excludes_it

# §10.7: a month over $5 "is a design error, not a budget overrun". Exit 4 is how that
# reaches a caller — and it is the test that catches a wrapper reverting to `go run`, which
# collapses every non-zero code to 1.
obs_exit 4 "usage.sh exits 4 for a month over the §10.7 ceiling" \
    bash scripts/usage.sh --tenant t-alpha --fixtures "$OBS_FIXTURE_OVER" --month 2026-07

# §Phase 0's acceptance criterion, in the failing direction: a billed figure twice the
# metered one is not within 5%, and exit 3 says so without claiming the tool broke.
obs_exit 3 "usage.sh exits 3 when metered and billed disagree beyond tolerance" \
    bash scripts/usage.sh --tenant t-alpha --fixtures "$OBS_FIXTURE" --month 2026-07 \
    --actual groq_whisper_turbo=0.001466

obs_exit 0 "usage.sh exits 0 when the billed figures reconcile" \
    bash scripts/usage.sh --tenant t-alpha --fixtures "$OBS_FIXTURE" --month 2026-07 \
    --actual groq_whisper_turbo=0.000740 --actual-total 0.113397

# A malformed window is an invocation error (2), not an empty report: "the log/report is
# empty" is the wrong answer to report from a typo.
obs_exit 2 "usage.sh refuses a malformed month rather than reporting zero spend" \
    bash scripts/usage.sh --tenant t-alpha --fixtures "$OBS_FIXTURE" --month 2026-13

obs_exit 2 "audit.sh refuses an impossible date bound rather than an empty log" \
    bash scripts/audit.sh --tenant t-alpha --fixtures "$OBS_FIXTURE" --since 2026-31-08

# ---------------------------------------------------------------------------
# backup.sh / restore.sh — the portable snapshot (§11.4, Phase 0)
# ---------------------------------------------------------------------------
#
# These are the first DATA scripts that write, so they carry the two §11.5
# requirements the infrastructure tests cannot: --tenant refusal (I11) and a dry run
# that describes precisely what --apply then does.
#
# The equivalence assertion is made on the --json plan rather than on prose. The plan
# is one structure, computed once, printed identically in both modes and executed
# entry by entry under --apply — so `jq -S '{plan, summary}'` from a dry run and from
# an --apply must be byte-identical, and the `applied` counts must equal the plan's
# write counts. That second half matters as much as the first: a plan that matches but
# an --apply that then does something else is the same lie in a different place.
#
# Both runs are given a FRESH COPY of the store fixture, because every invocation
# writes its own audit record (I13) and a second run against the same tree would see
# the first run's record. That is not the test working around the implementation — it
# is what makes the comparison a comparison of the same starting corpus.
#
# The fixture store holds TWO tenants. Every scoping assertion below depends on that:
# a backup that copied the whole table would pass a single-tenant fixture.

SNAP_FIXTURE="$REPO_ROOT/scripts/test/fixtures/backup/store"

# snap_test is run_test with the output kept on failure — a plan diff is unreadable
# without both plans in front of you.
snap_test() {
    local name="$1"
    shift
    local out
    if out="$("$@" 2>&1)"; then
        printf '%s  pass%s %s\n' "$C_GREEN" "$C_OFF" "$name" >&2
        PASS=$((PASS + 1))
    else
        printf '%s  FAIL%s %s\n' "$C_RED" "$C_OFF" "$name" >&2
        printf '%s\n' "$out" | sed 's/^/        /' >&2
        FAIL=$((FAIL + 1))
    fi
}

# snap_exit asserts an exact exit code. The distinction is load-bearing here: 3 is
# "refused, nothing happened" and 1 is "failed, possibly halfway", and an operator
# acts differently on each.
snap_exit() {
    local want="$1" name="$2"
    shift 2
    # `|| status=$?` rather than a bare call: common.sh sets -e, and a bare failing
    # command in a helper whose whole job is to expect a failure would end the run.
    local out="" status=0
    out="$("$@" 2>&1)" || status=$?
    if [ "$status" = "$want" ]; then
        printf '%s  pass%s %s\n' "$C_GREEN" "$C_OFF" "$name" >&2
        PASS=$((PASS + 1))
    else
        printf '%s  FAIL%s %s (exit %s, want %s)\n' "$C_RED" "$C_OFF" "$name" "$status" "$want" >&2
        printf '%s\n' "$out" | sed 's/^/        /' >&2
        FAIL=$((FAIL + 1))
    fi
}

# I11 via §11.3, in the direction that matters: the failure this prevents is a
# corpus-wide copy running because a flag was forgotten.
expect_fail "backup.sh refuses to run without --tenant (I11, §11.3)" \
    bash scripts/backup.sh --store "$SNAP_FIXTURE" --dest /nonexistent/snap
expect_fail "restore.sh refuses to run without --tenant (I11, §11.3)" \
    bash scripts/restore.sh --store "$SNAP_FIXTURE" --from "$SNAP_FIXTURE"

# The §11.5 assertion: "dry-run output is asserted to describe precisely what --apply
# then does".
t_backup_dryrun_equals_apply() {
    local w
    w="$(mktemp -d)"
    cp -r "$SNAP_FIXTURE" "$w/a"
    cp -r "$SNAP_FIXTURE" "$w/b"

    bash scripts/backup.sh --tenant alice --store "$w/a" --dest "$w/snap-a" --json >"$w/dry.json" || return 1
    bash scripts/backup.sh --tenant alice --store "$w/b" --dest "$w/snap-b" --json --apply \
        --accept-plaintext-copy --yes >"$w/apply.json" || return 1

    jq -S '{plan, summary}' "$w/dry.json" >"$w/dry.plan" || return 1
    jq -S '{plan, summary}' "$w/apply.json" >"$w/apply.plan" || return 1
    diff -u "$w/dry.plan" "$w/apply.plan" || return 1

    # The other half: --apply executed exactly the plan's write entries.
    jq -e '.applied.items_write == .summary.items_write
        and .applied.objects_write == .summary.objects_write
        and .applied.bytes == .summary.bytes' "$w/apply.json" >/dev/null || return 1

    # The dry run wrote no snapshot at all.
    [ ! -e "$w/snap-a" ] || return 1
    # ...and the --apply did.
    [ -f "$w/snap-b/manifest.json" ] || return 1

    # I13: both invocations wrote exactly one audit record into their own store, and a
    # dry run's record is the only write it makes to the corpus.
    [ "$(jq '[.[] | select(.sk | startswith("AUDIT"))] | length' "$w/a/items.json")" = "1" ] || return 1
    [ "$(jq '[.[] | select(.sk | startswith("AUDIT"))] | length' "$w/b/items.json")" = "1" ] || return 1

    rm -rf "$w"
}
snap_test "backup.sh: the dry-run plan is exactly what --apply executes (§11.5)" \
    t_backup_dryrun_equals_apply

# The fixture store holds a second tenant. A backup that read the table rather than
# the partition would copy it, and this is the only test that would notice (I11).
t_backup_copies_only_the_named_tenant() {
    local w
    w="$(mktemp -d)"
    cp -r "$SNAP_FIXTURE" "$w/store"
    bash scripts/backup.sh --tenant alice --store "$w/store" --dest "$w/snap" --apply \
        --accept-plaintext-copy --yes --json >"$w/out.json" || return 1
    # The other tenant's id and its capture appear nowhere in the archived records, in
    # no archived object path, and as no file on disk. Asserted against the records and
    # the object paths rather than by grepping the whole archive: the manifest is full
    # of hex digests, and 'c9' occurs in hex by chance — an assertion that fails at
    # random is worse than none.
    ! grep -q 'bob' "$w/snap/items.jsonl" || return 1
    ! grep -q 'c9' "$w/snap/items.jsonl" || return 1
    [ "$(jq -r '[.objects[].path] | map(select(test("bob|c9"))) | length' "$w/snap/manifest.json")" = "0" ] || return 1
    [ "$(find "$w/snap/objects" -type f \( -path '*bob*' -o -path '*c9*' \) | wc -l)" = "0" ] || return 1
    [ "$(jq '.summary.items' "$w/out.json")" = "4" ] || return 1
    [ "$(jq '.summary.objects' "$w/out.json")" = "4" ] || return 1
    rm -rf "$w"
}
snap_test "backup.sh: copies only the named tenant's partition and prefix (I11)" \
    t_backup_copies_only_the_named_tenant

# §9.2: the snapshot is plaintext user content outside the encrypted store, and I8's
# at-rest protection does not follow it. The acknowledgement is the operator's,
# recorded rather than assumed.
snap_exit 3 "backup.sh: --apply refuses without --accept-plaintext-copy (§9.2)" \
    bash scripts/backup.sh --tenant alice --store "$SNAP_FIXTURE" --dest /tmp/snap-never-written --apply --yes

t_backup_refuses_nonempty_destination() {
    local w
    w="$(mktemp -d)"
    cp -r "$SNAP_FIXTURE" "$w/store"
    mkdir -p "$w/snap"
    : >"$w/snap/something"
    # Exit 3: a point-in-time copy cannot be merged into an existing one, so this is a
    # refusal rather than a failure.
    local status=0
    bash scripts/backup.sh --tenant alice --store "$w/store" --dest "$w/snap" --apply \
        --accept-plaintext-copy --yes >/dev/null 2>&1 || status=$?
    [ "$status" = "3" ] || return 1
    [ ! -f "$w/snap/manifest.json" ] || return 1
    rm -rf "$w"
}
snap_test "backup.sh: refuses a destination that already holds something" \
    t_backup_refuses_nonempty_destination

# restore.sh's own §11.5 pair, plus the proof that a dry run writes nothing but its
# audit record.
t_restore_dryrun_equals_apply() {
    local w
    w="$(mktemp -d)"
    cp -r "$SNAP_FIXTURE" "$w/store"
    bash scripts/backup.sh --tenant alice --store "$w/store" --dest "$w/snap" --apply \
        --accept-plaintext-copy --yes >/dev/null || return 1

    mkdir -p "$w/dry" "$w/apply"
    bash scripts/restore.sh --tenant alice --store "$w/dry" --from "$w/snap" --json >"$w/dry.json" || return 1
    bash scripts/restore.sh --tenant alice --store "$w/apply" --from "$w/snap" --json --apply --yes >"$w/apply.json" || return 1

    jq -S '{plan, summary}' "$w/dry.json" >"$w/dry.plan" || return 1
    jq -S '{plan, summary}' "$w/apply.json" >"$w/apply.plan" || return 1
    diff -u "$w/dry.plan" "$w/apply.plan" || return 1

    jq -e '.applied.items_write == .summary.items_write
        and .applied.objects_write == .summary.objects_write' "$w/apply.json" >/dev/null || return 1

    # The dry run wrote no object and no record other than its own audit entry (I13).
    [ "$(find "$w/dry/objects" -type f 2>/dev/null | wc -l)" = "0" ] || return 1
    [ "$(jq 'length' "$w/dry/items.json")" = "1" ] || return 1
    [ "$(jq '[.[] | select(.sk | startswith("AUDIT"))] | length' "$w/dry/items.json")" = "1" ] || return 1

    # The --apply wrote the corpus.
    [ "$(find "$w/apply/objects" -type f | wc -l)" = "4" ] || return 1
    rm -rf "$w"
}
snap_test "restore.sh: the dry-run plan is exactly what --apply executes, and a dry run writes nothing (§11.5)" \
    t_restore_dryrun_equals_apply

# The refusal this pair of scripts exists to get right. A raw transcript already
# stored under the same key with DIFFERENT bytes is never replaced — I1 permits only
# the erasure path to remove L0 (§9.3) and nothing to overwrite it — and no flag
# changes that, including --on-conflict skip.
t_restore_refuses_differing_l0() {
    local w l0 before
    w="$(mktemp -d)"
    cp -r "$SNAP_FIXTURE" "$w/store"
    bash scripts/backup.sh --tenant alice --store "$w/store" --dest "$w/snap" --apply \
        --accept-plaintext-copy --yes >/dev/null || return 1
    mkdir -p "$w/target"
    bash scripts/restore.sh --tenant alice --store "$w/target" --from "$w/snap" --apply --yes >/dev/null || return 1

    # Make the stored raw transcript differ from the archived one. The path is found
    # rather than constructed: this test must not assemble a key either (I11).
    l0="$(find "$w/target/objects" -path '*L0*' -type f | head -1)"
    [ -n "$l0" ] || return 1
    printf 'a DIFFERENT raw transcript\n' >"$l0"
    before="$(cat "$l0")"

    local status=0
    bash scripts/restore.sh --tenant alice --store "$w/target" --from "$w/snap" --apply --yes \
        --json >"$w/refuse.json" 2>/dev/null || status=$?
    [ "$status" = "3" ] || return 1
    jq -e '.refused == true
        and ([.plan[] | select(.action == "refuse-l0")] | length == 1)' "$w/refuse.json" >/dev/null || return 1
    # I1, asserted on the bytes rather than on the message: the stored raw transcript
    # is untouched.
    [ "$(cat "$l0")" = "$before" ] || return 1

    # --on-conflict skip is for the mutable layers; it must not soften this one.
    status=0
    bash scripts/restore.sh --tenant alice --store "$w/target" --from "$w/snap" --apply --yes \
        --on-conflict skip >/dev/null 2>&1 || status=$?
    [ "$status" = "3" ] || return 1
    [ "$(cat "$l0")" = "$before" ] || return 1
    rm -rf "$w"
}
snap_test "restore.sh: refuses a differing raw transcript and leaves it untouched, in every conflict mode (I1)" \
    t_restore_refuses_differing_l0

# Idempotent wherever the operation permits (§11.3): a restore interrupted halfway
# must be resumable, so an identical key is a skip rather than a refusal.
t_restore_is_idempotent() {
    local w
    w="$(mktemp -d)"
    cp -r "$SNAP_FIXTURE" "$w/store"
    bash scripts/backup.sh --tenant alice --store "$w/store" --dest "$w/snap" --apply \
        --accept-plaintext-copy --yes >/dev/null || return 1
    mkdir -p "$w/target"
    bash scripts/restore.sh --tenant alice --store "$w/target" --from "$w/snap" --apply --yes >/dev/null || return 1
    bash scripts/restore.sh --tenant alice --store "$w/target" --from "$w/snap" --apply --yes --json >"$w/second.json" || return 1
    jq -e '.refused == false
        and .summary.items_write == 0 and .summary.objects_write == 0
        and ([.plan[] | select(.action == "skip-identical")] | length == 8)' "$w/second.json" >/dev/null || return 1
    rm -rf "$w"
}
snap_test "restore.sh: a second --apply is a no-op, so an interrupted restore resumes (§11.3)" \
    t_restore_is_idempotent

# "Restore into a NAMED tenant" means the target may differ from the source, so every
# key has to be rebuilt for it (I11, which binds admin scripts explicitly).
t_restore_rekeys_into_the_named_tenant() {
    local w
    w="$(mktemp -d)"
    cp -r "$SNAP_FIXTURE" "$w/store"
    bash scripts/backup.sh --tenant alice --store "$w/store" --dest "$w/snap" --apply \
        --accept-plaintext-copy --yes >/dev/null || return 1
    mkdir -p "$w/carol"

    # Without the flag it refuses: the fixture's capture names its owner, which in the
    # personal phase equals the source tenant id, and that is a reference this tool
    # will not silently write into another tenant.
    local status=0
    bash scripts/restore.sh --tenant carol --store "$w/carol" --from "$w/snap" \
        --json >"$w/refuse.json" 2>/dev/null || status=$?
    [ "$status" = "3" ] || return 1
    jq -e '[.plan[] | select(.action == "refuse-ref")] | length > 0' "$w/refuse.json" >/dev/null || return 1

    bash scripts/restore.sh --tenant carol --store "$w/carol" --from "$w/snap" --apply --yes \
        --allow-source-tenant-refs >/dev/null || return 1

    # Every partition key belongs to the target, and no stored reference still points
    # at the source tenant's records or objects. The pk assertions are made on the
    # UNIQUED set — one distinct partition key, and it is the target's — because
    # "length == 1" over the raw list would only ever hold for a one-record store and
    # would therefore fail on any real fixture. The target id is matched by substring
    # rather than by writing the partition-key prefix out: a key literal here would
    # fail check-tenant-keys.sh (I11), which is right — a test that hardcodes the
    # layout is a second copy of it.
    jq -e '([.[] | .pk] | unique) as $pks
        | ($pks | length == 1)
        and ($pks | map(select(test("carol"))) | length == 1)
        and ([.[] | .attrs.s3_prefix // empty] | map(select(test("alice"))) | length == 0)
        and ([.[] | .attrs.audio_key // empty] | map(select(test("alice"))) | length == 0)
        and ([.[] | .gsi1pk // empty] | map(select(test("alice"))) | length == 0)' \
        "$w/carol/items.json" >/dev/null || return 1
    # The objects landed under the target tenant's prefix, not the source's.
    [ "$(find "$w/carol/objects" -type f -path '*carol*' | wc -l)" = "4" ] || return 1
    [ "$(find "$w/carol/objects" -type f -path '*alice*' | wc -l)" = "0" ] || return 1
    rm -rf "$w"
}
snap_test "restore.sh: re-keys every record and object into the named tenant (I11)" \
    t_restore_rekeys_into_the_named_tenant

# A damaged archive that restores silently is worse than one that refuses: the records
# it lost are the ones nobody notices are missing.
t_restore_refuses_damaged_archive() {
    local w
    w="$(mktemp -d)"
    cp -r "$SNAP_FIXTURE" "$w/store"
    bash scripts/backup.sh --tenant alice --store "$w/store" --dest "$w/snap" --apply \
        --accept-plaintext-copy --yes >/dev/null || return 1
    printf '{"sk":"tampered"}\n' >>"$w/snap/items.jsonl"
    mkdir -p "$w/target"
    local status=0
    bash scripts/restore.sh --tenant alice --store "$w/target" --from "$w/snap" --apply --yes \
        >/dev/null 2>&1 || status=$?
    [ "$status" = "3" ] || return 1
    [ "$(find "$w/target/objects" -type f 2>/dev/null | wc -l)" = "0" ] || return 1
    rm -rf "$w"
}
snap_test "restore.sh: refuses an archive whose hash does not match its manifest" \
    t_restore_refuses_damaged_archive

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
