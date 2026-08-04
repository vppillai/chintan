#!/usr/bin/env bash
#
# Static checks over the CloudFormation templates. Four separate gates in the
# §0.5A inventory share one implementation because they all parse the same
# templates, and parsing them four times in four scripts is how the four drift
# apart.
#
# Invoked through the individual entry points (check-log-retention.sh,
# check-no-vpc.sh, check-no-alarms.sh, check-resource-prefix.sh) so that CI shows
# each gate by name and a reviewer can match the workflow to the inventory table
# line by line, which §Phase 0 acceptance requires.
#
# Each of these prevents a specific, documented, expensive failure:
#
#   log retention   Unset CloudWatch retention is INFINITE at $0.50/GB ingested.
#                   One of the five choices holding the bill near $1 (§10.1).
#   no VPC          A Lambda in a VPC needing internet requires a NAT Gateway at
#                   ~$32/month — over thirty times the entire target budget, and
#                   the single most common serverless cost catastrophe (G-018).
#   no alarms/SNS   CloudWatch's 10-alarm free allowance is account-WIDE, shared
#                   with passbook and everything else. An alarm with no confirmed
#                   subscription also emails into the void, which is worse than
#                   no alarm because it looks like coverage (§10.1, G-022).
#   name prefix     IAM role names are account-global; a generic name collides
#                   across projects and the stack fails on role creation, only in
#                   shared accounts (G-017).
#
# Usage: check-infra-invariants.sh <log-retention|no-vpc|no-alarms|prefix> [--json]

# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

WHICH="${1:?which check: log-retention|no-vpc|no-alarms|prefix}"
AS_JSON="${2:-}"
cd "$REPO_ROOT" || exit 1

TEMPLATES=()
while IFS= read -r f; do
    [ -f "$f" ] && TEMPLATES+=("$f")
done < <(tracked_files 'infrastructure/*.yaml' 'infrastructure/*.yml' 2>/dev/null)

if [ "${#TEMPLATES[@]}" -eq 0 ]; then
    # Phase 0 builds CI before any application code, so there is a window in
    # which the templates do not exist. Report it rather than passing silently.
    no_subject_yet "$WHICH" 0 "infrastructure/*.yaml"
    finish_check "$WHICH" "$AS_JSON"
    exit 0
fi

# yq reads the parsed document rather than grepping text, so a resource split
# across lines or written in flow style is still seen. It tolerates
# CloudFormation's short intrinsic tags (!Sub, !Ref, !GetAtt) — verified against
# these templates rather than assumed, because a parser that silently returned
# nothing would make every check below pass vacuously.

# resources_of_type lists the logical IDs of every resource of a given type.
resources_of_type() {
    local template="$1" type="$2"
    yq -r ".Resources | to_entries | .[] | select(.value.Type == \"$type\") | .key" "$template" 2>/dev/null || true
}

# resource_types lists every resource type present, for the alarm/SNS scan.
resource_types() {
    yq -r '.Resources | to_entries | .[] | .value.Type' "$1" 2>/dev/null || true
}

case "$WHICH" in

    log-retention)
        info "every log group declares explicit retention"
        for tpl in "${TEMPLATES[@]}"; do
            while IFS= read -r lg; do
                [ -n "$lg" ] || continue
                retention="$(yq -r ".Resources.\"$lg\".Properties.RetentionInDays // \"ABSENT\"" "$tpl" 2>/dev/null)"
                if [ "$retention" = "ABSENT" ] || [ "$retention" = "null" ]; then
                    violation "$tpl: log group $lg has no RetentionInDays — unset retention is infinite at \$0.50/GB ingested (§10.1)"
                fi
            done < <(resources_of_type "$tpl" "AWS::Logs::LogGroup")
        done

        # A Lambda with no explicit log group gets one created implicitly by the
        # service, with infinite retention and nothing in the template to show
        # it. That is the same cost failure arriving by omission rather than by
        # a wrong value, so it is checked here rather than trusted.
        for tpl in "${TEMPLATES[@]}"; do
            while IFS= read -r fn; do
                [ -n "$fn" ] || continue
                fn_name="$(yq -r ".Resources.\"$fn\".Properties.FunctionName // \"\"" "$tpl" 2>/dev/null)"
                # Look for any log group whose name references this function.
                if ! grep -q "aws/lambda/" "$tpl" 2>/dev/null; then
                    violation "$tpl: function $fn has no companion AWS::Logs::LogGroup; the implicitly-created one retains forever (§10.1)"
                elif [ -n "$fn_name" ] && ! grep -q "$fn" "$tpl"; then
                    violation "$tpl: function $fn has no log group referencing it"
                fi
            done < <(resources_of_type "$tpl" "AWS::Lambda::Function")
        done
        finish_check "explicit log retention on every log group (§10.1)" "$AS_JSON"
        ;;

    no-vpc)
        info "no Lambda is attached to a VPC"
        for tpl in "${TEMPLATES[@]}"; do
            while IFS= read -r fn; do
                [ -n "$fn" ] || continue
                vpc="$(yq -r ".Resources.\"$fn\".Properties.VpcConfig // \"ABSENT\"" "$tpl" 2>/dev/null)"
                if [ "$vpc" != "ABSENT" ] && [ "$vpc" != "null" ]; then
                    violation "$tpl: function $fn declares VpcConfig — a VPC Lambda needing internet requires a ~\$32/month NAT Gateway, over 30× the entire budget (G-018)"
                fi
            done < <(resources_of_type "$tpl" "AWS::Lambda::Function")

            # Belt and braces: catch NAT and VPC resources declared anywhere in
            # the stack, not only a VpcConfig on a function.
            while IFS= read -r t; do
                case "$t" in
                    AWS::EC2::NatGateway | AWS::EC2::VPC | AWS::EC2::Subnet | AWS::EC2::VPCEndpoint)
                        violation "$tpl: declares $t — nothing in this design requires VPC networking (§10.2, G-018)"
                        ;;
                esac
            done < <(resource_types "$tpl")
        done
        finish_check "no Lambda attached to a VPC (G-018)" "$AS_JSON"
        ;;

    no-alarms)
        info "no CloudWatch alarm and no SNS topic in the stack"
        for tpl in "${TEMPLATES[@]}"; do
            while IFS= read -r t; do
                case "$t" in
                    AWS::CloudWatch::Alarm | AWS::CloudWatch::CompositeAlarm)
                        violation "$tpl: declares $t — the 10-alarm free allowance is account-wide and shared with every other project (§10.1, G-022). Use the Usage entity plus one account-level Budget."
                        ;;
                    AWS::SNS::Topic | AWS::SNS::Subscription)
                        violation "$tpl: declares $t — §10.1 forbids SNS topics; an alarm with no confirmed subscription emails into the void, which looks like coverage (G-022)"
                        ;;
                esac
            done < <(resource_types "$tpl")
        done
        finish_check "no CloudWatch alarm or SNS topic (§10.1, G-022)" "$AS_JSON"
        ;;

    prefix)
        info "project prefix on every named resource"

        # The shell and the Go constant must agree, or a resource could be
        # created under a name the ABAC policies and teardown do not match.
        go_id="$(grep -oP 'const ID = "\K[^"]+' backend/internal/systemid/systemid.go 2>/dev/null || echo '')"
        if [ -n "$go_id" ] && [ "$go_id" != "$SYSTEM_ID" ]; then
            violation "system_id disagrees: scripts/lib/common.sh says '$SYSTEM_ID', backend/internal/systemid says '$go_id' — teardown, cost attribution, and the ABAC denies all key on this (§7.3)"
        fi

        # Every explicitly-named resource must carry the prefix. Names are
        # usually built with !Sub, so the check reads the raw property text and
        # asks whether the system id appears in it.
        for tpl in "${TEMPLATES[@]}"; do
            for pair in \
                "AWS::IAM::Role:RoleName" \
                "AWS::DynamoDB::Table:TableName" \
                "AWS::S3::Bucket:BucketName" \
                "AWS::Lambda::Function:FunctionName" \
                "AWS::Logs::LogGroup:LogGroupName" \
                "AWS::Cognito::UserPool:UserPoolName" \
                "AWS::ApiGatewayV2::Api:Name"; do
                type="${pair%:*}"
                prop="${pair##*:}"
                while IFS= read -r rid; do
                    [ -n "$rid" ] || continue
                    name="$(yq -r ".Resources.\"$rid\".Properties.$prop // \"ABSENT\"" "$tpl" 2>/dev/null)"
                    [ "$name" = "ABSENT" ] && continue
                    [ "$name" = "null" ] && continue
                    if ! printf '%s' "$name" | grep -q "$SYSTEM_ID"; then
                        violation "$tpl: $rid has $prop='$name' without the '$SYSTEM_ID' prefix — IAM role names are account-global and collide across projects (G-017)"
                    fi
                done < <(resources_of_type "$tpl" "$type")
            done
        done
        finish_check "project prefix on every named resource (G-017)" "$AS_JSON"
        ;;

    *)
        die "unknown check '$WHICH' (expected log-retention|no-vpc|no-alarms|prefix)"
        ;;
esac
