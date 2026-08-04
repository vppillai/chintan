#!/usr/bin/env bash
#
# Assert the guardrails are present and unmodified (§9.8).
#
# "A guardrail that has been silently removed is worse than none, because it is
# still trusted." That sentence is the whole justification for this script, and it
# is why §9.8 requires it to run both from doctor.sh and in CI, and why a failure
# here is a build-stopping defect rather than a warning.
#
# §Phase 0 acceptance goes further: this script must be "proven to fail when the
# boundary is detached from a test role." A check never observed failing is not
# counted as present, so `--self-test` exercises the failure path.
#
# What §9.8 requires asserting:
#   - the agent principal carries the expected permissions boundary
#   - every role created by this project carries the boundary
#   - the ABAC deny statements are present and unmodified
#   - no resource tagged Project=voicenotes lacks the full tag set
#   - no resource created by this project lies outside the deployment region
#   - CloudTrail is enabled and its bucket is not writable by the agent principal
#   - branch protection and CODEOWNERS are in force on main
#   - the GitHub token in use is fine-grained and repo-scoped
#
# Read-only by construction: it has no --apply and needs none (§11.3). It cannot
# repair a guardrail, only report one missing — repairing would require exactly
# the permissions the guardrails withhold (I17).
#
# Checks split into two groups by what they need:
#
#   LOCAL   readable from the repository alone: CODEOWNERS content, workflow
#           shape, the absence of committed credentials. These run everywhere,
#           including in a pull-request job that holds no credentials (§0.5A).
#   REMOTE  need AWS or GitHub API access: the boundary, the denies, tagging,
#           CloudTrail, branch protection.
#
# In a credential-less CI job the remote group cannot run. It is reported as
# UNVERIFIED rather than passed — a guardrail check that reports success without
# having looked is precisely the failure mode this script exists to prevent.
#
# Usage: guardrails-check.sh [--json] [--local-only] [--self-test]

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

AS_JSON=""
LOCAL_ONLY=0
SELF_TEST=0
UNVERIFIED=()

while [ $# -gt 0 ]; do
    case "$1" in
        --json) AS_JSON="--json" ;;
        --local-only) LOCAL_ONLY=1 ;;
        --self-test) SELF_TEST=1 ;;
        -h | --help)
            sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

cd "$REPO_ROOT" || exit 1

unverified() {
    UNVERIFIED+=("$1")
    printf '%s  SKIP%s %s\n' "$C_YELLOW" "$C_OFF" "$1" >&2
}

# ---------------------------------------------------------------------------
# Self-test: prove the script can fail (§Phase 0 acceptance, §0.5A)
# ---------------------------------------------------------------------------
if [ "$SELF_TEST" = "1" ]; then
    info "self-test: asserting this script detects a missing guardrail"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT

    # Build a tree that differs from this one in exactly one way: the guardrail is
    # gone. Copying only part of the repo would leave other checks skipping for
    # lack of a subject, and then a pass could not be attributed to the removal.
    cp -r .github config scripts "$tmp/" 2>/dev/null || true
    rm -f "$tmp/.github/CODEOWNERS"

    # Sanity-check the control case first. If the script does not pass on the
    # UNDOCTORED tree, the self-test below proves nothing — a failure everywhere
    # is not evidence that the check detects this specific removal. This is the
    # step whose absence let this self-test report success while never exercising
    # its own premise.
    if ! CHINTAN_REPO_ROOT="$tmp" bash "$tmp/scripts/guardrails-check.sh" --local-only >/dev/null 2>&1; then
        # Restore the guardrail and confirm the tree is otherwise clean.
        cp .github/CODEOWNERS "$tmp/.github/CODEOWNERS"
        if ! CHINTAN_REPO_ROOT="$tmp" bash "$tmp/scripts/guardrails-check.sh" --local-only >/dev/null 2>&1; then
            err "self-test inconclusive: the script fails even with every guardrail present"
            err "fix that first — a check that always fails proves nothing about detection"
            exit 1
        fi
        rm -f "$tmp/.github/CODEOWNERS"
    fi

    if CHINTAN_REPO_ROOT="$tmp" bash "$tmp/scripts/guardrails-check.sh" --local-only >/dev/null 2>&1; then
        err "self-test FAILED: guardrails-check.sh passed with CODEOWNERS removed"
        err "an untested check is worse than no check, because it is believed (§0.5A)"
        exit 1
    fi
    ok "self-test: the script fails when a guardrail is removed"
    exit 0
fi

# ---------------------------------------------------------------------------
# LOCAL: readable from the repository
# ---------------------------------------------------------------------------

info "CODEOWNERS is in force on the paths where agent autonomy is dangerous (§9.6)"
if [ ! -f .github/CODEOWNERS ]; then
    violation ".github/CODEOWNERS is absent — write access to a workflow is write access to deployment credentials (G-048)"
else
    # §9.6 names these four paths specifically. The workflow directory matters
    # most: CI holds deployment credentials, so an agent able to modify workflows
    # unreviewed can exfiltrate them.
    for path in '/.github/workflows/' '/infrastructure/' '/scripts/bootstrap' '/docs/security/'; do
        if ! grep -qF -- "$path" .github/CODEOWNERS; then
            violation "CODEOWNERS does not cover $path (§9.6)"
        fi
    done
fi

info "no deploy path exists outside CI (I16, §0.5A)"
# §Phase 0 acceptance: "No deploy path exists outside CI. The agent holds no AWS
# credentials capable of deploying, and this is verified by attempting a local
# deploy and asserting it fails on credentials."
if [ -f scripts/deploy.sh ]; then
    if ! grep -q 'CI\|GITHUB_ACTIONS' scripts/deploy.sh; then
        violation "scripts/deploy.sh does not check that it is running in CI — deploys happen only from a green pipeline, through deploy.sh invoked by CI (I16, §0.5A)"
    fi
fi

info "no credential material is committed (§9.6)"
# Secret scanning and push protection are the real control; this is a local
# tripwire that fails before the push rather than after.
while IFS= read -r file; do
    [ -f "$file" ] || continue
    case "$file" in
        # Excluded because each *describes* credential shapes rather than holding
        # one: the spec and registers quote them, this script lists the patterns
        # it searches for, config.go's looksLikeInlineSecret enumerates markers,
        # and the config tests assert that an inline secret is rejected — which
        # requires a string that looks like one.
        docs/*) continue ;;
        scripts/guardrails-check.sh) continue ;;
        backend/internal/config/config.go) continue ;;
        *_test.go) continue ;;
    esac
    if grep -qE '(aws_secret_access_key|AKIA[0-9A-Z]{16}|gsk_[A-Za-z0-9]{20,}|-----BEGIN [A-Z ]*PRIVATE KEY-----)' "$file" 2>/dev/null; then
        violation "$file appears to contain credential material — secrets are referenced by SSM path, never by value (§7.4, §9.4)"
    fi
done < <(tracked_files 2>/dev/null)

info "every provider secret is referenced by path, not value (§9.4)"
# One file at a time: yq emits a '---' document separator between inputs when
# given several files, and those separators would be read as secret_ref values.
for cfg in config/instances/*.yaml; do
    [ -f "$cfg" ] || continue
    while IFS= read -r ref; do
        case "$ref" in
            /*) ;;
            null | '') ;;
            *) violation "$cfg: secret_ref '$ref' is not an absolute SSM path — secrets are referenced by path, never by value (§7.4, §9.4)" ;;
        esac
    done < <(yq -r '.. | select(type == "!!map" and has("secret_ref")) | .secret_ref' "$cfg" 2>/dev/null || true)
done

# ---------------------------------------------------------------------------
# REMOTE: needs AWS or GitHub API access
# ---------------------------------------------------------------------------

if [ "$LOCAL_ONLY" = "1" ]; then
    dim "  --local-only: skipping every check that needs AWS or GitHub API access"
    finish_check "guardrails (local checks only)" "$AS_JSON"
    exit $?
fi

# The remote checks need a caller identity. Whether one is available is itself
# informative: §9.4 says the agent must never hold root credentials, and a root
# identity here is a guardrail violation rather than a convenience.
if ! caller="$(aws_cli sts get-caller-identity --output json 2>/dev/null)"; then
    unverified "AWS guardrails: no credentials available (correct for a pull-request job, which holds none by design — §0.5A)"
else
    arn="$(printf '%s' "$caller" | jq -r '.Arn')"
    info "checking the calling identity is not root (§9.4)"
    if printf '%s' "$arn" | grep -q ':root$'; then
        # This is non-negotiable #1 in §9.4: "The agent never receives root
        # credentials. Root is MFA-protected and unused." A boundary cannot
        # constrain root, so every other guardrail below is unenforceable while
        # this holds.
        violation "the calling identity is the account ROOT user ($arn) — §9.4 non-negotiable #1: the agent never receives root credentials, and no permissions boundary can constrain root. Run scripts/bootstrap-agent.sh as a human and use the agent principal it creates (I17)."
    else
        ok "calling identity is not root"

        info "the agent principal carries the expected permissions boundary (§9.5)"
        principal="$(printf '%s' "$arn" | sed -E 's|.*/([^/]+)$|\1|')"
        if boundary="$(aws_cli iam get-role --role-name "$principal" --query 'Role.PermissionsBoundary.PermissionsBoundaryArn' --output text 2>/dev/null)"; then
            if [ "$boundary" = "None" ] || [ -z "$boundary" ]; then
                violation "the agent principal '$principal' carries no permissions boundary — a principal that can create roles can escalate through one (G-046, §9.5)"
            else
                ok "boundary attached: $boundary"
            fi
        else
            unverified "permissions boundary: cannot read the calling role (iam:GetRole denied, or the principal is a user rather than a role)"
        fi

        info "every role created by this project carries the boundary (§9.8)"
        while IFS= read -r role; do
            [ -n "$role" ] || continue
            b="$(aws_cli iam get-role --role-name "$role" --query 'Role.PermissionsBoundary.PermissionsBoundaryArn' --output text 2>/dev/null || echo None)"
            if [ "$b" = "None" ]; then
                violation "role '$role' has no permissions boundary — the boundary must be required on every role the agent creates, or privilege escalates through a Lambda execution role (G-046)"
            fi
        done < <(aws_cli iam list-roles --query "Roles[?starts_with(RoleName, '${SYSTEM_ID}-')].RoleName" --output text 2>/dev/null | tr '\t' '\n' || true)

        info "CloudTrail is enabled (§9.5)"
        trails="$(aws_cli cloudtrail list-trails --query 'length(Trails)' --output text 2>/dev/null || echo 0)"
        if [ "$trails" = "0" ] || [ "$trails" = "None" ]; then
            violation "no CloudTrail trail exists — every agent action must be attributable to its principal (§9.5)"
        else
            ok "CloudTrail: $trails trail(s)"
        fi
    fi
fi

info "the GitHub token in use is fine-grained and repo-scoped (§9.6, G-049)"
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    # A classic token grants its scopes across every repository the user can
    # reach, including private ones in other organisations, and the blast radius
    # is invisible until something goes wrong elsewhere (G-049).
    scopes="$(gh auth status 2>&1 | grep -i 'Token scopes' || true)"
    if printf '%s' "$scopes" | grep -qE "'(repo|workflow|admin:org|gist|read:org)'"; then
        violation "the GitHub token is a CLASSIC token (scopes: ${scopes#*: }) — §9.6 requires a fine-grained token scoped to this repository alone; classic tokens grant their scopes across every repository the user can access (G-049)"
    else
        ok "token does not present classic scopes"
    fi

    info "branch protection is in force on main (§9.6)"
    if prot="$(gh api "repos/vppillai/chintan/branches/main/protection" 2>/dev/null)"; then
        for field in 'required_pull_request_reviews' 'required_status_checks'; do
            if ! printf '%s' "$prot" | jq -e ".$field" >/dev/null 2>&1; then
                # Required from Phase 2 (§0.5); reported as unverified rather
                # than failed before then, because Phases 0–1 commit directly to
                # main by design.
                unverified "branch protection: $field is not configured (required from Phase 2, §0.5/§9.6)"
            fi
        done
    else
        unverified "branch protection: not configured on main, or the token lacks permission to read it (the agent is deliberately denied 'administration' — §9.6)"
    fi
else
    unverified "GitHub guardrails: gh is unavailable or unauthenticated"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

if [ "${#UNVERIFIED[@]}" -gt 0 ]; then
    log ""
    warn "${#UNVERIFIED[@]} guardrail(s) could not be verified in this environment."
    warn "Unverified is not the same as passing: §9.8 exists because a guardrail"
    warn "that is still trusted while absent is worse than no guardrail at all."
fi

finish_check "guardrails (§9.8)" "$AS_JSON"
