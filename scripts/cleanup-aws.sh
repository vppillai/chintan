#!/bin/bash

# Cleanup script for Chintan - Remove specific instance resources
set -euo pipefail

# Source common utilities
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

# Default values
INSTANCE_NAME=""

show_usage() {
    cat << EOF
Usage: $(basename "$0") --instance INSTANCE [--dry-run|--apply]

Delete Chintan instance infrastructure including retained resources.

Required:
  --instance INSTANCE   Instance name to delete

Options:
  --dry-run            Show what would be done without making changes (default)
  --apply              Actually perform the deletions

This will delete:
  - CloudFormation stack: chintan-INSTANCE
  - DynamoDB table (even with DeletionPolicy: Retain)
  - S3 content bucket and all versions
  - CloudWatch log groups

Examples:
  $(basename "$0") --instance dev                    # Dry run
  $(basename "$0") --instance dev --apply            # Actually delete
EOF
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --instance)
                INSTANCE_NAME="$2"
                shift 2
                ;;
            --dry-run)
                DRY_RUN="true"
                shift
                ;;
            --apply)
                DRY_RUN="false"
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
}

validate_args() {
    if [[ -z "$INSTANCE_NAME" ]]; then
        log_error "Instance name is required"
        show_usage
        exit 1
    fi
    
    validate_instance_name "$INSTANCE_NAME"
}

get_stack_resources() {
    local stack_name="$1"
    
    if ! stack_exists "$stack_name"; then
        log_warn "Stack $stack_name does not exist"
        return 0
    fi
    
    aws cloudformation describe-stack-resources \
        --stack-name "$stack_name" \
        --query 'StackResources[].[LogicalResourceId,ResourceType,PhysicalResourceId]' \
        --output text
}

cleanup_retained_resources() {
    local stack_name="$1"
    
    log_info "Cleaning up retained resources for stack: $stack_name"
    
    local resources
    if ! is_dry_run; then
        resources=$(get_stack_resources "$stack_name")
    else
        resources="DynamoDBTable	AWS::DynamoDB::Table	chintan-${INSTANCE_NAME}-prod
ContentBucket	AWS::S3::Bucket	chintan-content-${INSTANCE_NAME}-123456789
ApiLogGroup	AWS::Logs::LogGroup	/aws/lambda/chintan-api-${INSTANCE_NAME}-prod"
    fi
    
    # Clean up DynamoDB table
    while IFS=$'\t' read -r logical_id resource_type physical_id; do
        case "$resource_type" in
            "AWS::DynamoDB::Table")
                if [[ "$physical_id" =~ ^chintan- ]]; then
                    log_info "Deleting DynamoDB table: $physical_id"
                    execute_cmd "aws dynamodb delete-table --table-name '$physical_id'"
                fi
                ;;
            "AWS::S3::Bucket")
                if [[ "$physical_id" =~ ^chintan-content- ]]; then
                    log_info "Emptying and deleting S3 bucket: $physical_id"
                    if ! is_dry_run; then
                        empty_s3_bucket "$physical_id"
                    fi
                    execute_cmd "aws s3 rb 's3://$physical_id' --force"
                fi
                ;;
            "AWS::Logs::LogGroup")
                if [[ "$physical_id" =~ /aws/lambda/chintan- ]]; then
                    log_info "Deleting CloudWatch log group: $physical_id"
                    execute_cmd "aws logs delete-log-group --log-group-name '$physical_id'"
                fi
                ;;
        esac
    done <<< "$resources"
}

delete_stack() {
    local stack_name="$1"
    
    if ! stack_exists "$stack_name"; then
        log_warn "Stack $stack_name does not exist, skipping deletion"
        return 0
    fi
    
    log_info "Deleting CloudFormation stack: $stack_name"
    execute_cmd "aws cloudformation delete-stack --stack-name '$stack_name'"
    
    if ! is_dry_run; then
        wait_for_stack "$stack_name" "stack-delete-complete"
        log_success "Stack deleted successfully"
    fi
}

cleanup_ssm_parameters() {
    log_info "Cleaning up SSM parameters for instance: $INSTANCE_NAME"
    
    local parameters=(
        "/chintan/$INSTANCE_NAME/groq_api_key"
        "/chintan/$INSTANCE_NAME/llm_api_key"
    )
    
    for param in "${parameters[@]}"; do
        if ! is_dry_run; then
            if aws ssm get-parameter --name "$param" &> /dev/null; then
                execute_cmd "aws ssm delete-parameter --name '$param'"
            else
                log_info "Parameter $param does not exist"
            fi
        else
            execute_cmd "aws ssm delete-parameter --name '$param'"
        fi
    done
}

main() {
    # Default to dry-run
    DRY_RUN="true"
    
    parse_args "$@"
    validate_args
    
    if is_dry_run; then
        log_warn "DRY-RUN MODE: No actual deletions will be performed"
        log_warn "Use --apply to execute the deletions"
    else
        log_warn "This will permanently delete all resources for instance: $INSTANCE_NAME"
        read -p "Are you sure you want to continue? (y/N): " -r
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            log_info "Cleanup cancelled"
            exit 0
        fi
    fi
    
    # Validate prerequisites
    check_aws_cli
    
    local stack_name="${CHINTAN_PREFIX}${INSTANCE_NAME}"
    
    log_info "Starting cleanup for Chintan instance: $INSTANCE_NAME"
    log_info "Stack: $stack_name"
    
    # Clean up retained resources first (before stack deletion)
    cleanup_retained_resources "$stack_name"
    
    # Delete SSM parameters
    cleanup_ssm_parameters
    
    # Delete the stack
    delete_stack "$stack_name"
    
    if is_dry_run; then
        log_info "Dry-run completed successfully"
        log_info "Run with --apply to execute these deletions"
    else
        log_success "Chintan instance cleanup completed successfully!"
        log_info "Instance '$INSTANCE_NAME' has been completely removed"
    fi
}

main "$@"