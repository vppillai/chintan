#!/usr/bin/env bash
#
# Assert the deployed permissions boundary is the one in this repository.
#
# The boundary is the only guardrail in this project that CloudFormation does not
# own. infrastructure/agent-policies/boundary.json is applied exclusively by
# scripts/bootstrap-agent.sh, which a deploy never runs — so a template change
# that needs a new AWS service ships green, and the grant it adds is void.
#
# That is not hypothetical. The boundary predated the capture queue, so its
# ceiling listed no sqs:* at all. template.yaml granted the worker
# sqs:ReceiveMessage, IAM accepted the grant, the role visibly held it, and
# Lambda still reported the role "does not have permissions to call
# ReceiveMessage on the queue" — because a permissions boundary is an
# intersection, and an action absent from the ceiling is denied no matter who
# allows it. The role appears to have a permission it does not have, which is
# the most expensive shape a permissions bug can take.
#
# This check closes that gap by comparing the rendered repository document
# against the live policy's DEFAULT VERSION — the version IAM actually
# evaluates. A non-default version left behind by an earlier apply is not the
# ceiling and is deliberately not compared.
#
# Comparison is semantic, not textual: IAM accepts a bare string wherever it
# accepts a list, reorders nothing meaningfully, and the CLI hands back a
# re-serialised document. Comparing bytes would report drift on every run and
# would then be muted, which is the failure mode §9.8 exists to prevent.
#
# Read-only. It has no --apply and needs none: repairing the boundary means
# creating a policy version, which is exactly what the boundary withholds from
# the agent (I17). It prints the command a human runs instead.
#
# Two halves, because the expensive half needs credentials a pull request does
# not have:
#
#   LOCAL   the ceiling covers every service the templates grant. Needs nothing
#           but the repository, so it runs on every pull request — and it is the
#           half that would have caught the SQS incident before the deploy,
#           rather than during it.
#   REMOTE  the deployed default version equals the rendered repository
#           document. Needs iam:GetPolicy and iam:GetPolicyVersion.
#
# Usage: check-boundary-drift.sh [--region REGION] [--json] [--local-only] [--self-test]

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

BOUNDARY_NAME="chintan-agent-boundary"
POLICY_DIR="${REPO_ROOT}/infrastructure/agent-policies"

# The same default bootstrap-agent.sh uses. Rendering with a different region
# than the boundary was created with would report drift that does not exist.
REGION="${CHINTAN_REGION:-${AWS_REGION:-us-west-2}}"
AS_JSON=""
SELF_TEST=0
LOCAL_ONLY=0

while [ $# -gt 0 ]; do
    case "$1" in
        --region) REGION="${2:?--region needs a value}" && shift ;;
        --json) AS_JSON="--json" ;;
        --local-only) LOCAL_ONLY=1 ;;
        --self-test) SELF_TEST=1 ;;
        -h | --help)
            sed -n '2,43p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

# ---------------------------------------------------------------------------
# Rendering and canonicalisation
# ---------------------------------------------------------------------------

# render_boundary substitutes the placeholders exactly as bootstrap-agent.sh
# does. Any divergence here would produce drift reports on a boundary that is
# in fact correct, so this is a deliberate duplicate of one sed expression
# rather than a reimplementation.
render_boundary() {
    local account="$1" region="$2" src="${3:-${POLICY_DIR}/boundary.json}"
    [ -f "$src" ] || die "missing $src"
    sed -e "s/ACCOUNT_ID/${account}/g" -e "s/REGION/${region}/g" "$src"
}

# canonicalise normalises a policy document so that two semantically identical
# documents compare equal: statements ordered by Sid, single-value Action and
# Resource fields promoted to lists, every list sorted, every key sorted.
canonicalise() {
    python3 - "$1" <<'PY'
import json
import sys

LISTY = ("Action", "NotAction", "Resource", "NotResource", "Principal", "NotPrincipal")


def norm(node):
    if isinstance(node, dict):
        return {k: norm(v) for k, v in sorted(node.items())}
    if isinstance(node, list):
        return sorted((norm(v) for v in node), key=lambda v: json.dumps(v, sort_keys=True))
    return node


with open(sys.argv[1]) as fh:
    doc = json.load(fh)

statements = doc.get("Statement", [])
if isinstance(statements, dict):
    statements = [statements]

out = []
for st in statements:
    st = dict(st)
    for key in LISTY:
        if key in st and isinstance(st[key], str):
            st[key] = [st[key]]
    out.append(norm(st))

out.sort(key=lambda s: (s.get("Sid", ""), json.dumps(s, sort_keys=True)))
doc = norm({k: v for k, v in doc.items() if k != "Statement"})
doc["Statement"] = out
print(json.dumps(doc, indent=2, sort_keys=True))
PY
}

# check_ceiling_covers_templates asserts every AWS service namespace the
# CloudFormation templates grant an action for is present in the boundary's
# ceiling.
#
# This is the credential-free half, and it is the one that would have caught the
# SQS incident on the pull request instead of on the deploy: template.yaml
# granted the worker sqs:ReceiveMessage while the ceiling listed no sqs:* at
# all, so the grant was void and Lambda reported a role that "does not have
# permissions" for something it visibly held.
#
# Actions are lifted by pattern rather than by parsing the YAML, because the
# templates are full of intrinsics no plain YAML loader will read. The pattern
# is deliberately narrow: a list item that is exactly service:Action.
check_ceiling_covers_templates() {
    local boundary="${1:-${POLICY_DIR}/boundary.json}"
    local infra="${2:-${REPO_ROOT}/infrastructure}"
    python3 - "$boundary" "$infra" <<'PY'
import json
import re
import sys
from pathlib import Path

boundary = json.load(open(sys.argv[1]))

ceiling = set()
denied = set()
for st in boundary["Statement"]:
    actions = st.get("Action", [])
    if isinstance(actions, str):
        actions = [actions]
    for a in actions:
        if not a.endswith(":*"):
            continue
        svc = a.split(":", 1)[0]
        if st.get("Effect") == "Allow":
            ceiling.add(svc)
        elif st.get("Sid") == "DenyRunawayCostServices":
            denied.add(svc)

ACTION = re.compile(r"^\s*-\s+([a-z0-9][a-z0-9-]{1,30}):([A-Z][A-Za-z0-9]*|\*)\s*$")

used = {}
for path in sorted(Path(sys.argv[2]).glob("*.yaml")):
    for lineno, line in enumerate(path.read_text().splitlines(), 1):
        m = ACTION.match(line)
        if not m:
            continue
        used.setdefault(m.group(1), []).append(f"{path.name}:{lineno}: {m.group(0).strip()}")

missing = sorted(s for s in used if s not in ceiling)
forbidden = sorted(s for s in used if s in denied)

for svc in missing:
    print(f"MISSING {svc}", *(f"    {w}" for w in used[svc][:3]), sep="\n")
for svc in forbidden:
    print(f"DENIED {svc}", *(f"    {w}" for w in used[svc][:3]), sep="\n")

sys.exit(1 if (missing or forbidden) else 0)
PY
}

# compare_documents is the whole detector, expressed over two files so that
# --self-test can exercise it without AWS. It prints a diff and returns 1 when
# the documents differ.
compare_documents() {
    local expected="$1" actual="$2" tmp
    tmp="$(mktemp -d)"
    canonicalise "$expected" >"${tmp}/expected.json" || {
        rm -rf "$tmp"
        return 2
    }
    canonicalise "$actual" >"${tmp}/actual.json" || {
        rm -rf "$tmp"
        return 2
    }
    if diff -u "${tmp}/expected.json" "${tmp}/actual.json" >"${tmp}/diff.txt"; then
        rm -rf "$tmp"
        return 0
    fi
    # Labelled from the operator's point of view: "repo" is what should be
    # deployed, "live" is what is.
    sed -e "1s|.*|--- repo (rendered infrastructure/agent-policies/boundary.json)|" \
        -e "2s|.*|+++ live (${BOUNDARY_NAME} default version)|" "${tmp}/diff.txt" >&2
    rm -rf "$tmp"
    return 1
}

# ---------------------------------------------------------------------------
# Self-test: prove the detector detects (§0.5A, §Phase 0 acceptance)
# ---------------------------------------------------------------------------
#
# Reproduces the real incident rather than an arbitrary edit: the deployed
# ceiling is missing sqs:*, which is precisely the drift that cost a debugging
# cycle and which a textual or attachment-only check would not have caught.
if [ "$SELF_TEST" = "1" ]; then
    info "self-test: asserting this check detects a drifted boundary"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT

    render_boundary 000000000000 us-west-2 >"${tmp}/repo.json"

    # Control case first. If identical documents do not compare equal, a
    # detected difference below proves nothing about detection — it would only
    # prove the comparator always reports drift. guardrails-check.sh --self-test
    # shipped without this premise check once and reported success while never
    # exercising itself.
    cp "${tmp}/repo.json" "${tmp}/same.json"
    if ! compare_documents "${tmp}/repo.json" "${tmp}/same.json" >/dev/null 2>&1; then
        err "self-test inconclusive: the check reports drift between a document and itself"
        err "fix the comparator first — a check that always fails proves nothing"
        exit 1
    fi
    ok "control: an unmodified boundary reports no drift"

    # Reordering alone must NOT read as drift, or the check gets muted.
    python3 - "${tmp}/repo.json" "${tmp}/reordered.json" <<'PY'
import json
import sys

with open(sys.argv[1]) as fh:
    doc = json.load(fh)
doc["Statement"].reverse()
for st in doc["Statement"]:
    for key in ("Action", "Resource"):
        if isinstance(st.get(key), list):
            st[key] = list(reversed(st[key]))
with open(sys.argv[2], "w") as fh:
    json.dump(doc, fh)
PY
    if ! compare_documents "${tmp}/repo.json" "${tmp}/reordered.json" >/dev/null 2>&1; then
        err "self-test FAILED: a reordered but identical document reported drift"
        err "a check that cries wolf on every run is a check that gets muted"
        exit 1
    fi
    ok "control: reordering statements and actions is not drift"

    # The real drift: the live ceiling predates SQS.
    python3 - "${tmp}/repo.json" "${tmp}/drifted.json" <<'PY'
import json
import sys

with open(sys.argv[1]) as fh:
    doc = json.load(fh)
for st in doc["Statement"]:
    if st.get("Sid") == "CeilingServicesThisProjectUses":
        st["Action"] = [a for a in st["Action"] if a != "sqs:*"]
with open(sys.argv[2], "w") as fh:
    json.dump(doc, fh)
PY
    if diff -q "${tmp}/repo.json" "${tmp}/drifted.json" >/dev/null 2>&1; then
        err "self-test inconclusive: the drift fixture is identical to the source"
        err "boundary.json no longer grants sqs:* under CeilingServicesThisProjectUses"
        exit 1
    fi
    if compare_documents "${tmp}/repo.json" "${tmp}/drifted.json" >/dev/null 2>&1; then
        err "self-test FAILED: a boundary missing sqs:* reported no drift"
        err "this is the exact drift that made the worker's sqs:ReceiveMessage void"
        exit 1
    fi
    ok "self-test: the check fails when the deployed ceiling is missing a service"

    # And the credential-free half, over the same drift: with sqs:* gone from
    # the ceiling, template.yaml's sqs grants are outside it.
    if check_ceiling_covers_templates >/dev/null 2>&1; then
        ok "control: the repository ceiling covers the templates"
    else
        err "self-test inconclusive: the ceiling does not cover the templates as committed"
        err "fix that first — run scripts/check-boundary-drift.sh --local-only"
        exit 1
    fi

    if check_ceiling_covers_templates "${tmp}/drifted.json" \
        "${REPO_ROOT}/infrastructure" >/dev/null 2>&1; then
        err "self-test FAILED: a ceiling missing sqs:* covered templates that grant sqs actions"
        err "this is the exact gap that shipped a void sqs:ReceiveMessage grant"
        exit 1
    fi
    ok "self-test: the check fails when the templates grant a service the ceiling omits"
    exit 0
fi

# ---------------------------------------------------------------------------
# LOCAL: the ceiling covers what the templates grant
# ---------------------------------------------------------------------------

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

info "the boundary's ceiling covers every service infrastructure/*.yaml grants"
if COVERAGE="$(check_ceiling_covers_templates 2>&1)"; then
    ok "every service the templates grant an action for is inside the ceiling"
else
    printf '%s\n' "$COVERAGE" >&2
    violation "infrastructure/*.yaml grants actions for services the boundary's ceiling does not allow (listed above).

A permissions boundary is an INTERSECTION. An action outside the ceiling is denied
however the template grants it: the role holds the grant, IAM shows the grant, and
the service still reports that the role 'does not have permissions'. Add the
missing 'service:*' entries to the CeilingServicesThisProjectUses statement in
infrastructure/agent-policies/boundary.json, then apply them:

    scripts/bootstrap-agent.sh --region ${REGION} --apply

A service listed as DENIED is in DenyRunawayCostServices — the template should not
be using it at all."
fi

if [ "$LOCAL_ONLY" = "1" ]; then
    dim "  --local-only: skipping the comparison against the deployed policy"
    finish_check "permissions boundary (local checks only)" "$AS_JSON"
    exit $?
fi

# ---------------------------------------------------------------------------
# REMOTE: the deployed default version equals the repository document
# ---------------------------------------------------------------------------

if ! ACCOUNT_ID="$(aws_cli sts get-caller-identity --query Account --output text 2>/dev/null)" ||
    [ -z "$ACCOUNT_ID" ] || [ "$ACCOUNT_ID" = "None" ]; then
    # Not a pass. This check cannot be satisfied without looking, and reporting
    # success without having looked is the failure mode it exists to prevent.
    err "cannot resolve the AWS account: no usable credentials"
    err "run this where the account is reachable, or omit it deliberately —"
    err "it must not be reported as passing"
    exit 1
fi

POLICY_ARN="arn:aws:iam::${ACCOUNT_ID}:policy/${BOUNDARY_NAME}"
info "comparing ${BOUNDARY_NAME} against infrastructure/agent-policies/boundary.json"
dim "  account: $ACCOUNT_ID"
dim "  region:  $REGION"

if ! DEFAULT_VERSION="$(aws_cli iam get-policy --policy-arn "$POLICY_ARN" \
    --query 'Policy.DefaultVersionId' --output text 2>/dev/null)" ||
    [ -z "$DEFAULT_VERSION" ] || [ "$DEFAULT_VERSION" = "None" ]; then
    err "cannot read $POLICY_ARN"
    err "either the boundary does not exist — run scripts/bootstrap-agent.sh --apply —"
    err "or this principal lacks iam:GetPolicy on arn:aws:iam::${ACCOUNT_ID}:policy/chintan-agent-*."
    exit 1
fi
dim "  default version: $DEFAULT_VERSION"

if ! aws_cli iam get-policy-version --policy-arn "$POLICY_ARN" \
    --version-id "$DEFAULT_VERSION" --query 'PolicyVersion.Document' --output json \
    >"${WORK}/live.json" 2>/dev/null; then
    err "cannot read version $DEFAULT_VERSION of $POLICY_ARN (iam:GetPolicyVersion denied?)"
    exit 1
fi

render_boundary "$ACCOUNT_ID" "$REGION" >"${WORK}/repo.json"

# common.sh sets -e, so the status must be captured rather than read from $?
# after a bare call: a non-zero return would abort the script here and the
# operator would see the diff with none of the instructions below it. That is
# how the first version of this check behaved.
DRIFT=0
compare_documents "${WORK}/repo.json" "${WORK}/live.json" || DRIFT=$?
case $DRIFT in
    0)
        ok "the deployed boundary matches the repository"
        ;;
    1)
        violation "the deployed permissions boundary differs from infrastructure/agent-policies/boundary.json (diff above: '-' is the repository, '+' is what is deployed).

The boundary is an intersection, so any action the deployed ceiling omits is DENIED
however the template grants it — the role will appear to hold a permission it does
not have, and CloudFormation will report a permissions error for a grant that is
visibly present.

To reconcile, as a human with credentials that are not the agent's:

    scripts/bootstrap-agent.sh --region ${REGION} --apply

That creates a new version of ${BOUNDARY_NAME} from the repository document and
makes it the default. The agent principal cannot do this itself by design
(iam:CreatePolicyVersion and iam:SetDefaultPolicyVersion are denied on
chintan-agent-*), so this check reports rather than repairs.

If the DEPLOYED document is the correct one, update the repository file instead —
never leave the two disagreeing, because bootstrap-agent.sh will silently
overwrite the live policy with the repository's version on its next run."
        ;;
    *)
        violation "could not canonicalise one of the documents — see the error above"
        ;;
esac

finish_check "permissions boundary matches the repository (§9.8)" "$AS_JSON"
