#!/bin/bash

# Common utilities for Chintan ops scripts
set -euo pipefail

# Colors for output
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[1;33m'
readonly BLUE='\033[0;34m'
readonly NC='\033[0m' # No Color

# Chintan resource prefix
readonly CHINTAN_PREFIX="chintan-"
readonly CHINTAN_BOOTSTRAP_STACK="${CHINTAN_PREFIX}bootstrap"

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $*" >&2
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*" >&2
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*" >&2
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*" >&2
}

# Check if we're in dry-run mode
is_dry_run() {
    [[ "${DRY_RUN:-}" == "true" ]]
}

# Execute command with dry-run support
execute_cmd() {
    local cmd="$1"
    if is_dry_run; then
        log_info "[DRY-RUN] Would execute: $cmd"
    else
        log_info "Executing: $cmd"
        eval "$cmd"
    fi
}

# Check if AWS CLI is available and configured
check_aws_cli() {
    if ! command -v aws &> /dev/null; then
        log_error "AWS CLI is not installed. Please install it first."
        exit 1
    fi
    
    if ! aws sts get-caller-identity &> /dev/null; then
        log_error "AWS CLI is not configured or credentials are invalid."
        exit 1
    fi
}

# Get AWS account ID
get_aws_account_id() {
    aws sts get-caller-identity --query Account --output text
}

# Check if GitHub CLI is available
check_gh_cli() {
    if ! command -v gh &> /dev/null; then
        log_error "GitHub CLI is not installed. Please install it first."
        exit 1
    fi
    
    if ! gh auth status &> /dev/null; then
        log_error "GitHub CLI is not authenticated. Please run 'gh auth login' first."
        exit 1
    fi
}

# Get GitHub repository info
get_github_repo_info() {
    if ! gh repo view --json name,owner &> /dev/null; then
        log_error "Not in a GitHub repository or repository not found."
        exit 1
    fi
    
    local repo_json
    repo_json=$(gh repo view --json name,owner)
    local repo_name
    repo_name=$(echo "$repo_json" | jq -r '.name')
    local repo_owner
    repo_owner=$(echo "$repo_json" | jq -r '.owner.login')
    
    echo "${repo_owner}/${repo_name}"
}

# Check if CloudFormation stack exists
stack_exists() {
    local stack_name="$1"
    aws cloudformation describe-stacks --stack-name "$stack_name" &> /dev/null
}

# Wait for CloudFormation stack operation to complete
wait_for_stack() {
    local stack_name="$1"
    local operation="${2:-}"
    
    log_info "Waiting for stack $stack_name to complete..."
    
    if [[ -n "$operation" ]]; then
        aws cloudformation wait "$operation" --stack-name "$stack_name"
    else
        # Determine the operation based on stack status
        local status
        status=$(aws cloudformation describe-stacks --stack-name "$stack_name" --query 'Stacks[0].StackStatus' --output text 2>/dev/null || echo "NOT_EXISTS")
        
        case "$status" in
            *CREATE_IN_PROGRESS*)
                aws cloudformation wait stack-create-complete --stack-name "$stack_name"
                ;;
            *UPDATE_IN_PROGRESS*)
                aws cloudformation wait stack-update-complete --stack-name "$stack_name"
                ;;
            *DELETE_IN_PROGRESS*)
                aws cloudformation wait stack-delete-complete --stack-name "$stack_name"
                ;;
        esac
    fi
}

# List all Chintan stacks
list_chintan_stacks() {
    local region="${1:-}"
    local aws_cmd="aws cloudformation list-stacks --stack-status-filter CREATE_COMPLETE UPDATE_COMPLETE"
    
    if [[ -n "$region" ]]; then
        aws_cmd="$aws_cmd --region $region"
    fi
    
    $aws_cmd --query "StackSummaries[?starts_with(StackName, \`$CHINTAN_PREFIX\`)].{Name:StackName,Status:StackStatus,Created:CreationTime}" --output table
}

# Delete S3 bucket versions and contents
empty_s3_bucket() {
    local bucket_name="$1"
    
    if ! aws s3api head-bucket --bucket "$bucket_name" &> /dev/null; then
        log_warn "Bucket $bucket_name does not exist or is not accessible"
        return 0
    fi
    
    log_info "Emptying S3 bucket: $bucket_name"
    
    # Delete all versions and delete markers
    execute_cmd "aws s3api delete-objects --bucket '$bucket_name' --delete \"\$(aws s3api list-object-versions --bucket '$bucket_name' --output json | jq '{Objects: [.Versions[]?, .DeleteMarkers[]?] | map({Key:.Key, VersionId:.VersionId}) | map(select(.VersionId != null)), Quiet: false}')\""
    
    # Delete remaining objects (if any)
    execute_cmd "aws s3 rm s3://$bucket_name --recursive"
}

# Validate instance name
validate_instance_name() {
    local name="$1"
    
    if [[ ! "$name" =~ ^[a-z0-9-]+$ ]]; then
        log_error "Instance name must contain only lowercase letters, numbers, and hyphens"
        exit 1
    fi
    
    if [[ ${#name} -gt 32 ]]; then
        log_error "Instance name must be 32 characters or less"
        exit 1
    fi
}

# Parse command line arguments for dry-run
parse_dry_run_args() {
    DRY_RUN="true"  # Default to dry-run
    
    for arg in "$@"; do
        case "$arg" in
            --apply)
                DRY_RUN="false"
                ;;
            --dry-run)
                DRY_RUN="true"
                ;;
        esac
    done
    
    export DRY_RUN
}

# Show usage for dry-run scripts
show_dry_run_usage() {
    local script_name="$1"
    shift
    echo "Usage: $script_name $* [--dry-run|--apply]"
    echo
    echo "Options:"
    echo "  --dry-run    Show what would be done without making changes (default)"
    echo "  --apply      Actually perform the operations"
    echo
}