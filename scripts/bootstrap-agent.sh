#!/usr/bin/env bash
#
# One-time, administrator-only bootstrap: create the agent IAM principal, the
# policies it runs under, and the CloudTrail trail.
#
# ============================================================================
# READ THIS BEFORE RUNNING IT
# ============================================================================
#
# This is the one script in the repository that needs administrative AWS
# credentials, and the only one that touches IAM outside CloudFormation. It is
# run once, by a human, and then everything else runs as the role it creates.
# Nothing in CI calls it, nothing checks that what it deployed still matches the
# repository, and nothing here should be mistaken for a control on the account's
# owner: a permissions boundary constrains the principal it is attached to and
# cannot constrain root or an administrator.
#
# What it creates, all named chintan-*:
#
#   chintan-agent-boundary     managed policy — the permissions CEILING. Also
#                              named by infrastructure/bootstrap.yaml, so the
#                              deploy roles cannot be created until this exists.
#   chintan-agent-deny         managed policy — explicit denies, override any allow
#   chintan-agent-permissions  managed policy — what the agent may actually do
#   chintan-agent              IAM role, boundary attached, both policies attached,
#                              sessions capped at one hour
#   chintan-agent-cli          IAM user, boundary attached, may only assume that role
#   chintan-cloudtrail-*       S3 bucket for the audit trail; the agent is denied
#                              write and delete on it by bucket policy
#   chintan-trail              CloudTrail trail (multi-region, management events,
#                              log-file validation)
#
# The policy documents it applies are the four JSON files in
# infrastructure/agent-policies/ — boundary.json, deny.json, permissions.json and
# trust.json — with ACCOUNT_ID and REGION substituted at apply time. They are
# hand-maintained now: the generator that derived boundary.json and deny.json
# from a shared source, and the drift check that compared the deployed boundary
# against the repository, were removed with the rest of the guardrail tooling
# (docs/review-2026-09-03.md §4, §5.1 step 5). Edit the JSON directly, keep the
# boundary under IAM's 6144-character cap (this script measures it), and re-run
# with --apply; each policy is updated as a new default version rather than
# recreated, so there is no window in which it does not exist.
#
# WHY BOTH A ROLE AND A MINIMAL USER
#
# An account bootstrapped with root credentials cannot assume any role: "Roles
# may not be assumed by root accounts", whatever the trust policy says. Without
# IAM Identity Center there is therefore no path from root to bounded, short-lived
# credentials that does not pass through an IAM user, so there are two objects:
#
#   chintan-agent-cli   an IAM user whose ONLY permission is sts:AssumeRole on the
#                       role below, with the boundary also attached so it can never
#                       be granted more. Holds the one long-lived access key.
#   chintan-agent       the role that actually carries the project permissions.
#
# The gain over attaching the permissions straight to the user is modest — anyone
# holding the key can assume the role — but the credentials used for *work*
# expire within the hour, and the stored key on its own can only acquire a bounded
# session.
#
# The trail costs about $0.13/month in S3 PUTs for hourly digest files across
# every region (docs/review-2026-09-03/cost-alarms-logs.md item 2); deleting it or
# making it single-region is an owner decision, taken with the same credentials
# this script needs. scripts/teardown.sh deliberately leaves the trail, its bucket
# and the agent principal alone.
#
# Usage:
#   scripts/bootstrap-agent.sh                 # dry run — prints the plan, changes nothing
#   scripts/bootstrap-agent.sh --apply         # create or update
#   scripts/bootstrap-agent.sh --verify        # check what exists, change nothing
#   scripts/bootstrap-agent.sh --region <r>    # default: us-west-2
#
#   CHINTAN_KEY_OUT=/secure/path scripts/bootstrap-agent.sh --apply
#       also create an access key for the CLI user and write it to that path,
#       mode 600. The value is never printed. Omit to skip key creation.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

APPLY=0
VERIFY_ONLY=0
AS_JSON=""
REGION="us-west-2"

while [ $# -gt 0 ]; do
    case "$1" in
        --apply) APPLY=1 ;;
        --verify) VERIFY_ONLY=1 ;;
        --json) AS_JSON="--json" ;;
        --region)
            REGION="${2:?--region needs a value}"
            shift
            ;;
        -h | --help)
            sed -n '2,75p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

cd "$REPO_ROOT" || exit 1
export AWS_REGION="$REGION"
export AWS_PAGER=""

POLICY_DIR="infrastructure/agent-policies"
ROLE_NAME="chintan-agent"
BOUNDARY_NAME="chintan-agent-boundary"
DENY_NAME="chintan-agent-deny"
PERMS_NAME="chintan-agent-permissions"
TRAIL_NAME="chintan-trail"
USER_NAME="chintan-agent-cli"

# ---------------------------------------------------------------------------
# Preconditions
# ---------------------------------------------------------------------------

command -v aws >/dev/null 2>&1 || die "aws CLI not found"

ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text 2>/dev/null)" ||
    die "no AWS credentials. This script needs administrative credentials; it creates the constraints everything else runs under (§0.8 item 1)."
CALLER_ARN="$(aws sts get-caller-identity --query Arn --output text)"

TRAIL_BUCKET="chintan-cloudtrail-${ACCOUNT_ID}-${REGION}"

info "account:  $ACCOUNT_ID"
info "region:   $REGION"
info "caller:   $CALLER_ARN"

# Refuse to run as the very principal being created. Attaching a boundary to
# yourself mid-run leaves a half-configured principal that may not be able to
# finish or to undo.
if printf '%s' "$CALLER_ARN" | grep -q "$ROLE_NAME"; then
    die "running as $ROLE_NAME itself. This script must be run by an administrator, not by the agent principal it configures (I17)."
fi

# ---------------------------------------------------------------------------
# Render policies
# ---------------------------------------------------------------------------
#
# The documents live in infrastructure/agent-policies/ as reviewable files with
# ACCOUNT_ID and REGION placeholders, rather than inline heredocs here, so a
# change to one shows up as a diff rather than inside a 500-line script.

RENDER_DIR="$(mktemp -d)"
trap 'rm -rf "$RENDER_DIR"' EXIT

render() {
    local name="$1"
    [ -f "${POLICY_DIR}/${name}.json" ] || die "missing ${POLICY_DIR}/${name}.json"
    sed -e "s/ACCOUNT_ID/${ACCOUNT_ID}/g" -e "s/REGION/${REGION}/g" \
        "${POLICY_DIR}/${name}.json" >"${RENDER_DIR}/${name}.json"
    # An IAM managed policy is capped at 6144 characters and a permissions boundary
    # is exactly one managed policy, so exceeding it is a hard failure rather than a
    # style problem. Checked here because the error IAM returns for an oversized
    # document is not obviously about size.
    local size
    size="$(python3 -c "import json,sys;print(len(json.dumps(json.load(open(sys.argv[1])),separators=(',',':'))))" "${RENDER_DIR}/${name}.json")"
    if [ "$size" -gt 6144 ]; then
        die "${name}.json is ${size} characters; IAM caps a managed policy at 6144"
    fi
    printf '%s' "$size"
}

# Pre-flight every document through IAM Access Analyzer before creating anything.
#
# This is not ceremony. It caught two real errors in these very policies: the
# wildcard-service action form that §9.5's own snippet uses is invalid IAM syntax,
# and several service namespaces in the runaway-cost deny do not exist. It also
# reports where a tag condition is silently unsupported, which is G-047's failure
# mode and is otherwise invisible.
validate_policy() {
    local name="$1" ptype="${2:-IDENTITY_POLICY}"
    local errors
    # Backticks here are JMESPath literal syntax, not command substitution.
    # shellcheck disable=SC2016
    errors="$(aws accessanalyzer validate-policy \
        --policy-type "$ptype" \
        --policy-document "file://${RENDER_DIR}/${name}.json" \
        --query 'findings[?findingType==`ERROR`].findingDetails' --output json 2>/dev/null || echo '[]')"
    if [ "$(printf '%s' "$errors" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))')" != "0" ]; then
        err "${name}.json has validation errors:"
        printf '%s\n' "$errors" >&2
        return 1
    fi
    return 0
}

info "rendering and validating policy documents"
for name in boundary deny permissions; do
    size="$(render "$name")"
    if validate_policy "$name"; then
        ok "${name}.json — ${size} chars, no errors"
    else
        die "${name}.json failed validation; nothing has been created"
    fi
done
render trust >/dev/null
ok "trust.json rendered"

# ---------------------------------------------------------------------------
# Current state
# ---------------------------------------------------------------------------

policy_arn() { printf 'arn:aws:iam::%s:policy/%s' "$ACCOUNT_ID" "$1"; }

exists_policy() { aws iam get-policy --policy-arn "$(policy_arn "$1")" >/dev/null 2>&1; }
exists_role() { aws iam get-role --role-name "$1" >/dev/null 2>&1; }
exists_bucket() { aws s3api head-bucket --bucket "$1" >/dev/null 2>&1; }
exists_trail() { aws cloudtrail get-trail --name "$1" >/dev/null 2>&1; }

log ""
info "current state"
for p in "$BOUNDARY_NAME" "$DENY_NAME" "$PERMS_NAME"; do
    if exists_policy "$p"; then ok "policy $p exists"; else dim "  policy $p absent"; fi
done
if exists_role "$ROLE_NAME"; then ok "role $ROLE_NAME exists"; else dim "  role $ROLE_NAME absent"; fi
if exists_bucket "$TRAIL_BUCKET"; then ok "bucket $TRAIL_BUCKET exists"; else dim "  bucket $TRAIL_BUCKET absent"; fi
if exists_trail "$TRAIL_NAME"; then ok "trail $TRAIL_NAME exists"; else dim "  trail $TRAIL_NAME absent"; fi

if [ "$VERIFY_ONLY" = "1" ]; then
    log ""
    info "--verify: reporting only, nothing changed."
    exit 0
fi

# ---------------------------------------------------------------------------
# Plan
# ---------------------------------------------------------------------------

log ""
info "plan"
dim "  create managed policy   $BOUNDARY_NAME    (permissions ceiling)"
dim "  create managed policy   $DENY_NAME        (explicit denies)"
dim "  create managed policy   $PERMS_NAME       (grants)"
dim "  create role             $ROLE_NAME        (boundary + both policies attached)"
dim "  create bucket           $TRAIL_BUCKET     (audit log, agent cannot write)"
dim "  create trail            $TRAIL_NAME       (multi-region, management events)"
dim ""
dim "  nothing outside the chintan-* namespace is touched."
dim "  no access key is created; credentials come from sts:AssumeRole and expire."

if ! confirm_apply "$APPLY" "create the agent principal, its boundary, the deny policies, and CloudTrail"; then
    exit 0
fi

# ---------------------------------------------------------------------------
# Apply
# ---------------------------------------------------------------------------

# An array, not a string: each Key=..,Value=.. must reach the CLI as its own
# argument, so this needs word splitting — which is exactly what quoting a string
# would prevent and what leaving it unquoted would do by accident.
# The commas are part of each element's Key=..,Value=.. syntax, which is what the
# CLI expects; they are not element separators.
# shellcheck disable=SC2054
TAGS=(
    Key=Project,Value=chintan
    Key=Instance,Value=shared
    Key=Environment,Value=shared
    Key=ManagedBy,Value=iac
    Key=Owner,Value=vppillai
    Key=CostCenter,Value=chintan-shared
)

create_policy() {
    local name="$1" desc="$2"
    if exists_policy "$name"; then
        # Idempotent: add a new default version rather than failing. A guardrail
        # that cannot be updated is a guardrail that gets deleted and recreated,
        # which is a window in which it does not exist.
        info "updating $name (new default version)"
        # IAM allows 5 versions; prune the oldest non-default before adding.
        local versions
        # The backticks are JMESPath literal syntax, not command substitution, so
        # single quotes are required and shellcheck's SC2016 is a false positive here.
        # shellcheck disable=SC2016
        versions="$(aws iam list-policy-versions --policy-arn "$(policy_arn "$name")" \
            --query 'Versions[?IsDefaultVersion==`false`].VersionId' --output text)"
        local count
        count="$(printf '%s' "$versions" | wc -w)"
        if [ "$count" -ge 4 ]; then
            local oldest
            oldest="$(printf '%s' "$versions" | tr '\t' '\n' | tail -1)"
            aws iam delete-policy-version --policy-arn "$(policy_arn "$name")" --version-id "$oldest" >/dev/null
        fi
        aws iam create-policy-version \
            --policy-arn "$(policy_arn "$name")" \
            --policy-document "file://${RENDER_DIR}/${name#chintan-agent-}.json" \
            --set-as-default >/dev/null
        ok "$name updated"
    else
        info "creating $name"
        aws iam create-policy \
            --policy-name "$name" \
            --description "$desc" \
            --policy-document "file://${RENDER_DIR}/${name#chintan-agent-}.json" \
            --tags "${TAGS[@]}" >/dev/null
        ok "$name created"
    fi
}

create_policy "$BOUNDARY_NAME" "Chintan agent permissions boundary (ceiling). See infrastructure/agent-policies/boundary.json."
create_policy "$DENY_NAME" "Chintan agent explicit denies. Overrides any allow. See §9.5."
create_policy "$PERMS_NAME" "Chintan agent grants, scoped to chintan-* resources in the deploy region."

if exists_role "$ROLE_NAME"; then
    info "updating role $ROLE_NAME"
    aws iam update-assume-role-policy --role-name "$ROLE_NAME" \
        --policy-document "file://${RENDER_DIR}/trust.json" >/dev/null
    aws iam put-role-permissions-boundary --role-name "$ROLE_NAME" \
        --permissions-boundary "$(policy_arn "$BOUNDARY_NAME")" >/dev/null
else
    info "creating role $ROLE_NAME with its boundary attached"
    # The boundary is attached AT CREATION, in the same call. Creating the role
    # first and attaching afterwards leaves a window in which an unbounded
    # principal exists, and a window is all a mistake needs.
    aws iam create-role \
        --role-name "$ROLE_NAME" \
        --description "Chintan implementing agent. Operates under a permissions boundary; holds no key-creation rights and cannot read provider secrets (§9.4)." \
        --assume-role-policy-document "file://${RENDER_DIR}/trust.json" \
        --permissions-boundary "$(policy_arn "$BOUNDARY_NAME")" \
        --max-session-duration 3600 \
        --tags "${TAGS[@]}" >/dev/null
fi
aws iam attach-role-policy --role-name "$ROLE_NAME" --policy-arn "$(policy_arn "$PERMS_NAME")" >/dev/null
aws iam attach-role-policy --role-name "$ROLE_NAME" --policy-arn "$(policy_arn "$DENY_NAME")" >/dev/null
ok "role $ROLE_NAME configured"

# --- Minimal CLI user (G-064) ----------------------------------------------
# Root cannot assume a role, so this user is the only way to reach the bounded role.
# Its single permission is sts:AssumeRole on that role, and the boundary is attached
# as well, so even a mistaken policy attachment cannot lift it above the ceiling.

if aws iam get-user --user-name "$USER_NAME" >/dev/null 2>&1; then
    ok "user $USER_NAME already exists"
else
    info "creating minimal CLI user $USER_NAME"
    aws iam create-user --user-name "$USER_NAME" \
        --permissions-boundary "$(policy_arn "$BOUNDARY_NAME")" \
        --tags "${TAGS[@]}" >/dev/null
    ok "user created with the boundary attached"
fi

cat >"${RENDER_DIR}/assume-only.json" <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AssumeTheAgentRoleAndNothingElse",
      "Effect": "Allow",
      "Action": "sts:AssumeRole",
      "Resource": "arn:aws:iam::${ACCOUNT_ID}:role/${ROLE_NAME}"
    }
  ]
}
EOF
aws iam put-user-policy --user-name "$USER_NAME" \
    --policy-name assume-agent-role-only \
    --policy-document "file://${RENDER_DIR}/assume-only.json" >/dev/null
ok "user may assume $ROLE_NAME and do nothing else"

# The role's trust policy must name the user. Naming the account root principal is
# not sufficient in a useful way here: root itself cannot assume, and any other
# principal would need its own grant regardless.
cat >"${RENDER_DIR}/trust.json" <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "OnlyTheMinimalCliUserMayAssume",
      "Effect": "Allow",
      "Principal": { "AWS": "arn:aws:iam::${ACCOUNT_ID}:user/${USER_NAME}" },
      "Action": "sts:AssumeRole"
    }
  ]
}
EOF
attempt=1
until aws iam update-assume-role-policy --role-name "$ROLE_NAME" \
    --policy-document "file://${RENDER_DIR}/trust.json" >/dev/null 2>&1; do
    if [ "$attempt" -ge 12 ]; then
        die "could not update the trust policy on $ROLE_NAME after $attempt attempts"
    fi
    dim "  waiting for IAM to propagate $USER_NAME (attempt $attempt)"
    sleep 5
    attempt=$((attempt + 1))
done
ok "trust policy scoped to $USER_NAME alone"

# --- CloudTrail (§9.5) ------------------------------------------------------
# "CloudTrail enabled, delivering to a bucket the agent cannot write to or delete
# from. Every agent action is attributable to its principal."
#
# The first trail's management events are free; this account had none, so there is
# no second-trail charge (checked before creating).

# Create the bucket only if absent, but ALWAYS apply its configuration. Putting the
# policy inside the create branch made the script non-idempotent in a way that only
# showed on the second run: the bucket existed, so the whole block was skipped, and
# trail creation then failed with InsufficientS3BucketPolicyException — an error that
# points at the bucket policy rather than at the branch that skipped writing it.
# §11.3 asks for idempotency; every call below is safe to repeat.
if ! exists_bucket "$TRAIL_BUCKET"; then
    info "creating audit bucket $TRAIL_BUCKET"
    if [ "$REGION" = "us-east-1" ]; then
        # us-east-1 must NOT be given a LocationConstraint; the API rejects it.
        aws s3api create-bucket --bucket "$TRAIL_BUCKET" >/dev/null
    else
        aws s3api create-bucket --bucket "$TRAIL_BUCKET" \
            --create-bucket-configuration "LocationConstraint=$REGION" >/dev/null
    fi
    ok "bucket created"
else
    ok "bucket $TRAIL_BUCKET already exists"
fi

info "applying audit bucket configuration"
aws s3api put-public-access-block --bucket "$TRAIL_BUCKET" \
    --public-access-block-configuration \
    'BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true' >/dev/null
aws s3api put-bucket-encryption --bucket "$TRAIL_BUCKET" \
    --server-side-encryption-configuration \
    '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}' >/dev/null
# Trail objects are the audit record. Expire them at 400 days: long enough to
# investigate anything, short enough that storage stays negligible.
aws s3api put-bucket-lifecycle-configuration --bucket "$TRAIL_BUCKET" \
    --lifecycle-configuration '{"Rules":[{"ID":"ExpireTrailObjects","Status":"Enabled","Filter":{"Prefix":""},"Expiration":{"Days":400}}]}' >/dev/null
aws s3api put-bucket-tagging --bucket "$TRAIL_BUCKET" --tagging \
    'TagSet=[{Key=Project,Value=chintan},{Key=Instance,Value=shared},{Key=Environment,Value=shared},{Key=ManagedBy,Value=iac},{Key=Owner,Value=vppillai},{Key=CostCenter,Value=chintan-shared},{Key=Protected,Value=true}]' >/dev/null

cat >"${RENDER_DIR}/trail-bucket-policy.json" <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AWSCloudTrailAclCheck",
      "Effect": "Allow",
      "Principal": { "Service": "cloudtrail.amazonaws.com" },
      "Action": "s3:GetBucketAcl",
      "Resource": "arn:aws:s3:::${TRAIL_BUCKET}",
      "Condition": { "StringEquals": { "aws:SourceArn": "arn:aws:cloudtrail:${REGION}:${ACCOUNT_ID}:trail/${TRAIL_NAME}" } }
    },
    {
      "Sid": "AWSCloudTrailWrite",
      "Effect": "Allow",
      "Principal": { "Service": "cloudtrail.amazonaws.com" },
      "Action": "s3:PutObject",
      "Resource": "arn:aws:s3:::${TRAIL_BUCKET}/AWSLogs/${ACCOUNT_ID}/*",
      "Condition": {
        "StringEquals": {
          "s3:x-amz-acl": "bucket-owner-full-control",
          "aws:SourceArn": "arn:aws:cloudtrail:${REGION}:${ACCOUNT_ID}:trail/${TRAIL_NAME}"
        }
      }
    },
    {
      "Sid": "DenyTheAgentPrincipalEntirely",
      "Effect": "Deny",
      "Principal": { "AWS": "arn:aws:iam::${ACCOUNT_ID}:role/${ROLE_NAME}" },
      "Action": ["s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion", "s3:PutBucketPolicy", "s3:DeleteBucket"],
      "Resource": [
        "arn:aws:s3:::${TRAIL_BUCKET}",
        "arn:aws:s3:::${TRAIL_BUCKET}/*"
      ]
    }
  ]
}
EOF

# A bucket policy deny naming the agent principal, in addition to the identity policy
# deny. Two independent controls, because the identity policy is something the
# agent's own policies could in principle be changed to relax, whereas the bucket
# policy is not reachable from the agent's permissions at all.
#
# Retried, because IAM is eventually consistent and S3 resolves the principal ARN
# when the policy is applied. On a fresh account the role was created seconds
# earlier, so the first attempt fails with "MalformedPolicy: Invalid principal in
# policy" — a message that says nothing about timing and reads like a syntax error in
# a policy that is in fact correct.
attempt=1
until aws s3api put-bucket-policy --bucket "$TRAIL_BUCKET" \
    --policy "file://${RENDER_DIR}/trail-bucket-policy.json" >/dev/null 2>&1; do
    if [ "$attempt" -ge 12 ]; then
        die "could not apply the audit bucket policy after $attempt attempts. The bucket exists but the agent principal is NOT denied write access to it — re-run this script, which is idempotent, rather than leaving it in that state."
    fi
    dim "  waiting for IAM to propagate the agent role (attempt $attempt)"
    sleep 5
    attempt=$((attempt + 1))
done
ok "audit bucket configured; the agent principal is explicitly denied write and delete"

if ! exists_trail "$TRAIL_NAME"; then
    info "creating trail $TRAIL_NAME"
    aws cloudtrail create-trail \
        --name "$TRAIL_NAME" \
        --s3-bucket-name "$TRAIL_BUCKET" \
        --is-multi-region-trail \
        --include-global-service-events \
        --enable-log-file-validation \
        --tags-list "Key=Project,Value=chintan" "Key=Instance,Value=shared" "Key=ManagedBy,Value=iac" >/dev/null
    aws cloudtrail start-logging --name "$TRAIL_NAME" >/dev/null
    ok "trail created and logging started"
else
    aws cloudtrail start-logging --name "$TRAIL_NAME" >/dev/null 2>&1 || true
    ok "trail $TRAIL_NAME already exists; logging confirmed on"
fi

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------

log ""
ok "bootstrap complete"
log ""
if [ -n "${CHINTAN_KEY_OUT:-}" ]; then
    # An access key is the one piece of long-lived material this bootstrap creates.
    # It is written to the path given, mode 600, and **never printed** — a secret
    # echoed to a terminal is a secret in a scrollback buffer, a CI log, or an agent's
    # context (§9.7, G-050).
    if [ "$(aws iam list-access-keys --user-name "$USER_NAME" --query 'length(AccessKeyMetadata)' --output text)" != "0" ]; then
        warn "$USER_NAME already has an access key; not creating a second."
        warn "Rotate deliberately with: aws iam delete-access-key --user-name $USER_NAME --access-key-id <id>"
    else
        info "creating an access key for $USER_NAME"
        umask 077
        aws iam create-access-key --user-name "$USER_NAME" \
            --query 'AccessKey.{id:AccessKeyId,secret:SecretAccessKey}' --output json |
            python3 -c "
import json,sys,os
k=json.load(sys.stdin)
path=os.environ['CHINTAN_KEY_OUT']
with open(path,'w') as f:
    f.write(f\"export AWS_ACCESS_KEY_ID={k['id']}\n\")
    f.write(f\"export AWS_SECRET_ACCESS_KEY={k['secret']}\n\")
    f.write('export AWS_REGION=${REGION}\n')
os.chmod(path,0o600)
print('written')
" >/dev/null
        ok "access key written to \$CHINTAN_KEY_OUT (mode 600, value not printed)"
    fi
fi

log ""
info "assume the agent role with:"
dim "  # as ${USER_NAME}, not as an administrator — root cannot assume a role (G-064)"
dim "  aws sts assume-role \\"
dim "    --role-arn arn:aws:iam::${ACCOUNT_ID}:role/${ROLE_NAME} \\"
dim "    --role-session-name chintan-agent"
log ""
warn "everything from here runs as that role, not as an administrator; a permissions"
warn "boundary cannot constrain root, so work done as root is work done with no ceiling."
log ""
info "still to do by hand, and not this script's to do:"
dim "  - scripts/setup.sh --region ${REGION} --apply   (bootstrap stack, secrets, environments)"
dim "  - provider keys in SSM: /chintan/<instance>/groq_api_key and llm_api_key"
dim "  - activate the Project/Instance/Environment cost allocation tags in Billing"

finish_check "bootstrap-agent" "$AS_JSON"
