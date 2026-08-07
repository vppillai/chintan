#!/bin/bash

# Teardown script for Chintan - Remove ALL Chintan resources
set -euo pipefail

# Source common utilities
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

show_usage() {
    cat << EOF
Usage: $(basename "$0") [--dry-run|--apply] [--yes]

Remove ALL Chintan infrastructure including all instances and bootstrap.

Options:
  --dry-run            Show what would be done without making changes (default)
  --apply              Actually perform the deletions
  --yes                Skip confirmation prompt (use with --apply)

This will delete:
  - All chintan-* CloudFormation stacks
  - All retained DynamoDB tables
  - All S3 buckets and contents
  - All CloudWatch log groups
  - All SSM parameters
  - Bootstrap infrastructure

WARNING: This action is irreversible and will destroy all data!

Examples:
  $(basename "$0")                        # Dry run
  $(basename "$0") --apply                # Delete with confirmation
  $(basename "$0") --apply --yes          # Delete without confirmation
EOF
}

parse_args() {
    local skip_confirmation=false
    
    while [[ $# -gt 0 ]]; do
        case $1 in
            --dry-run)
                DRY_RUN="true"
                shift
                ;;
            --apply)
                DRY_RUN="false"
                shift
                ;;
            --yes)
                skip_confirmation=true
                shift
                ;;
            -h|--help)
                show_usage
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                show_usage
                exit 1
                ;;
        esac
    done
    
    export DRY_RUN
    export SKIP_CONFIRMATION="$skip_confirmation"
}

get_all_chintan_stacks() {
    aws cloudformation list-stacks \
        --stack-status-filter CREATE_COMPLETE UPDATE_COMPLETE ROLLBACK_COMPLETE UPDATE_ROLLBACK_COMPLETE \
        --query "StackSummaries[?starts_with(StackName, \`$CHINTAN_PREFIX\`)].StackName" \
        --output text
}

cleanup_instance_stack() {
    local stack_name="$1"
    local instance_name="${stack_name#$CHINTAN_PREFIX}"
    
    log_info "Cleaning up instance stack: $stack_name"
    
    # Use the cleanup-aws script for thorough cleanup
    local cleanup_script="$SCRIPT_DIR/cleanup-aws.sh"
    if [[ -f "$cleanup_script" ]]; then
        if is_dry_run; then
            execute_cmd "$cleanup_script --instance '$instance_name' --dry-run"
        else
            # Run cleanup without confirmation since we already confirmed the teardown
            execute_cmd "SKIP_CONFIRMATION=true $cleanup_script --instance '$instance_name' --apply"
        fi
    else
        log_warn "Cleanup script not found, using basic stack deletion"
        if stack_exists "$stack_name"; then
            execute_cmd "aws cloudformation delete-stack --stack-name '$stack_name'"
            if ! is_dry_run; then
                wait_for_stack "$stack_name" "stack-delete-complete"
            fi
        fi
    fi
}

cleanup_bootstrap_stack() {
    local stack_name="$CHINTAN_BOOTSTRAP_STACK"
    
    if ! stack_exists "$stack_name"; then
        log_info "Bootstrap stack does not exist"
        return 0
    fi
    
    log_info "Cleaning up bootstrap stack: $stack_name"
    
    # Get bootstrap resources before deletion
    local resources
    if ! is_dry_run; then
        resources=$(aws cloudformation describe-stack-resources \
            --stack-name "$stack_name" \
            --query 'StackResources[].[LogicalResourceId,ResourceType,PhysicalResourceId]' \
            --output text)
    else
        resources="LambdaArtifactBucket	AWS::S3::Bucket	chintan-lambda-123456789-us-west-2"
    fi
    
    # Clean up S3 buckets first
    while IFS=$'\t' read -r logical_id resource_type physical_id; do
        if [[ "$resource_type" == "AWS::S3::Bucket" ]] && [[ "$physical_id" =~ ^chintan- ]]; then
            log_info "Emptying S3 bucket: $physical_id"
            if ! is_dry_run; then
                empty_s3_bucket "$physical_id"
            fi
        fi
    done <<< "$resources"
    
    # Delete the stack
    execute_cmd "aws cloudformation delete-stack --stack-name '$stack_name'"
    
    if ! is_dry_run; then
        wait_for_stack "$stack_name" "stack-delete-complete"
        log_success "Bootstrap stack deleted successfully"
    fi
}

cleanup_orphaned_resources() {
    log_info "Cleaning up any orphaned Chintan resources"
    
    # Find orphaned S3 buckets
    if ! is_dry_run; then
        local buckets
        buckets=$(aws s3api list-buckets --query "Buckets[?starts_with(Name, \`chintan-\`)].Name" --output text)
        for bucket in $buckets; do
            log_info "Found orphaned S3 bucket: $bucket"
            empty_s3_bucket "$bucket"
            execute_cmd "aws s3 rb 's3://$bucket'"
        done
    else
        log_info "[DRY-RUN] Would check for and clean up orphaned S3 buckets"
    fi
    
    # Find orphaned DynamoDB tables
    if ! is_dry_run; then
        local tables
        tables=$(aws dynamodb list-tables --query "TableNames[?starts_with(@, \`chintan-\`)]" --output text)
        for table in $tables; do
            log_info "Found orphaned DynamoDB table: $table"
            execute_cmd "aws dynamodb delete-table --table-name '$table'"
        done
    else
        log_info "[DRY-RUN] Would check for and clean up orphaned DynamoDB tables"
    fi
    
    # Find orphaned CloudWatch log groups
    if ! is_dry_run; then
        local log_groups
        log_groups=$(aws logs describe-log-groups --log-group-name-prefix "/aws/lambda/chintan-" --query "logGroups[].logGroupName" --output text)
        for log_group in $log_groups; do
            log_info "Found orphaned log group: $log_group"
            execute_cmd "aws logs delete-log-group --log-group-name '$log_group'"
        done
    else
        log_info "[DRY-RUN] Would check for and clean up orphaned CloudWatch log groups"
    fi
    
    # Find orphaned SSM parameters
    if ! is_dry_run; then
        local parameters
        parameters=$(aws ssm get-parameters-by-path --path "/chintan/" --recursive --query "Parameters[].Name" --output text)
        for param in $parameters; do
            log_info "Found orphaned SSM parameter: $param"
            execute_cmd "aws ssm delete-parameter --name '$param'"
        done
    else
        log_info "[DRY-RUN] Would check for and clean up orphaned SSM parameters"
    fi
}

main() {
    # Default to dry-run
    DRY_RUN="true"
    SKIP_CONFIRMATION="false"
    
    parse_args "$@"
    
    if is_dry_run; then
        log_warn "DRY-RUN MODE: No actual deletions will be performed"
        log_warn "Use --apply to execute the deletions"
    else
        if [[ "$SKIP_CONFIRMATION" != "true" ]]; then
            log_warn "⚠️  DANGER: This will permanently delete ALL Chintan resources! ⚠️"
            log_warn "This includes:"
            log_warn "  - All instance stacks and data"
            log_warn "  - All DynamoDB tables and content"
            log_warn "  - All S3 buckets and files"
            log_warn "  - Bootstrap infrastructure"
            log_warn "  - All configuration and secrets"
            echo
            read -p "Type 'DELETE ALL CHINTAN RESOURCES' to confirm: " -r
            if [[ "$REPLY" != "DELETE ALL CHINTAN RESOURCES" ]]; then
                log_info "Teardown cancelled"
                exit 0
            fi
        fi
    fi
    
    # Validate prerequisites
    check_aws_cli
    
    log_info "Starting complete Chintan teardown"
    
    # Get all Chintan stacks
    local stacks
    if ! is_dry_run; then
        stacks=$(get_all_chintan_stacks)
    else
        stacks="chintan-dev chintan-staging chintan-prod chintan-bootstrap"
    fi
    
    if [[ -z "$stacks" ]] && ! is_dry_run; then
        log_info "No Chintan stacks found"
    else
        log_info "Found Chintan stacks: $stacks"
        
        # Clean up instance stacks first
        for stack in $stacks; do
            if [[ "$stack" != "$CHINTAN_BOOTSTRAP_STACK" ]]; then
                cleanup_instance_stack "$stack"
            fi
        done
        
        # Clean up bootstrap stack last
        if [[ "$stacks" =~ $CHINTAN_BOOTSTRAP_STACK ]]; then
            cleanup_bootstrap_stack
        fi
    fi
    
    # Clean up any orphaned resources
    cleanup_orphaned_resources
    
    if is_dry_run; then
        log_info "Dry-run completed successfully"
        log_info "Run with --apply to execute these deletions"
        log_warn "Remember to add --yes to skip confirmation in automated scenarios"
    else
        log_success "Complete Chintan teardown completed successfully!"
        log_info "All Chintan resources have been removed"
    fi
}

main "$@"