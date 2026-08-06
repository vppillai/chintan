#!/usr/bin/env bash
#
# Full snapshot of one tenant — DynamoDB records plus S3 objects — to a local
# destination (§11.4, Phase 0, "Backup and data protection").
#
# WHAT THIS IS FOR, AND WHAT PITR IS FOR. PITR is enabled on the table and versioning on
# the bucket, so this is not the only recovery mechanism and does not duplicate one:
#   PITR / versioning  in-place time travel inside one account, to any second in the
#                      retention window, at no operator effort. It is also the only
#                      ATOMIC cut. Use it for "the pipeline wrote nonsense an hour ago".
#   this snapshot      a PORTABLE, self-describing copy that outlives the account, the
#                      table and the stack. Use it for moving a tenant to another
#                      instance, seeding a new deployment, or keeping a copy off AWS
#                      before a risky migration.
# A snapshot reads records first and objects second, so it is not a consistent cut across
# both stores. If you need one, use PITR.
#
# THE SNAPSHOT IS PLAINTEXT USER CONTENT OUTSIDE THE ENCRYPTED STORE. It contains verbatim
# transcripts and raw audio, and §9.2 treats the audio corpus as among the most sensitive
# content a product can hold. The at-rest encryption I8 requires is a property of the
# bucket and the table — SSE-S3 and SSE-DynamoDB with AWS-managed keys — and it does NOT
# follow these files. Once written they are plaintext on whatever filesystem you named,
# with no key to revoke and therefore no crypto-shredding path (§9.3). Files are created
# 0600 and directories 0700, and --apply requires --accept-plaintext-copy so the operator's
# acceptance of that is explicit and recorded in the audit log.
#
# --tenant is REQUIRED (I11) and every invocation writes an audit record (I13) — including
# a dry run, because computing the plan reads user content. That record is the only write
# this operation makes to the corpus; it cannot modify the corpus and can never remove
# anything from it.
#
# --dry-run is the DEFAULT (§11.3). Reading a whole corpus costs money and a mistaken
# invocation should print a plan, not copy a tenant's audio onto a laptop.
#
# ALL THE LOGIC IS IN chintanctl (§11.2): tenant-scoped enumeration, the manifest, the
# hashes, the key re-basing, and the plan that --apply executes exactly. This script owns
# argument parsing, the confirmation prompt, region defaulting and output plumbing. NO
# BUSINESS LOGIC IN BASH — passbook's admin.sh and add-data.sh drifted ~300 duplicated
# lines apart, and §11.2 exists to stop that happening here.
#
# Usage:
#   scripts/backup.sh --tenant <id> --instance prod --dest /mnt/enc/snap-2026-08-05
#   scripts/backup.sh --tenant <id> --instance prod --dest /mnt/enc/snap --json
#   scripts/backup.sh --tenant <id> --instance prod --dest /mnt/enc/snap --apply \
#       --accept-plaintext-copy
#   scripts/backup.sh --tenant <id> --store scripts/test/fixtures/... --dest /tmp/snap
#
# Prerequisites: the toolchain container (make/scripts/dev.sh), AWS credentials for the
# agent principal for a live run, and a destination directory that does not already hold a
# snapshot — a point-in-time copy cannot be merged into an existing one.
#
# Exit codes: 0 planned or applied, 1 failure, 2 bad arguments, 3 refused (a non-empty
#             destination, a missing acknowledgement, an unrepresentable record). Refused
#             is distinct from failed because "nothing happened" and "something
#             half-happened" call for different next actions.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

TENANT=""
INSTANCE=""
REGION=""
DEST=""
STORE=""
ACTOR=""
AS_JSON=""
ACCEPT=""
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
        --dest)
            DEST="${2:?--dest needs a directory}"
            shift
            ;;
        --store)
            # CI/harness mode (§11.5): read a local store tree instead of live AWS, so the
            # mutating path is tested with no credentials.
            STORE="${2:?--store needs a directory}"
            shift
            ;;
        --as)
            # Who is running this, recorded in the invocation's own audit record (I13). A
            # user id, never an email: the audit package refuses an '@', because PII in the
            # longest-retained store in the system is not repairable (§9.2).
            ACTOR="${2:?--as needs an actor id}"
            shift
            ;;
        --accept-plaintext-copy) ACCEPT="--accept-plaintext-copy" ;;
        --json) AS_JSON="--json" ;;
        --apply) APPLY=1 ;;
        --dry-run) APPLY=0 ;;
        --yes) YES=1 ;;
        -h | --help)
            sed -n '2,56p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done
cd "$REPO_ROOT" || exit 1

# I11 via §11.3: no data operation runs untenanted. A backup without a tenant would be
# either nothing or everything, and one of those is a cross-tenant copy.
require_tenant "$TENANT"
[ -n "$DEST" ] || die "--dest is required: name the directory to write the snapshot into"

ARGS=(backup --tenant "$TENANT" --dest "$DEST")

if [ -n "$STORE" ]; then
    [ -d "$STORE" ] || die "no store directory at '$STORE'"
    ARGS+=(--store "$(cd "$STORE" && pwd)")
else
    # Required rather than defaulted, for the reason audit.sh and usage.sh require it: the
    # corpus lives in the instance's own table and bucket, and a default would quietly
    # snapshot the wrong environment — which is a wrong answer you only discover when you
    # restore from it.
    [ -n "$INSTANCE" ] || die "--instance is required (or --store for the CI mode): the corpus lives in the instance's own table and bucket (§6.3)"
    CONFIG="config/instances/${INSTANCE}.yaml"
    [ -f "$CONFIG" ] || die "no config for instance '$INSTANCE' at $CONFIG"
    if [ -z "$REGION" ]; then
        # Region from config, not the ambient environment (deploy.sh, audit.sh and usage.sh
        # all do the same). Reading the wrong region's table would produce an empty
        # snapshot that looks like a successful one.
        REGION="$(yq -r '.region' "$CONFIG")"
    fi
    [ -n "$REGION" ] && [ "$REGION" != "null" ] || die "no region in $CONFIG"
    export AWS_REGION="$REGION" AWS_PAGER=""
    ARGS+=(--instance "$INSTANCE")
fi

[ -n "$ACTOR" ] && ARGS+=(--as "$ACTOR")
[ -n "$AS_JSON" ] && ARGS+=("$AS_JSON")
[ -n "$ACCEPT" ] && ARGS+=("$ACCEPT")

if [ "$APPLY" = "1" ]; then
    # The confirmation prompt is the wrapper's job (§11.2). Interactive only: the harness
    # and CI run with no terminal, and a prompt that blocks there would turn a test into a
    # hang. --yes skips it for a scripted run that has already decided.
    if [ "$YES" = "0" ] && [ -t 0 ]; then
        warn "This copies tenant '$TENANT' — verbatim transcripts and raw audio — to $DEST in PLAINTEXT."
        warn "The bucket's and table's at-rest encryption does not follow it (I8, §9.2)."
        printf 'Type the tenant id to continue: ' >&2
        read -r reply
        [ "$reply" = "$TENANT" ] || die "not confirmed; nothing was read and nothing was written"
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

# The plan, the cost estimate and the refusals all come from chintanctl, which prints them
# to stdout so --json stays parseable. This script adds nothing to them.
#
# `set +e` around the call: the exit code IS the result here — 3 means the operation
# refused and nothing happened — and letting the shell abort on it would throw away the
# distinction §11.3 asks for.
set +e
"$CTL" "${ARGS[@]}"
status=$?
set -e
exit "$status"
