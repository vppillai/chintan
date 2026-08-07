#!/bin/bash

# Bootstrap script for Chintan - Deploy individual instance infrastructure
set -euo pipefail

# Source common utilities
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

# Default values
INSTANCE_NAME=""
REGION=""
ALLOWED_ORIGIN=""
ENVIRONMENT="prod"

show_usage() {
    cat << EOF
Usage: $(basename "$0") --instance INSTANCE --region REGION --origin ORIGIN [OPTIONS]

Deploy Chintan instance infrastructure (Lambda, API Gateway, DynamoDB, etc).

Required:
  --instance INSTANCE   Instance name (lowercase, alphanumeric with hyphens)
  --region REGION       AWS region for deployment
  --origin ORIGIN       CORS allowed origin (e.g., https://user.github.io)

Options:
  --environment ENV     Environment name (prod, staging, dev) [default: prod]
  --dry-run            Show what would be done without making changes (default)
  --apply              Actually perform the operations

Examples:
  $(basename "$0") --instance dev --region us-west-2 --origin https://user.github.io/repo
  $(basename "$0") --instance dev --region us-west-2 --origin https://user.github.io/repo --apply
EOF
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --instance)
                INSTANCE_NAME="$2"
                shift 2
                ;;
            --region)
                REGION="$2"
                shift 2
                ;;
            --origin)
                ALLOWED_ORIGIN="$2"
                shift 2
                ;;
            --environment)
                ENVIRONMENT="$2"
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
    
    if [[ -z "$REGION" ]]; then
        log_error "Region is required"
        show_usage
        exit 1
    fi
    
    if [[ -z "$ALLOWED_ORIGIN" ]]; then
        log_error "Allowed origin is required"
        show_usage
        exit 1
    fi
    
    validate_instance_name "$INSTANCE_NAME"
    
    if [[ ! "$ENVIRONMENT" =~ ^(prod|staging|dev)$ ]]; then
        log_error "Environment must be one of: prod, staging, dev"
        exit 1
    fi
}

check_bootstrap_stack() {
    if ! stack_exists "$CHINTAN_BOOTSTRAP_STACK"; then
        log_error "Bootstrap stack '$CHINTAN_BOOTSTRAP_STACK' not found"
        log_error "Please run setup.sh first to deploy the bootstrap infrastructure"
        exit 1
    fi
}

build_lambda() {
    log_info "Building Lambda function"
    
    local build_script="$SCRIPT_DIR/build-lambda.sh"
    if [[ ! -f "$build_script" ]]; then
        log_error "Lambda build script not found: $build_script"
        exit 1
    fi
    
    execute_cmd "$build_script"
}

upload_lambda() {
    log_info "Uploading Lambda function to S3"
    
    local lambda_zip="$SCRIPT_DIR/../backend/lambda-function.zip"
    if [[ ! -f "$lambda_zip" ]] && ! is_dry_run; then
        log_error "Lambda zip file not found: $lambda_zip"
        log_error "Please build the Lambda function first"
        exit 1
    fi
    
    # Get artifact bucket from bootstrap stack
    local bucket_name
    if ! is_dry_run; then
        bucket_name=$(aws cloudformation describe-stacks \
            --stack-name "$CHINTAN_BOOTSTRAP_STACK" \
            --region "$REGION" \
            --query "Stacks[0].Outputs[?OutputKey=='LambdaArtifactBucketName'].OutputValue" \
            --output text)
        
        if [[ -z "$bucket_name" ]]; then
            log_error "Could not get Lambda artifact bucket from bootstrap stack"
            exit 1
        fi
    else
        bucket_name="chintan-lambda-ACCOUNT-$REGION"
    fi
    
    local s3_key="${INSTANCE_NAME}/lambda-function.zip"
    
    execute_cmd "aws s3 cp '$lambda_zip' 's3://$bucket_name/$s3_key' --region '$REGION'"
    
    echo "$bucket_name:$s3_key"
}

deploy_instance_stack() {
    local lambda_location="$1"
    local bucket_name="${lambda_location%:*}"
    local s3_key="${lambda_location#*:}"
    
    local stack_name="${CHINTAN_PREFIX}${INSTANCE_NAME}"
    local template_path="$SCRIPT_DIR/../infrastructure/template.yaml"
    
    if [[ ! -f "$template_path" ]]; then
        log_error "Instance template not found: $template_path"
        exit 1
    fi
    
    log_info "Deploying instance stack: $stack_name"
    
    local repo_info
    repo_info=$(get_github_repo_info)
    local repo_name="${repo_info#*/}"
    
    # Extract pages host from allowed origin
    local pages_host
    pages_host=$(echo "$ALLOWED_ORIGIN" | sed 's|^https\?://||' | sed 's|/.*$||')
    
    local deploy_cmd="aws cloudformation deploy \
        --template-file '$template_path' \
        --stack-name '$stack_name' \
        --region '$REGION' \
        --parameter-overrides \
            InstanceName='$INSTANCE_NAME' \
            AllowedOrigin='$ALLOWED_ORIGIN' \
            LambdaCodeBucket='$bucket_name' \
            LambdaCodeKey='$s3_key' \
            Environment='$ENVIRONMENT' \
            PagesHost='$pages_host' \
            RepoName='$repo_name' \
        --capabilities CAPABILITY_NAMED_IAM \
        --tags \
            Application=Chintan \
            Project=chintan \
            Instance='$INSTANCE_NAME'"
    
    execute_cmd "$deploy_cmd"
    
    if ! is_dry_run; then
        wait_for_stack "$stack_name"
        log_success "Instance stack deployed successfully"
        
        # Get and display outputs
        local api_endpoint
        api_endpoint=$(aws cloudformation describe-stacks \
            --stack-name "$stack_name" \
            --region "$REGION" \
            --query "Stacks[0].Outputs[?OutputKey=='ApiEndpoint'].OutputValue" \
            --output text)
        
        log_success "API Endpoint: $api_endpoint"
    fi
}

main() {
    # Default to dry-run
    DRY_RUN="true"
    
    parse_args "$@"
    validate_args
    
    if is_dry_run; then
        log_warn "DRY-RUN MODE: No actual changes will be made"
        log_warn "Use --apply to execute the operations"
    fi
    
    # Validate prerequisites
    check_aws_cli
    check_gh_cli
    check_bootstrap_stack
    
    log_info "Starting Chintan instance bootstrap"
    log_info "Instance: $INSTANCE_NAME"
    log_info "Region: $REGION"
    log_info "Environment: $ENVIRONMENT"
    log_info "Allowed Origin: $ALLOWED_ORIGIN"
    
    # Build and upload Lambda
    build_lambda
    local lambda_location
    lambda_location=$(upload_lambda)
    
    # Deploy instance infrastructure
    deploy_instance_stack "$lambda_location"
    
    if is_dry_run; then
        log_info "Dry-run completed successfully"
        log_info "Run with --apply to execute these operations"
    else
        log_success "Chintan instance bootstrap completed successfully!"
        log_info "Stack name: ${CHINTAN_PREFIX}${INSTANCE_NAME}"
        log_info "Remember to set the required SSM parameters:"
        log_info "  /chintan/$INSTANCE_NAME/groq_api_key"
        log_info "  /chintan/$INSTANCE_NAME/llm_api_key"
    fi
}

main "$@"