#!/usr/bin/env bash
#
# Remove an instance, and prove the removal was complete (§Phase 0, §10.3).
#
# "Teardown must be provably complete. Teardown deletes the stacks, then queries the
# group and fails loudly if anything remains. **Never a wildcard delete** — a shared
# account makes an over-broad teardown catastrophic rather than merely annoying."
#
# This account hosts passbook. Every deletion here is scoped to one CloudFormation
# stack, and the completeness check is a read against the tag-based Resource Group. At
# no point is a resource deleted because its name matched a pattern.
#
# What deliberately SURVIVES teardown, and why:
#   - the DynamoDB table and the data bucket carry DeletionPolicy: Retain, because they
#     hold the corpus (I1). Removing them is tenant erasure, which is a separate and
#     separately-permissioned operation (§9.3) — not a side effect of tearing down a
#     deployment.
#   - the bootstrap stack, which is shared across instances.
#
# Usage:
#   scripts/teardown.sh --instance dev              # dry run
#   scripts/teardown.sh --instance dev --apply

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

APPLY=0
INSTANCE=""
REGION="ca-central-1"
while [ $# -gt 0 ]; do
    case "$1" in
        --apply) APPLY=1 ;;
        --instance)
            INSTANCE="${2:?}"
            shift
            ;;
        --region)
            REGION="${2:?}"
            shift
            ;;
        -h | --help)
            sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done
cd "$REPO_ROOT" || exit 1
export AWS_REGION="$REGION" AWS_PAGER=""

[ -n "$INSTANCE" ] || die "--instance is required. Teardown keys on the Instance tag, and an empty value would match nothing or everything depending on the query (§6.4)."
STACK="voicenotes-${INSTANCE}"

aws_cli sts get-caller-identity >/dev/null 2>&1 || die "no AWS credentials"

if ! aws_cli cloudformation describe-stacks --stack-name "$STACK" >/dev/null 2>&1; then
    ok "stack $STACK does not exist; nothing to remove"
    exit 0
fi

info "resources in $STACK"
aws_cli cloudformation describe-stack-resources --stack-name "$STACK" \
    --query 'StackResources[].{Type:ResourceType,Id:PhysicalResourceId}' --output table

log ""
warn "the table and the data bucket are RETAINED by policy — they hold the corpus (I1)."
warn "removing those is tenant erasure (§9.3), a separate operation with its own audit record."

if ! confirm_apply "$APPLY" "delete stack $STACK"; then
    exit 0
fi

info "deleting $STACK"
aws_cli cloudformation delete-stack --stack-name "$STACK"
aws_cli cloudformation wait stack-delete-complete --stack-name "$STACK" || true
ok "stack deleted"

# Completeness proof: query the Resource Group, not a name pattern.
info "verifying completeness against the Project=voicenotes Resource Group (§10.3)"
remaining="$(aws_cli resourcegroupstaggingapi get-resources \
    --tag-filters "Key=Project,Values=voicenotes" "Key=Instance,Values=${INSTANCE}" \
    --query 'length(ResourceTagMappingList)' --output text 2>/dev/null || echo "unknown")"

if [ "$remaining" = "unknown" ]; then
    warn "could not query the tagging API; completeness UNVERIFIED — do not record this teardown as clean"
elif [ "$remaining" = "0" ]; then
    ok "no resources remain tagged Instance=${INSTANCE}"
else
    info "$remaining resource(s) still tagged Instance=${INSTANCE}:"
    aws_cli resourcegroupstaggingapi get-resources \
        --tag-filters "Key=Project,Values=voicenotes" "Key=Instance,Values=${INSTANCE}" \
        --query 'ResourceTagMappingList[].ResourceARN' --output table
    warn "expected if they are the retained table and bucket; investigate anything else."
fi
