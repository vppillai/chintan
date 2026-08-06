#!/usr/bin/env bash
#
# Portability export of one tenant (§9.3, §11.4, Phase 0).
#
# §9.3: "a tenant-scoped operation producing a complete archive — Markdown, alignment
# sidecars, L0/L1/L2 transcripts, audio, rules, and metadata. Must be complete enough to
# satisfy a data-portability request and to migrate off the product entirely."
#
# WHAT MAKES IT COMPLETE, since a portability guarantee is only as good as its weakest
# omission: the archive is every record in the tenant's DynamoDB partition and every current
# object under its S3 prefix. Nothing is selected by entity type, so a type introduced in a
# later phase is exported without this script or the binary changing. That matters most now,
# in Phase 0, when most types do not exist yet and an omission would be invisible.
#
# ONE IMPLEMENTATION, NOT TWO. §Phase 7 adds a full-corpus export endpoint and requires that
# "export and export-tenant.sh produce identical content for the same tenant — one
# implementation, not two". That implementation is `chintanctl export`; this script is a
# front-end over it, and the Phase 7 handler will be another over the same code.
#
# THE ARCHIVE IS PLAINTEXT USER CONTENT OUTSIDE THE ENCRYPTED STORE. It holds verbatim
# transcripts and raw audio, and §9.2 treats the audio corpus as among the most sensitive
# content a product can hold. I8's at-rest encryption is a property of the bucket and the
# table; it does NOT follow these files. Files are written 0600 and directories 0700 — a
# permission, not encryption. Put the destination on an encrypted volume and delete it once
# the request is satisfied.
#
# --tenant is REQUIRED (I11) and every invocation writes an audit record (I13), including a
# dry run, because computing the plan reads the tenant's inventory. The audit record and the
# metering record (I12) are the only writes this operation makes to the corpus; it can never
# remove anything from it.
#
# --dry-run is the DEFAULT (§11.3). Reading a whole corpus costs S3 requests and egress and
# a mistaken invocation should print a plan, not copy a tenant's audio onto a laptop. The
# dry run prints the cost basis — request count and bytes — rather than a dollar figure: AWS
# rates are not in config, and a price hardcoded in the tool would drift silently. No
# provider is called, so §11.3's spend-breaker clause (§10.5.9) does not apply.
#
# ALL THE LOGIC IS IN chintanctl (§11.2): enumeration, the manifest, the SHA-256 per file,
# the path-traversal refusals and the plan --apply executes exactly. This script owns
# argument parsing, region defaulting, the confirmation prompt and output plumbing. NO
# BUSINESS LOGIC IN BASH — passbook's admin.sh and add-data.sh drifted ~300 duplicated lines
# apart, and §11.2 exists to stop that happening here.
#
# Usage:
#   scripts/export-tenant.sh --tenant <id> --instance prod --out /mnt/enc/export-2026-08
#   scripts/export-tenant.sh --tenant <id> --instance prod --out /mnt/enc/export --json
#   scripts/export-tenant.sh --tenant <id> --instance prod --out /mnt/enc/export --apply
#   scripts/export-tenant.sh --tenant <id> --fixtures scripts/test/fixtures/tenant-data \
#       --out /tmp/export        # credential-free test mode (§11.5)
#
# Prerequisites: the toolchain container (make, or scripts/dev.sh), AWS credentials for a
# live run, a config for the instance under config/instances/, and a destination that is
# either empty or a previous archive — re-exporting into one is allowed, because export is
# idempotent and re-runnable (§Phase 7).
#
# Exit codes: 0 planned or exported, 1 failure, 2 bad arguments, 3 refused — a destination
#             holding unrelated files, or an unconfirmed run. Refused is distinct from
#             failed because "nothing happened" and "something half-happened" call for
#             different next actions.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

TENANT=""
INSTANCE=""
REGION=""
ACCOUNT=""
OUT=""
FIXTURES=""
ACTOR="script:export-tenant"
AS_JSON=""
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
        --account)
            # Discovered from the caller's identity when omitted. It is part of the bucket
            # name (§6.2), not decoration: S3 bucket names are globally unique, so the
            # account is what makes this deployment's bucket distinguishable from another
            # clone of this repo.
            ACCOUNT="${2:?--account needs a value}"
            shift
            ;;
        --out)
            OUT="${2:?--out needs a directory}"
            shift
            ;;
        --fixtures)
            # Harness mode (§11.5): read a fixture set instead of live AWS, so the mutating
            # path is tested with no credentials.
            FIXTURES="${2:?--fixtures needs a path}"
            shift
            ;;
        --as)
            # Who is running this, recorded in the invocation's audit record (I13). A user
            # id, never an email: the audit package refuses an '@', because PII in the
            # longest-retained store in the system is not repairable (§9.2).
            ACTOR="${2:?--as needs an actor id}"
            shift
            ;;
        --json) AS_JSON="--json" ;;
        --apply) APPLY=1 ;;
        --dry-run) APPLY=0 ;;
        --yes) YES=1 ;;
        -h | --help)
            sed -n '2,60p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

# Paths are resolved against the INVOKING directory, before the cd below, and passed on
# absolute. The binary runs with its working directory inside backend/, so a relative --out
# would land somewhere the operator did not name — and for an export that means a plaintext
# copy of a tenant's audio in an unexpected place.
abs_path() {
    case "$1" in
        /*) printf '%s\n' "$1" ;;
        *) printf '%s/%s\n' "$PWD" "$1" ;;
    esac
}
[ -n "$OUT" ] && OUT="$(abs_path "$OUT")"
[ -n "$FIXTURES" ] && FIXTURES="$(abs_path "$FIXTURES")"

cd "$REPO_ROOT" || exit 1

# I11 via §11.3: no data operation runs untenanted. An export with no tenant would be
# either nothing or everything, and one of those is a cross-tenant copy.
require_tenant "$TENANT"
[ -n "$OUT" ] || die "--out is required: name the directory to write the archive into"

ARGS=(export --tenant "$TENANT" --out "$OUT" --as "$ACTOR")

if [ -n "$FIXTURES" ]; then
    [ -e "$FIXTURES" ] || die "no fixture set at '$FIXTURES'"
    ARGS+=(--fixtures "$FIXTURES")
else
    # Required rather than defaulted, for the reason backup.sh, audit.sh and usage.sh
    # require it: the corpus lives in the instance's own table and bucket, and a default
    # would quietly export the wrong environment — a wrong answer discovered only by whoever
    # migrates onto the archive.
    [ -n "$INSTANCE" ] || die "--instance is required (or --fixtures for the CI mode): the corpus lives in the instance's own table and bucket (§6.3)"
    CONFIG="config/instances/${INSTANCE}.yaml"
    [ -f "$CONFIG" ] || die "no config for instance '$INSTANCE' at $CONFIG"
    if [ -z "$REGION" ]; then
        # Region from config, not the ambient environment — deploy.sh and backup.sh do the
        # same. Reading the wrong region's table produces an empty archive that looks like a
        # successful one.
        REGION="$(yq -r '.region' "$CONFIG")"
    fi
    [ -n "$REGION" ] && [ "$REGION" != "null" ] || die "no region in $CONFIG"
    export AWS_REGION="$REGION" AWS_PAGER=""

    if [ -z "$ACCOUNT" ]; then
        ACCOUNT="$(aws_cli sts get-caller-identity --output json 2>/dev/null | jq -r '.Account // empty')"
        [ -n "$ACCOUNT" ] || die "could not determine the AWS account id; pass --account, or check your credentials"
    fi
    ARGS+=(--instance "$INSTANCE" --region "$REGION" --account "$ACCOUNT" --config "../$CONFIG")
fi

[ -n "$AS_JSON" ] && ARGS+=("$AS_JSON")

if [ "$APPLY" = "1" ]; then
    # The confirmation prompt is the wrapper's job (§11.2). Interactive only: the harness and
    # CI have no terminal, and a prompt that blocked there would turn a test into a hang.
    # --yes skips it for a scripted run that has already decided.
    if [ "$YES" = "0" ] && [ -t 0 ]; then
        warn "This writes tenant '$TENANT' — verbatim transcripts and raw audio — to $OUT in PLAINTEXT."
        warn "The bucket's and table's at-rest encryption does not follow it (I8, §9.2)."
        printf 'Type the tenant id to continue: ' >&2
        read -r reply
        [ "$reply" = "$TENANT" ] || die "not confirmed; nothing was read and nothing was written"
    fi
    ARGS+=(--apply)
fi

# The plan, the cost basis, the notes and the refusals all come from chintanctl, which
# prints them to stdout so --json stays parseable. This script adds nothing to them.
set +e
(cd backend && go run ./cmd/chintanctl "${ARGS[@]}")
status=$?
set -e
exit "$status"
