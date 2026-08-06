#!/usr/bin/env bash
#
# Cognito user management — add | remove | list | reset (§11.4, Phase 0).
#
# **One implementation.** §Phase 0 says so explicitly: "CLI script:
# scripts/users.sh add <email> → creates Cognito user, sends invite. One
# implementation (§11.2) — do not also create an add-user.sh." Passbook's admin.sh and
# add-data.sh drifted ~300 duplicated lines apart; there is one front-end here.
#
# The pool is admin-create-only with self-signup disabled (§9), so this script is the
# only way a user comes to exist. No password material is stored or handled by
# application code — Cognito mints the temporary password and mails the invite, which
# is the whole reason Cognito is used rather than passbook's Argon2id pattern (§4).
#
# # Why this script is audited when the other bash scripts are not
#
# §11.3 places it across the line deliberately: "users.sh and telegram-link.sh sit
# across the line: they take --tenant and write audit records, because they change WHO
# CAN REACH TENANT DATA." It touches no stored content, and that is not the test —
# adding a user is granting reach into a tenant's corpus, and a grant with no record of
# who made it is the gap §2A.1 calls unrepairable.
#
# The record is rendered by `chintanctl users audit-item`, not built here: I11 gives
# backend/internal/keys a monopoly on key construction (enforced by
# scripts/checks/check-tenant-keys.sh, which fails the build on a key prefix literal in
# a shell script), and §11.2 keeps the record's shape in tested application code. This
# script performs the put, which is what makes the write observable to the fake-AWS
# harness and lets §11.5's --apply test assert it happened.
#
# # An email address is PII, and this script's only argument is one
#
# §9.2 keeps PII out of logs. That cannot mean "never handle an address" — the address
# IS the username in a pool with UsernameAttributes: [email]. So the line is drawn at
# what persists:
#
#   - The audit record carries a DIGEST, never the address (chintanctl computes it).
#     Reproduce it for an audit.sh query with:
#         printf '%s' someone@example.com | sha256sum | cut -c1-16
#   - Diagnostics on stderr — the stream a CI job captures into a log — name the
#     digest, never the address.
#   - `list` masks the local part by default. --reveal prints addresses in full, and
#     that output is PII: do not paste it into an issue, a chat, or a CI log.
#   - Nothing is written to $GITHUB_STEP_SUMMARY. deploy.sh writes one; a step summary
#     is a durable artifact of the run and no address belongs in it.
#
# The address does reach this script's own argv, and so the shell history and process
# list of the machine it runs on. That is inherent to a CLI whose argument is an address
# and is not something the script can fix.
#
# # Removing a user does NOT erase their data
#
# `remove` deletes the Cognito identity. The tenant's captures, transcripts, audio, and
# items remain stored, encrypted, and billable. Erasure is a separate, separately
# permissioned operation (§9.3, erase-tenant.sh) precisely because I1 makes L0
# immutable to application code — the erasure handler is its sole exception. `remove`
# says this on every invocation rather than leaving the operator to assume either way.
#
# Prerequisites:
#   - the instance stack is deployed: the pool id and table name come from its
#     CloudFormation outputs, so nothing here is hardcoded and no id is pasted by hand
#   - AWS credentials with cognito-idp admin rights on the pool
#   - go, jq, yq
#
# Spends no provider money: Cognito's default email delivery is free at this volume, so
# there is no cost estimate to print (§11.3), and no metering event is emitted (I12).
# Cognito's cost is per monthly active user — shared-infrastructure spend, which §6.4
# assigns to the deployment tag set rather than to per-tenant Usage records, "because AWS
# cost allocation tags cannot attribute shared-resource spend across tenants". A Usage
# record here would be a per-tenant figure invented from a bill that is not per-tenant.
#
# Usage:
#   scripts/users.sh list   --instance dev --tenant t-vp [--json] [--reveal]
#   scripts/users.sh add    --instance dev --tenant t-vp someone@example.com [--apply]
#   scripts/users.sh add    --instance dev --tenant t-vp someone@example.com --resend --apply
#   scripts/users.sh reset  --instance dev --tenant t-vp someone@example.com --apply
#   scripts/users.sh remove --instance dev --tenant t-vp someone@example.com --apply
#
# --dry-run is the default for add, remove, and reset; --apply executes (§11.3).
# `list` is read-only and has no --apply.
#
# Example:
#   scripts/users.sh add --instance dev --tenant t-vp someone@example.com --apply

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

# usage_die separates "you invoked this wrongly" from "the thing you asked for failed"
# (§11.3, meaningful exit codes). 2 for the first, 1 for the second — the distinction a
# CI step or a wrapper needs, and the one `die` alone cannot make. require_tenant in
# scripts/lib/common.sh already exits 2, so this matches it.
usage_die() {
    err "$*"
    err "See --help."
    exit 2
}

SUBCOMMAND=""
EMAIL=""
TENANT=""
INSTANCE=""
REGION=""
APPLY=0
AS_JSON=0
RESEND=0
REVEAL=0

while [ $# -gt 0 ]; do
    case "$1" in
        --tenant)
            TENANT="${2:-}"
            shift
            ;;
        --instance)
            INSTANCE="${2:-}"
            shift
            ;;
        --region)
            REGION="${2:-}"
            shift
            ;;
        --apply) APPLY=1 ;;
        --json) AS_JSON=1 ;;
        --resend) RESEND=1 ;;
        --reveal) REVEAL=1 ;;
        -h | --help)
            sed -n '2,82p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        -*) usage_die "unknown flag '$1'" ;;
        *)
            if [ -z "$SUBCOMMAND" ]; then
                SUBCOMMAND="$1"
            elif [ -z "$EMAIL" ]; then
                EMAIL="$1"
            else
                usage_die "unexpected argument '$1'"
            fi
            ;;
    esac
    shift
done
cd "$REPO_ROOT" || exit 1

case "$SUBCOMMAND" in
    add | remove | list | reset) ;;
    "") usage_die "a subcommand is required: add | remove | list | reset" ;;
    *) usage_die "unknown subcommand '$SUBCOMMAND': expected add | remove | list | reset" ;;
esac

# I11 via §11.3: no data operation runs untenanted. Checked before anything is read,
# because the failure this prevents is an invocation that changes who can reach a
# tenant's data leaving a record that cannot name the tenant.
require_tenant "$TENANT"

if [ -z "$INSTANCE" ]; then
    err "--instance is required. Known instances:"
    for f in config/instances/*.yaml; do
        [ -f "$f" ] && dim "  $(basename "$f" .yaml)"
    done
    exit 2
fi
CONFIG="config/instances/${INSTANCE}.yaml"
[ -f "$CONFIG" ] || usage_die "no config for instance '$INSTANCE' at $CONFIG"

if [ "$SUBCOMMAND" = "list" ]; then
    [ -z "$EMAIL" ] || usage_die "list takes no email argument; it lists the whole pool"
    [ "$APPLY" = "0" ] || usage_die "list is read-only and has no --apply (§11.3)"
else
    [ -n "$EMAIL" ] || usage_die "$SUBCOMMAND requires an email address"
    [ "$REVEAL" = "0" ] || usage_die "--reveal applies to list, not $SUBCOMMAND"
fi
if [ "$RESEND" = "1" ] && [ "$SUBCOMMAND" != "add" ]; then
    usage_die "--resend applies to add, not $SUBCOMMAND"
fi

# Region from config, not from the ambient environment — same reasoning as deploy.sh: a
# developer machine's default region is whatever they last worked on, and resolving the
# stack in the wrong region would report "not deployed" for a pool that exists.
if [ -z "$REGION" ]; then
    REGION="$(yq -r '.region' "$CONFIG")"
fi
[ -n "$REGION" ] && [ "$REGION" != "null" ] || die "no region in $CONFIG"
export AWS_REGION="$REGION" AWS_PAGER=""

# SYSTEM_ID, not the literal: §7.3 freezes the resource prefix and
# scripts/checks/check-resource-prefix.sh asserts the bash constant and
# backend/internal/systemid agree, so taking it from there is what keeps a rename from
# having to find every string.
STACK="${SYSTEM_ID}-${INSTANCE}"

# ---------------------------------------------------------------------------
# Stack outputs
# ---------------------------------------------------------------------------
#
# The pool id and the table name come from the stack; neither is hardcoded and neither
# is asked for. An operator who has to paste a pool id is an operator who will one day
# paste the prod one while meaning dev.
#
# Read as one JSON document and extracted with jq rather than with --query/--output
# text. Server-side, one describe-stacks call answers both questions; locally, the fake
# CLI in scripts/test/fake-aws ignores --query, so a script depending on it would be
# untestable (§11.5).
STACK_JSON="$(aws_cli cloudformation describe-stacks --stack-name "$STACK" --output json 2>/dev/null)" ||
    die "cannot describe $STACK in $REGION. Is the instance deployed?"

stack_output() {
    printf '%s' "$STACK_JSON" |
        jq -r --arg k "$1" 'first(.Stacks[0].Outputs[]? | select(.OutputKey == $k) | .OutputValue) // ""'
}

USER_POOL_ID="$(stack_output UserPoolId)"
TABLE="$(stack_output TableName)"
[ -n "$USER_POOL_ID" ] ||
    die "$STACK has no UserPoolId output. Deploy the instance first (scripts/deploy.sh)."
[ -n "$TABLE" ] ||
    die "$STACK has no TableName output; the audit record (I13) would have nowhere to go, so this refuses rather than acting unrecorded."

info "instance $INSTANCE ($REGION), pool $USER_POOL_ID"

# ---------------------------------------------------------------------------
# Probe — read the pool before deciding anything
# ---------------------------------------------------------------------------
#
# Read-only, and it runs in BOTH modes. That symmetry is the point: one code path
# computes the plan and --apply executes exactly it, so "dry-run output describes
# precisely what --apply then does" (§11.5) is structural rather than two branches that
# have to be kept agreeing.
#
# It also runs before the audit record is written, which is a deliberate departure from
# internal/audit's "record first" rule. That rule exists so no access to content goes
# unrecorded; here the read is what determines which operation this invocation actually
# performs — creating a user versus finding one already there — and recording an action
# before knowing it would put "user.create" in the seven-year log for an invocation that
# created nothing. Every MUTATION is still preceded by its record, which is the ordering
# §9.3 requires.
EXISTS=0
USER_STATUS=""
USER_SUB=""
if [ "$SUBCOMMAND" = "list" ]; then
    POOL_JSON="$(aws_cli cognito-idp list-users --user-pool-id "$USER_POOL_ID" --output json)" ||
        die "cannot list users in $USER_POOL_ID"
else
    # A filter, not admin-get-user: a missing user is an empty result rather than an
    # exception, so "does not exist" stays distinguishable from "the call failed". Under
    # set -e a failed read aborts before any mutation, which is the direction to fail.
    # The filter expression is double-quoted and the address cannot contain a quote —
    # chintanctl's emailRe refuses one, precisely because this expression exists.
    POOL_JSON="$(aws_cli cognito-idp list-users --user-pool-id "$USER_POOL_ID" \
        --filter "email = \"${EMAIL}\"" --limit 1 --output json)" ||
        die "cannot read $USER_POOL_ID"
    if [ "$(printf '%s' "$POOL_JSON" | jq -r '(.Users // []) | length')" != "0" ]; then
        EXISTS=1
        USER_STATUS="$(printf '%s' "$POOL_JSON" | jq -r '(.Users // [])[0].UserStatus // ""')"
        USER_SUB="$(printf '%s' "$POOL_JSON" |
            jq -r '[(.Users // [])[0].Attributes[]? | select(.Name == "sub") | .Value][0] // ""')"
    fi
fi

# ---------------------------------------------------------------------------
# Plan
# ---------------------------------------------------------------------------
#
# PLAN_CALLS holds the mutating AWS calls this operation makes, as "service operation".
# It is what --dry-run prints, what --json reports, and what --apply executes — one
# list, so a dry-run that lies is not expressible. OPERATION is the effective operation
# after the probe; chintanctl maps it to the action recorded.
PLAN_CALLS=()
PLAN_NOTE=""
OUTCOME=""
OPERATION="$SUBCOMMAND"

case "$SUBCOMMAND" in
    add)
        if [ "$EXISTS" = "1" ] && [ "$RESEND" = "0" ]; then
            # Idempotent, and deliberately NOT a silent second invite. §Phase 0 wants
            # `add` re-runnable; an invite resent without being asked for mints a new
            # temporary password and invalidates the one the user may be part-way
            # through using, which reads to them as the account being broken.
            OUTCOME="exists"
            PLAN_NOTE="the user already exists (status ${USER_STATUS:-unknown}); no invite is resent. Re-run with --resend --apply to send a fresh one."
        elif [ "$EXISTS" = "1" ]; then
            OPERATION="resend"
            OUTCOME="resend"
            PLAN_CALLS=("cognito-idp admin-create-user")
            PLAN_NOTE="resends the invite with a NEW temporary password; the previous one stops working."
        else
            OUTCOME="create"
            PLAN_CALLS=("cognito-idp admin-create-user")
            PLAN_NOTE="creates the user and mails an invite carrying a temporary password."
        fi
        ;;
    remove)
        if [ "$EXISTS" = "0" ]; then
            OUTCOME="absent"
            PLAN_NOTE="no such user in this pool; nothing to remove."
        else
            OUTCOME="delete"
            PLAN_CALLS=("cognito-idp admin-delete-user")
            PLAN_NOTE="deletes the Cognito identity. THE TENANT'S DATA IS NOT ERASED (§9.3)."
        fi
        ;;
    reset)
        if [ "$EXISTS" = "0" ]; then
            # Refused rather than reported as a no-op: unlike remove, there is no state
            # in which "reset a user who does not exist" is the operator's intent
            # already satisfied. The likely cause is a mistyped address, and reporting
            # success would send them looking for a mail that was never sent.
            die "no such user in $USER_POOL_ID. Nothing was changed — check the address with: scripts/users.sh list --instance $INSTANCE --tenant $TENANT"
        fi
        if [ "$USER_STATUS" = "FORCE_CHANGE_PASSWORD" ]; then
            # Cognito refuses admin-reset-user-password for a user who has never signed
            # in, and what the operator wants in that state is a fresh invite. Named
            # here rather than left to a service-side InvalidParameterException, which
            # does not say what to do instead.
            die "this user has never signed in (status FORCE_CHANGE_PASSWORD), so there is no password to reset. Send a fresh invite instead: scripts/users.sh add --instance $INSTANCE --tenant $TENANT <email> --resend --apply"
        fi
        OUTCOME="reset"
        PLAN_CALLS=("cognito-idp admin-reset-user-password")
        PLAN_NOTE="the current password stops working immediately and a reset code is mailed."
        ;;
    list)
        OUTCOME="list"
        ;;
esac

# The mode the audit record is written in. `execute` only when this invocation both was
# asked to apply and has something to execute — with nothing to do, the invocation only
# read the pool, and `user.read` is what actually happened.
AUDIT_MODE="plan"
if [ "$APPLY" = "1" ] && [ "${#PLAN_CALLS[@]}" -gt 0 ]; then
    AUDIT_MODE="execute"
fi

# ---------------------------------------------------------------------------
# Audit record (I13) — written before any mutation
# ---------------------------------------------------------------------------
#
# Rendered in Go, put from here; see the header for why the split exists.
#
# A dry run writes one too. §11.3 requires that "every invocation of a data script
# writes an audit record", and a dry run genuinely read who can reach this tenant's
# data — that read is the access being recorded. It is the ONLY write a dry run makes,
# and the harness asserts exactly that.
audit_args=(users audit-item
    --tenant "$TENANT"
    --operation "$OPERATION"
    --mode "$AUDIT_MODE"
    --config "../$CONFIG")
if [ "$SUBCOMMAND" != "list" ]; then
    audit_args+=(--subject "$EMAIL")
fi

# Exit 1, not the binary's own code: `go run` collapses a non-zero child status to 1, so
# a subject rejected by chintanctl (a usage error, exit 2 from the binary) arrives here
# indistinguishable from a config that failed to load. The binary's message is already on
# stderr and says which it was; propagating the wrong code would be worse than a single
# honest one.
AUDIT_JSON="$(cd backend && go run ./cmd/chintanctl "${audit_args[@]}")" ||
    die "could not render the audit record; nothing was changed. An unrecorded change to who can reach tenant data is not permitted (I13)."

AUDIT_ACTION="$(printf '%s' "$AUDIT_JSON" | jq -r .action)"
APPLY_ACTION="$(printf '%s' "$AUDIT_JSON" | jq -r .apply_action)"
SUBJECT_DIGEST="$(printf '%s' "$AUDIT_JSON" | jq -r '.subject_digest // ""')"

# Diagnostics name the digest, never the address (§9.2).
if [ -n "$SUBJECT_DIGEST" ]; then
    dim "  subject $SUBJECT_DIGEST (sha256 prefix of the address; the address is not logged)"
fi

# PutOnce semantics, mirroring repository.Dynamo.PutOnce exactly: the condition is
# attribute_not_exists on the partition key, which DynamoDB evaluates against the single
# item addressed by PK+SK — so it means "no item with this exact key", not "nothing in
# this partition". Audit records are write-once (§6.3, I13); a taken key means the ULID
# generator collided, and failing on that is the point, because an overwrite would
# destroy an existing record, the one thing an append-only log may never do.
aws_cli dynamodb put-item \
    --table-name "$TABLE" \
    --item "$(printf '%s' "$AUDIT_JSON" | jq -c .item)" \
    --condition-expression 'attribute_not_exists(#pk)' \
    --expression-attribute-names '{"#pk":"PK"}' >/dev/null ||
    die "the audit record could not be written; nothing was changed (I13)."
dim "  audit   $AUDIT_ACTION recorded in $TABLE"

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------

# pool_users renders the listing. Masked unless --reveal: the common accidents are a
# terminal capture pasted into an issue and a CI job log, and neither needs the address
# to be legible (§9.2). The domain survives masking because it is what tells an operator
# which organisation a user belongs to and is not identifying on its own; the first and
# last characters of the local part survive so two users at one domain stay
# distinguishable.
pool_users() {
    printf '%s' "$POOL_JSON" | jq --argjson reveal "$([ "$REVEAL" = "1" ] && echo true || echo false)" '
        def mask:
            split("@") as $p
            | ($p[0][0:1]) + "***" + (if ($p[0] | length) > 1 then $p[0][-1:] else "" end)
              + "@" + ($p[1:] | join("@"));
        (.Users // [])
        | map({
            email:   ([.Attributes[]? | select(.Name == "email") | .Value][0] // .Username),
            sub:     ([.Attributes[]? | select(.Name == "sub") | .Value][0] // ""),
            status:  (.UserStatus // ""),
            enabled: (.Enabled // false),
            created: (.UserCreateDate // "")
          })
        | if $reveal then . else map(.email |= mask) end'
}

# plan_json is every mutating AWS call an --apply invocation makes, in order. The audit
# put leads it because it happens first (I13, and §9.3's ordering rule) and because a
# plan that omitted it would be a plan that did not describe the write a dry run makes.
plan_json() {
    local calls=("dynamodb put-item")
    if [ "${#PLAN_CALLS[@]}" -gt 0 ]; then
        calls+=("${PLAN_CALLS[@]}")
    fi
    printf '%s\n' "${calls[@]}" | jq -R . | jq -sc .
}

# cognito_json is the subset that changes who can reach tenant data. Empty means this
# invocation has nothing to do, which is what makes `add` and `remove` idempotent.
cognito_json() {
    if [ "${#PLAN_CALLS[@]}" -eq 0 ]; then
        echo '[]'
        return 0
    fi
    printf '%s\n' "${PLAN_CALLS[@]}" | jq -R . | jq -sc .
}

# executed_json is what THIS invocation called, as against what the plan promises. The
# audit put has always happened by the time it is called; the Cognito calls happened
# only under --apply.
executed_json() {
    local calls=("dynamodb put-item")
    if [ "$APPLY" = "1" ] && [ "${#PLAN_CALLS[@]}" -gt 0 ]; then
        calls+=("${PLAN_CALLS[@]}")
    fi
    printf '%s\n' "${calls[@]}" | jq -R . | jq -sc .
}

emit_result() {
    local users_json='null'
    if [ "$SUBCOMMAND" = "list" ]; then
        users_json="$(pool_users)"
    fi
    jq -nc \
        --arg subcommand "$SUBCOMMAND" \
        --arg operation "$OPERATION" \
        --arg tenant "$TENANT" \
        --arg instance "$INSTANCE" \
        --arg region "$REGION" \
        --arg user_pool_id "$USER_POOL_ID" \
        --arg audit_action "$AUDIT_ACTION" \
        --arg apply_audit_action "$APPLY_ACTION" \
        --arg subject_digest "$SUBJECT_DIGEST" \
        --arg outcome "$OUTCOME" \
        --argjson dry_run "$([ "$APPLY" = "1" ] && echo false || echo true)" \
        --argjson subject_exists "$([ "$EXISTS" = "1" ] && echo true || echo false)" \
        --argjson masked "$([ "$REVEAL" = "1" ] && echo false || echo true)" \
        --argjson plan "$(plan_json)" \
        --argjson cognito_calls "$(cognito_json)" \
        --argjson executed "$(executed_json)" \
        --argjson users "$users_json" \
        '{subcommand: $subcommand, operation: $operation, tenant: $tenant,
          instance: $instance, region: $region, user_pool_id: $user_pool_id,
          dry_run: $dry_run, subject_exists: $subject_exists, outcome: $outcome,
          audit_action: $audit_action, subject_digest: $subject_digest,
          plan: $plan, cognito_calls: $cognito_calls, executed: $executed}
         + (if ($cognito_calls | length) > 0 then {apply_audit_action: $apply_audit_action} else {} end)
         + (if $users == null then {} else {masked: $masked, users: $users} end)'
}

# ---------------------------------------------------------------------------
# list — read-only, no --apply (§11.3)
# ---------------------------------------------------------------------------
#
# **Pool-wide, not tenant-scoped, and that is a known gap rather than an oversight.**
# The user pool carries no tenant attribute (infrastructure/template.yaml declares no
# custom:tenant_id in its schema), so there is nothing to filter on: this lists every
# user in the instance's pool, whichever tenant they belong to. It is correct in the
# personal phase, where tenant_id == user_id and the pool holds one user (§6.2), and it
# is the one read path in this script that I11 does not qualify. Closing it needs a
# custom:tenant_id attribute on the pool and on admin-create-user — an infrastructure
# change, recorded as a finding rather than worked around here.
if [ "$SUBCOMMAND" = "list" ]; then
    if [ "$AS_JSON" = "1" ]; then
        emit_result
    else
        printf '%-34s %-22s %-9s %s\n' email status enabled sub
        while IFS=$'\t' read -r email status enabled sub; do
            printf '%-34s %-22s %-9s %s\n' "$email" "$status" "$enabled" "$sub"
        done < <(pool_users | jq -r '.[] | [.email, .status, (.enabled | tostring), .sub] | @tsv')
        if [ "$REVEAL" = "1" ]; then
            warn "this output contains full email addresses — PII. Do not paste it into an issue, a chat, or a CI log (§9.2)."
        else
            dim "  local parts are masked; --reveal prints them in full"
        fi
        dim "  pool-wide, not tenant-scoped: the pool carries no tenant attribute (see the comment in this script)"
    fi
    exit 0
fi

# ---------------------------------------------------------------------------
# Plan output, and the dry-run exit
# ---------------------------------------------------------------------------

log ""
info "plan — ${SUBCOMMAND}"
dim "  tenant        $TENANT"
dim "  pool          $USER_POOL_ID"
dim "  subject       $SUBJECT_DIGEST"
if [ -n "$USER_SUB" ]; then
    dim "  cognito sub   $USER_SUB"
fi
if [ -n "$USER_STATUS" ]; then
    dim "  status        $USER_STATUS"
fi
if [ "${#PLAN_CALLS[@]}" -eq 0 ]; then
    dim "  calls         dynamodb put-item (audit: $AUDIT_ACTION)"
else
    dim "  calls         dynamodb put-item (audit: $APPLY_ACTION)"
    for call in "${PLAN_CALLS[@]}"; do
        dim "                $call"
    done
fi
if [ -n "$PLAN_NOTE" ]; then
    dim "  effect        $PLAN_NOTE"
fi

# Said on every remove invocation, in both modes. The operator's most likely wrong
# assumption is that removing the user removed their data, and the cost of believing it
# is a data-subject request answered incorrectly (§9.3).
if [ "$SUBCOMMAND" = "remove" ]; then
    log ""
    warn "removing this user does NOT erase tenant data."
    warn "  Captures, transcripts, audio, and items remain stored, encrypted, and billable."
    warn "  Application code has no delete path for L0 (I1); erasure is a separate,"
    warn "  separately permissioned operation (§9.3): scripts/erase-tenant.sh --tenant $TENANT"
fi

if [ "${#PLAN_CALLS[@]}" -eq 0 ]; then
    # Nothing to execute, so --apply and the default are the same invocation. Reported
    # as success: this is the idempotent case §Phase 0 asks for.
    log ""
    ok "nothing to do — ${PLAN_NOTE:-already in the requested state}"
    if [ "$AS_JSON" = "1" ]; then
        emit_result
    fi
    exit 0
fi

if ! confirm_apply "$APPLY" "${PLAN_NOTE:-$SUBCOMMAND the user}"; then
    dim "  the audit record for this read ($AUDIT_ACTION) was written; no Cognito call was made"
    if [ "$AS_JSON" = "1" ]; then
        emit_result
    fi
    exit 0
fi

# ---------------------------------------------------------------------------
# Execute
# ---------------------------------------------------------------------------

# run_cognito wraps one call so a service-side exception becomes an actionable message
# rather than a raw traceback, and so the address never reaches the error output: an AWS
# error quotes the request back, username included (§9.2).
run_cognito() { # run_cognito <description> <aws args...>
    local what="$1"
    shift
    local out
    if out="$(aws_cli "$@" 2>&1)"; then
        return 0
    fi
    case "$out" in
        *UsernameExistsException*)
            # Only reachable as a race: the probe said absent and something created the
            # user in between. The idempotent outcome, not a failure — and the guarantee
            # that no second invite goes out even if the probe was stale.
            warn "the user was created between the probe and this call; no second invite was sent"
            return 0
            ;;
        *UserNotFoundException*)
            die "the user no longer exists (removed between the probe and this call). Nothing was changed."
            ;;
        *NotAuthorizedException* | *AccessDenied*)
            die "not authorised to $what on $USER_POOL_ID. The audit record was already written; CloudTrail has the denial (§9.5)."
            ;;
        *)
            # The service message may quote the username, so it is described rather than
            # printed. The operator has CloudTrail and the digest above.
            err "the '$what' call failed; $(printf '%s' "$out" | wc -c) bytes of service output withheld because it quotes the request (§9.2) — see CloudTrail"
            die "nothing further was attempted."
            ;;
    esac
}

case "$OPERATION" in
    add)
        # email_verified=true because the operator vouches for the address by typing it.
        # Without it the user cannot use the forgot-password flow at all, and the
        # symptom shows up months later at the moment they need it. The pool's
        # AutoVerifiedAttributes covers self-service verification, which this pool does
        # not have (§9, self-signup disabled).
        run_cognito "create user" cognito-idp admin-create-user \
            --user-pool-id "$USER_POOL_ID" \
            --username "$EMAIL" \
            --user-attributes "Name=email,Value=${EMAIL}" "Name=email_verified,Value=true" \
            --desired-delivery-mediums EMAIL
        ok "user created; invite mailed"
        ;;
    resend)
        run_cognito "resend invite" cognito-idp admin-create-user \
            --user-pool-id "$USER_POOL_ID" \
            --username "$EMAIL" \
            --message-action RESEND \
            --desired-delivery-mediums EMAIL
        ok "invite resent with a new temporary password"
        ;;
    remove)
        run_cognito "delete user" cognito-idp admin-delete-user \
            --user-pool-id "$USER_POOL_ID" \
            --username "$EMAIL"
        ok "Cognito identity deleted — tenant data was NOT erased (§9.3)"
        ;;
    reset)
        run_cognito "reset password" cognito-idp admin-reset-user-password \
            --user-pool-id "$USER_POOL_ID" \
            --username "$EMAIL"
        ok "password reset; a reset code was mailed"
        ;;
    *) die "internal: no executor for operation '$OPERATION'" ;;
esac

if [ "$AS_JSON" = "1" ]; then
    emit_result
fi
exit 0
