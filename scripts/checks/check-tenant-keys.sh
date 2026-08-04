#!/usr/bin/env bash
#
# Static check: no DynamoDB or S3 key is constructed outside the tenant-scoped
# helper (I11, §0.5A, §Phase 0 acceptance).
#
# "Cross-tenant leakage is the one bug a multi-tenant product cannot survive."
# The way it happens is never a deliberate unscoped query — it is one key built
# by hand, somewhere, months after the helper was written and by someone who did
# not know it existed.
#
# backend/internal/keys is the only package permitted to contain a key-prefix
# literal. This check enforces that monopoly; without it, that package is a
# convention rather than a control, and §Phase 0 requires specifically that "a
# static check fails the build if any DynamoDB or S3 key is constructed outside
# the tenant-scoped helper."
#
# Usage: check-tenant-keys.sh [--json]

# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

AS_JSON="${1:-}"
cd "$REPO_ROOT" || exit 1

# The prefixes that participate in a key (§6.3, §6.2). A literal from this list
# appearing outside the keys package means someone is assembling a key by hand.
#
# Deliberately includes the S3 tenant root: the per-tenant S3 prefix is enforced
# in IAM policy conditions (§9.1), so a hand-built S3 path is a security boundary
# being bypassed, not just a style problem.
KEY_LITERALS=(
    'TENANT#'
    'USER#'
    'CAPTURE#'
    'ITEM#'
    'THREAD#'
    'SEG#'
    'SESSION#'
    'INGEST#'
    'RULE#'
    'USAGE#'
    'AUDIT#'
    'METRIC#'
    'IDEM#'
    'TG#'
    'UPDATED#'
    'tenants/'
)

# Paths allowed to contain these literals, with the reason each is exempt.
#
#   backend/internal/keys/       the helper itself, and its tests
#   infrastructure/              IAM policy conditions must name the S3 prefix
#   scripts/checks/              this file lists the literals it searches for
#   docs/                        the spec and registers quote the key shapes
#   scripts/test/fake-aws/       harness fixtures model stored records
is_exempt() {
    case "$1" in
        backend/internal/keys/*) return 0 ;;
        infrastructure/*) return 0 ;;
        scripts/checks/check-tenant-keys.sh) return 0 ;;
        scripts/test/fake-aws/*) return 0 ;;
        docs/*) return 0 ;;
        *) return 1 ;;
    esac
}

info "checking for key literals outside backend/internal/keys"

# Search Go, TypeScript, and shell sources. The frontend is included on purpose:
# it must never build a key either — it holds no tenant identity, since tenant_id
# comes from a validated JWT claim server-side and never from the client (§6.6).
while IFS= read -r file; do
    [ -f "$file" ] || continue
    is_exempt "$file" && continue

    for literal in "${KEY_LITERALS[@]}"; do
        # -F: fixed string, so '#' and '/' need no escaping.
        # -n: report the line, so the failure is directly actionable.
        if matches="$(grep -Fn -- "$literal" "$file" 2>/dev/null)"; then
            while IFS= read -r m; do
                line_no="${m%%:*}"
                violation "$file:$line_no builds a key by hand (found '$literal') — every key must come from backend/internal/keys (I11)"
            done <<<"$matches"
        fi
    done
done < <(tracked_files '*.go' '*.ts' '*.js' '*.sh' '*.yaml' '*.yml' 2>/dev/null)

# The mirror-image failure: the helper existing but nothing calling it. A build
# where no package imports keys is one where a parallel key-construction path was
# added and this check's literal search happened not to catch it.
#
# Only asserted once a data path exists. Phase 0 has no handler that stores user
# content yet, so an import count of zero is correct now and wrong later — the
# check reports which state it is in rather than silently accepting both.
importers="$(grep -rl 'internal/keys' --include='*.go' backend 2>/dev/null | grep -cv '^backend/internal/keys/' || true)"
if [ "$importers" = "0" ]; then
    dim "  no package imports internal/keys yet — correct in Phase 0, which has no storage path"
    dim "  this becomes a violation once a handler stores user content (Phase 1)"
else
    dim "  $importers package(s) import internal/keys"
fi

finish_check "tenant-key helper enforcement (I11)" "$AS_JSON"
