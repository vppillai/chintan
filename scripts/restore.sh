#!/usr/bin/env bash
#
# Restore a snapshot into a NAMED tenant (§11.4, Phase 0, "Backup and data protection").
#
# DESTRUCTIVE, so --dry-run is the DEFAULT and --apply executes (§11.3) — "the single most
# important convention for agent safety: a mistaken invocation prints a plan instead of
# causing damage."
#
# IT NEVER OVERWRITES ANYTHING, and that is the design rather than a limitation:
#   absent at the target          written
#   present, byte-identical       skipped, so an interrupted restore resumes
#   present and different, L0     REFUSED, always, in every mode. Raw transcripts are
#                                 immutable (I1); only tenant erasure may remove one
#                                 (§9.3) and nothing may replace one. Overwriting one
#                                 here would be an I1 violation committed by an
#                                 operational script, and it would destroy the L0-to-L2
#                                 diff that trains the correction system (§6.1).
#   present and different, other  refused by default; --on-conflict skip skips them
# There is no --overwrite flag. To restore over existing data, restore into a fresh tenant
# id, or erase the tenant first with the separately permissioned erasure operation.
#
# RE-KEYING IS MANDATORY, because the target tenant may differ from the source and I11
# binds "admin and migration scripts" explicitly. The archive deliberately carries no
# tenant: records are stored by sort key and objects by a path relative to the per-tenant
# prefix, so every key written is built for the TARGET tenant through the one key helper.
# Tenant references inside stored attributes are rewritten through that same helper, and a
# residual reference the tool cannot classify refuses the restore rather than writing a
# dangling cross-tenant pointer (--allow-source-tenant-refs accepts it once you have
# looked). Identifiers inside sort keys are preserved — restore does not invent identities,
# so in the personal phase, where tenant_id == user_id, a restore into a different tenant
# leaves user records naming the SOURCE user id and the real user still needs users.sh.
#
# --tenant is REQUIRED (I11) and names the TARGET. Every invocation writes an audit record
# (I13), including a dry run, and it is written before anything is read.
#
# ALL THE LOGIC IS IN chintanctl (§11.2): the plan, the hash verification, the conflict
# classification, the re-keying. This script owns argument parsing, the confirmation
# prompt, region defaulting and output plumbing. NO BUSINESS LOGIC IN BASH.
#
# Usage:
#   scripts/restore.sh --tenant <id> --instance prod --from /mnt/enc/snap-2026-08-05
#   scripts/restore.sh --tenant <id> --instance prod --from /mnt/enc/snap --apply
#   scripts/restore.sh --tenant <id> --instance prod --from /mnt/enc/snap \
#       --on-conflict skip --apply
#   scripts/restore.sh --tenant <id> --store /tmp/store --from /tmp/snap --json
#
# Prerequisites: the toolchain container, AWS credentials for the agent principal for a
# live run, and a snapshot directory containing a manifest — a directory without one is an
# interrupted backup and is refused.
#
# Exit codes: 0 planned or applied, 1 failure (including a partial write, which the output
#             quantifies), 2 bad arguments, 3 refused — a conflict, a damaged archive, or
#             an unclassifiable tenant reference. Nothing was written on a 3.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

TENANT=""
INSTANCE=""
REGION=""
FROM=""
STORE=""
ACTOR=""
AS_JSON=""
ON_CONFLICT=""
ALLOW_REFS=""
APPLY=0
YES=0

while [ $# -gt 0 ]; do
    case "$1" in
        --tenant)
            TENANT="${2:?--tenant needs a value}"
            shift
            ;;
        --instance)
            INSTANCE="${2:?--instance needs a value}"
            shift
            ;;
        --region)
            REGION="${2:?--region needs a value}"
            shift
            ;;
        --from)
            FROM="${2:?--from needs a snapshot directory}"
            shift
            ;;
        --store)
            # CI/harness mode (§11.5): write into a local store tree instead of live AWS,
            # so both --dry-run and --apply are tested with no credentials.
            STORE="${2:?--store needs a directory}"
            shift
            ;;
        --on-conflict)
            ON_CONFLICT="${2:?--on-conflict needs refuse or skip}"
            shift
            ;;
        --allow-source-tenant-refs) ALLOW_REFS="--allow-source-tenant-refs" ;;
        --as)
            ACTOR="${2:?--as needs an actor id}"
            shift
            ;;
        --json) AS_JSON="--json" ;;
        --apply) APPLY=1 ;;
        --dry-run) APPLY=0 ;;
        --yes) YES=1 ;;
        -h | --help)
            sed -n '2,53p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done
cd "$REPO_ROOT" || exit 1

# I11 via §11.3. The tenant is the TARGET, and it is required rather than read from the
# snapshot on purpose: a restore that took its destination from the archive would put data
# back into whichever tenant the file happened to name, which is a cross-tenant write one
# typo away.
require_tenant "$TENANT"
[ -n "$FROM" ] || die "--from is required: name the snapshot directory to restore"
[ -d "$FROM" ] || die "no snapshot directory at '$FROM'"

ARGS=(restore --tenant "$TENANT" --from "$(cd "$FROM" && pwd)")

if [ -n "$STORE" ]; then
    [ -d "$STORE" ] || die "no store directory at '$STORE'"
    ARGS+=(--store "$(cd "$STORE" && pwd)")
else
    [ -n "$INSTANCE" ] || die "--instance is required (or --store for the CI mode): the corpus lives in the instance's own table and bucket (§6.3)"
    CONFIG="config/instances/${INSTANCE}.yaml"
    [ -f "$CONFIG" ] || die "no config for instance '$INSTANCE' at $CONFIG"
    if [ -z "$REGION" ]; then
        # Region from config, not the ambient environment. Writing into the wrong region's
        # table is the one mistake here that a dry run cannot warn you about, because the
        # plan against an empty table looks perfectly reasonable.
        REGION="$(yq -r '.region' "$CONFIG")"
    fi
    [ -n "$REGION" ] && [ "$REGION" != "null" ] || die "no region in $CONFIG"
    export AWS_REGION="$REGION" AWS_PAGER=""
    ARGS+=(--instance "$INSTANCE")
fi

[ -n "$ON_CONFLICT" ] && ARGS+=(--on-conflict "$ON_CONFLICT")
[ -n "$ALLOW_REFS" ] && ARGS+=("$ALLOW_REFS")
[ -n "$ACTOR" ] && ARGS+=(--as "$ACTOR")
[ -n "$AS_JSON" ] && ARGS+=("$AS_JSON")

if [ "$APPLY" = "1" ]; then
    # Interactive confirmation only, for the reason backup.sh gives: a prompt with no
    # terminal is a hung CI job. The prompt asks for the TARGET tenant id specifically,
    # because the mistake this catches is restoring the right archive into the wrong
    # tenant.
    if [ "$YES" = "0" ] && [ -t 0 ]; then
        warn "This writes the snapshot at $FROM into tenant '$TENANT'."
        warn "Nothing already present will be replaced — a differing raw transcript refuses the run (I1)."
        printf 'Type the TARGET tenant id to continue: ' >&2
        read -r reply
        [ "$reply" = "$TENANT" ] || die "not confirmed; nothing was written"
    fi
    ARGS+=(--apply)
fi

# Built and executed, NOT `go run`: go run does not propagate its child's exit code — it
# exits 1 and prints "exit status 3" to stderr, which would flatten "refused, nothing
# happened" into a generic failure and make exit code 3 unreachable for any caller (the
# same trap usage.sh documents). The binary lands in build/, which is gitignored;
# CHINTANCTL points at a prebuilt one so a test or a CI job need not rebuild per
# invocation.
CTL="${CHINTANCTL:-}"
if [ -z "$CTL" ]; then
    CTL="$REPO_ROOT/build/chintanctl"
    (cd backend && go build -o "$CTL" ./cmd/chintanctl) || die "building chintanctl failed"
fi

# `set +e` around the call: the exit code IS the result here — 3 means the operation
# refused and nothing was written — and letting the shell abort on it would throw away the
# distinction §11.3 asks for.
set +e
"$CTL" "${ARGS[@]}"
status=$?
set -e
exit "$status"
