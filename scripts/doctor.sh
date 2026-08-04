#!/usr/bin/env bash
#
# Preflight validation of every prerequisite before first deploy (§Phase 0).
#
# Passbook needed an AWS account and GitHub. This needs AWS, GitHub Pages, a
# custom domain, a Groq key, a MiniMax key, and optionally a Telegram bot — which
# is why §10.2 lists a preflight script as worth more here than it was there.
#
# §Phase 0 acceptance: "doctor.sh on a fresh clone with nothing configured
# produces a complete, actionable list of what is missing." So it never stops at
# the first problem: it reports everything, with what to do about each.
#
# Read-only. It has one side effect by design — reporting whether the GitHub OIDC
# provider already exists so that CreateOIDCProvider can be set correctly (G-016)
# — but it changes nothing itself.
#
# Usage: doctor.sh [--json] [--instance <name>]
# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

AS_JSON=""
INSTANCE="${CHINTAN_INSTANCE:-dev}"
while [ $# -gt 0 ]; do
    case "$1" in
        --json) AS_JSON="--json" ;;
        --instance)
            INSTANCE="${2:?}"
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

CONFIG="config/instances/${INSTANCE}.yaml"
[ -f "$CONFIG" ] || die "no config for instance '$INSTANCE' at $CONFIG"

# --- Toolchain -------------------------------------------------------------
# Reported rather than checked in depth: the container pins every tool by version
# and sha256, so if this is running at all the toolchain is correct by
# construction. What is worth reporting is whether it IS the container.
info "toolchain"
if [ "${CHINTAN_IN_CONTAINER:-}" = "1" ]; then
    ok "running inside the toolchain container"
else
    violation "not running inside the toolchain container — every check must run in the same image as CI (§0.5A). Use 'make doctor'."
fi

# --- Config ----------------------------------------------------------------
info "configuration (§7.4)"
if (cd backend && go run ./cmd/chintanctl config validate "../$CONFIG" >/dev/null 2>&1); then
    ok "$CONFIG validates"
else
    violation "$CONFIG does not validate — run 'make check-config' for the full list"
fi

# --- Human prerequisites (§0.8) --------------------------------------------
# Six things require a human and block the start. The agent cannot do them and
# should not attempt to, so doctor.sh reports their state rather than acting.
info "human prerequisites (§0.8)"

if ! aws_cli sts get-caller-identity >/dev/null 2>&1; then
    violation "§0.8/1: no AWS credentials. A human must run scripts/bootstrap-agent.sh once to create the agent principal, its permissions boundary, the ABAC and deny policies, and CloudTrail. The agent cannot create its own credentials by design (I17)."
else
    arn="$(aws_cli sts get-caller-identity --query Arn --output text 2>/dev/null)"
    if printf '%s' "$arn" | grep -q ':root$'; then
        violation "§9.4 non-negotiable #1: the available credentials are the account ROOT user. No permissions boundary can constrain root, so every guardrail in §9.5 is unenforceable. Run scripts/bootstrap-agent.sh and use the agent principal it creates."
    else
        ok "AWS identity: $arn"
    fi

    # G-016: the GitHub OIDC provider is account-global and singleton. Passbook's
    # bootstrap already created one in this account, so a second declaration
    # fails with "provider already exists" — a GUARANTEED first-deploy failure,
    # not an edge case. Detect it and report the parameter value to use.
    info "GitHub OIDC provider (G-016)"
    acct="$(aws_cli sts get-caller-identity --query Account --output text 2>/dev/null)"
    if aws_cli iam get-open-id-connect-provider \
        --open-id-connect-provider-arn "arn:aws:iam::${acct}:oidc-provider/token.actions.githubusercontent.com" \
        >/dev/null 2>&1; then
        ok "provider exists — deploy the bootstrap stack with CreateOIDCProvider=false"
    else
        ok "provider absent — deploy the bootstrap stack with CreateOIDCProvider=true"
    fi

    # §7.1: a catalog entry's secret is only required when some active or shadow
    # names it. Sarvam is catalogued but not a Phase 0 dependency, so checking
    # every entry's key would demand an account nobody needs yet.
    info "provider secrets in SSM (§0.8/4)"
    active_stt="$(yq -r '.providers.stt.active' "$CONFIG")"
    shadow_stt="$(yq -r '.providers.stt.shadow // ""' "$CONFIG")"
    for key in \
        "$(yq -r ".providers.stt.catalog.${active_stt}.secret_ref" "$CONFIG")" \
        "$(yq -r '.providers.llm.catalog | to_entries | .[0].value.secret_ref' "$CONFIG")"; do
        [ -n "$key" ] && [ "$key" != "null" ] || continue
        path="${key//\{env\}/$INSTANCE}"
        # --with-decryption is deliberately NOT used: the agent must not read a
        # secret's value, only confirm the parameter exists (§9.4). kms:Decrypt
        # on these paths is denied to the agent principal.
        if aws_cli ssm get-parameter --name "$path" >/dev/null 2>&1; then
            ok "$path exists"
        else
            violation "§0.8/4: SSM parameter $path is missing. A human must create the provider account and place the key as a SecureString under alias/aws/ssm (free — NOT Secrets Manager, which at \$0.40/secret/month costs more than the rest of the stack, G-019)."
        fi
    done
    if [ -n "$shadow_stt" ] && [ "$shadow_stt" != "null" ]; then
        dim "  shadow mode is enabled ($shadow_stt) — this doubles STT spend while set (§7.2)"
    fi
fi

# --- GitHub Pages and the assetlinks topology (§10.6, §0.8/3) --------------
info "GitHub Pages and hosting (§10.6, §0.8/3)"
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    repo_json="$(gh api repos/vppillai/chintan 2>/dev/null || echo '{}')"
    private="$(printf '%s' "$repo_json" | jq -r '.private // "unknown"')"
    has_pages="$(printf '%s' "$repo_json" | jq -r '.has_pages // false')"
    # G-057: two separate limits. Pages from a private repo needs Pro or above —
    # on Free it is unavailable entirely. And the published site is public
    # regardless of repository visibility.
    if [ "$private" = "true" ]; then
        violation "§0.8/3: the repository is private. GitHub Pages from a private repo requires Pro or above; on Free it is unavailable entirely (G-057). Decide plan and visibility and record an ADR."
    else
        ok "repository is public — Pages works on the Free plan (G-057)"
        dim "  note: the published site is world-readable in every plan. Never ship a"
        dim "  secret, key, or credential in the frontend bundle (§10.6)."
    fi
    if [ "$has_pages" = "true" ]; then
        ok "Pages is enabled"
    else
        violation "§0.8/3: Pages is not enabled on the repository"
    fi

    # G-007: verification reads https://{domain}/.well-known/assetlinks.json at
    # the DOMAIN ROOT. A Pages *project* site serves at
    # https://{user}.github.io/{repo}/, so the well-known path resolves to the
    # user-site repo instead. This gates WebAPK verification, voice launch, and
    # the Phase 8 NFC path.
    origin="$(yq -r '.allowed_origin' "$CONFIG")"
    if printf '%s' "$origin" | grep -q 'github\.io'; then
        violation "§0.8/3 unresolved: allowed_origin is '$origin', a github.io project site. Digital Asset Links must be served from the DOMAIN ROOT, so assetlinks will resolve to the user-site repo, not this one (G-007). Resolve with a custom domain (recommended — it also decouples the app from the Pages URL, which matters for NFC tags that physically encode it) or by serving the file from the {user}.github.io repo. Record an ADR either way."
    fi
else
    violation "gh is unavailable or unauthenticated — cannot check Pages configuration"
fi

# --- Cost allocation tags (§0.8/5, G-023) ----------------------------------
info "cost allocation tags (§0.8/5)"
if aws_cli sts get-caller-identity >/dev/null 2>&1; then
    # These must be activated manually in the Billing console and apply only
    # going forward — they do NOT backfill, so the first months are unrecoverable
    # if this is missed (G-023).
    if tags="$(aws_cli ce list-cost-allocation-tags --status Active --output json 2>/dev/null)"; then
        if printf '%s' "$tags" | jq -e --arg k "$SYSTEM_ID" '.CostAllocationTags[]? | select(.TagKey=="Project")' >/dev/null 2>&1; then
            ok "the Project tag is active as a cost allocation tag"
        else
            violation "§0.8/5: the 'Project' cost allocation tag is not active. It must be activated in the Billing console (console-only), and it does NOT backfill — the first months of per-project cost data are unrecoverable otherwise (G-023)."
        fi
    else
        dim "  cannot query cost allocation tags (ce:ListCostAllocationTags denied) — verify manually in the Billing console"
    fi
fi

log ""
info "reporting complete. Every item above is actionable; none are advisory."
finish_check "doctor (§Phase 0 prerequisites)" "$AS_JSON"
