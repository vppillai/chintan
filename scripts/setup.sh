#!/bin/bash

# Setup script for Chintan - Deploy bootstrap and configure GitHub Actions
set -euo pipefail

# Source common utilities
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

# Default values
REGION=""
GITHUB_REPO=""

show_usage() {
    cat << EOF
Usage: $(basename "$0") --region REGION [--dry-run|--apply]

Deploy Chintan bootstrap infrastructure and configure GitHub Actions.

Required:
  --region REGION    AWS region for deployment (e.g., us-west-2)

Options:
  --dry-run         Show what would be done without making changes (default)
  --apply           Actually perform the operations

Examples:
  $(basename "$0") --region us-west-2                # Dry run
  $(basename "$0") --region us-west-2 --apply        # Actually deploy
EOF
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --region)
                REGION="$2"
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
    if [[ -z "$REGION" ]]; then
        log_error "Region is required"
        show_usage
        exit 1
    fi
}

deploy_bootstrap() {
    local stack_name="$CHINTAN_BOOTSTRAP_STACK"
    local template_path="$SCRIPT_DIR/../infrastructure/bootstrap.yaml"
    
    if [[ ! -f "$template_path" ]]; then
        log_error "Bootstrap template not found: $template_path"
        exit 1
    fi
    
    log_info "Deploying bootstrap stack: $stack_name"
    
    local repo_info
    repo_info=$(get_github_repo_info)
    local repo_owner="${repo_info%/*}"
    local repo_name="${repo_info#*/}"
    
    local deploy_cmd="aws cloudformation deploy \
        --template-file '$template_path' \
        --stack-name '$stack_name' \
        --region '$REGION' \
        --parameter-overrides \
            GitHubOrg='$repo_owner' \
            GitHubRepo='$repo_name' \
        --capabilities CAPABILITY_NAMED_IAM \
        --tags \
            Application=Chintan \
            Project=chintan"
    
    execute_cmd "$deploy_cmd"
    
    if ! is_dry_run; then
        wait_for_stack "$stack_name"
        log_success "Bootstrap stack deployed successfully"
    fi
}

set_github_secrets() {
    log_info "Setting GitHub repository secrets"
    
    local account_id
    account_id=$(get_aws_account_id)
    
    local role_arn="arn:aws:iam::${account_id}:role/chintan-github-actions"
    
    execute_cmd "gh secret set AWS_ACCOUNT_ID --body '$account_id'"
    execute_cmd "gh secret set AWS_ROLE_ARN --body '$role_arn'"
    execute_cmd "gh secret set AWS_REGION --body '$REGION'"
    
    if ! is_dry_run; then
        log_success "GitHub secrets configured"
    fi
}

create_production_environment() {
    log_info "Ensuring production GitHub environment exists"
    
    # Check if environment already exists
    if ! is_dry_run; then
        if gh api repos/:owner/:repo/environments/production &> /dev/null; then
            log_info "Production environment already exists"
            return 0
        fi
    fi
    
    execute_cmd "gh api repos/:owner/:repo/environments/production --method PUT --field wait_timer=0"
    
    if ! is_dry_run; then
        log_success "Production environment created"
    fi
}

enable_github_pages() {
    log_info "Enabling GitHub Pages"
    
    local pages_config='{
        "source": {
            "branch": "main",
            "path": "/"
        }
    }'
    
    execute_cmd "gh api repos/:owner/:repo/pages --method POST --input - <<< '$pages_config'" 
    
    if ! is_dry_run; then
        log_success "GitHub Pages enabled"
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
    
    # Check if we're in a git repository
    if ! git rev-parse --git-dir &> /dev/null; then
        log_error "Must be run from within a git repository"
        exit 1
    fi
    
    log_info "Starting Chintan setup for region: $REGION"
    
    # Deploy infrastructure
    deploy_bootstrap
    
    # Configure GitHub integration
    set_github_secrets
    create_production_environment
    enable_github_pages
    
    if is_dry_run; then
        log_info "Dry-run completed successfully"
        log_info "Run with --apply to execute these operations"
    else
        log_success "Chintan setup completed successfully!"
        log_info "Bootstrap stack: $CHINTAN_BOOTSTRAP_STACK"
        log_info "Region: $REGION"
        log_info "GitHub Actions configured with AWS integration"
    fi
}

main "$@"