#!/usr/bin/env bash
#
# Assert that the custom API domain in infrastructure/template.yaml is dormant:
# the two parameters default to empty, and every resource and output that
# exists for the domain carries the Condition that turns it off.
#
# WHY THIS EXISTS
#
# ApiCertificate, ApiDomainName, ApiMapping and ApiAliasRecord are the first
# resources in the template that must NOT be created on the shipped configs,
# which set no api_host. A Condition dropped from one of them — a merge, a
# copy of a sibling — would make the next deploy request an ACM certificate
# for '' and fail every stack, staging first, for a reason that names ACM and
# not the missing line. cfn-lint checks that a Condition a resource names
# exists; nothing checked that the resources which need one have it.
#
# Usage:
#   scripts/check-custom-domain-dormant.sh [--json] [--self-test]

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

AS_JSON=""
SELF_TEST=0

while [ $# -gt 0 ]; do
    case "$1" in
        --json) AS_JSON="--json" ;;
        --self-test) SELF_TEST=1 ;;
        -h | --help)
            usage_from_header "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

# Overridable so the self-test can point this at a doctored copy.
TEMPLATE="${CHINTAN_TEMPLATE:-$REPO_ROOT/infrastructure/template.yaml}"
require_cmd python3

# Parsed in Python with PyYAML, as scripts/list-instances.sh does. The
# intrinsic-function tags (!Ref, !If, !Sub ...) are read as their plain
# values: the check needs the Condition key of a resource and the Default of a
# parameter, neither of which is inside a tag.
run_check() {
    local out
    out="$(
        CHINTAN_TEMPLATE_PATH="$TEMPLATE" python3 - <<'PY'
import os
import sys

try:
    import yaml
except ImportError:
    sys.exit("PyYAML is required: python3 -m pip install pyyaml")


class Plain(yaml.SafeLoader):
    pass


def any_tag(loader, _suffix, node):
    if isinstance(node, yaml.ScalarNode):
        return loader.construct_scalar(node)
    if isinstance(node, yaml.SequenceNode):
        return loader.construct_sequence(node)
    return loader.construct_mapping(node)


Plain.add_multi_constructor("!", any_tag)

with open(os.environ["CHINTAN_TEMPLATE_PATH"]) as f:
    doc = yaml.load(f, Loader=Plain)

params = doc.get("Parameters", {})
conditions = doc.get("Conditions", {})
resources = doc.get("Resources", {})
outputs = doc.get("Outputs", {})

# What the provision is made of, and the condition each part must carry.
GUARDED = {
    "ApiCertificate": "HasApiHost",
    "ApiDomainName": "HasApiHost",
    "ApiMapping": "HasApiHost",
    "ApiAliasRecord": "HasDnsZone",
}
MENTIONS = ("ApiHost", "DnsZoneId", *GUARDED)

for name in ("ApiHost", "DnsZoneId"):
    if name not in params:
        print(f"parameter {name} is missing")
    elif params[name].get("Default") != "":
        print(f"parameter {name} must default to '' so a config without api_host deploys nothing")

for name in ("HasApiHost", "HasDnsZone"):
    if name not in conditions:
        print(f"condition {name} is missing")

for name, condition in GUARDED.items():
    if name not in resources:
        print(f"resource {name} is missing")
    elif resources[name].get("Condition") != condition:
        print(f"{name} must carry Condition: {condition}; it has {resources[name].get('Condition')!r}")


def mentions(body):
    text = repr(body)
    return any(m in text for m in MENTIONS)


# Anything ELSE that reaches into the provision has to be conditional too, or
# it dereferences a resource that does not exist on the shipped configs.
for name, body in resources.items():
    if name in GUARDED:
        continue
    if mentions(body.get("Properties", {})) and "Condition" not in body:
        print(f"resource {name} references the custom-domain provision but carries no Condition")

for name, body in outputs.items():
    text = repr(body.get("Value"))
    if any(g in text for g in GUARDED) and body.get("Condition") not in GUARDED.values():
        print(f"output {name} reads a custom-domain resource but carries no Condition")
PY
    )" || die "could not parse $TEMPLATE"
    local line
    while IFS= read -r line; do
        [ -n "$line" ] || continue
        violation "$line"
    done <<<"$out"
}

# ---------------------------------------------------------------------------
# Self-test: the check fails on the defect it exists for
# ---------------------------------------------------------------------------

if [ "$SELF_TEST" = "1" ]; then
    info "self-test: asserting this check fails when the provision stops being dormant"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT

    if ! CHINTAN_TEMPLATE="$TEMPLATE" "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
        die "self-test inconclusive: the check fails on the committed template"
    fi
    ok "control: the committed template passes"

    # The Condition line under ApiCertificate, and only that one, removed.
    awk '/^  ApiCertificate:$/ {in_block=1} in_block && /^    Condition: HasApiHost$/ {in_block=0; next} {print}' \
        "$TEMPLATE" >"$tmp/template.yaml"
    if CHINTAN_TEMPLATE="$tmp/template.yaml" "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
        die "self-test FAILED: ApiCertificate without its Condition passed"
    fi
    ok "a resource that lost its Condition is refused"

    # Every empty default filled in: the same defect from the other side,
    # since a deploy with no api_host would then carry a hostname anyway.
    sed "s/^    Default: ''$/    Default: api.example.com/" "$TEMPLATE" >"$tmp/template.yaml"
    if CHINTAN_TEMPLATE="$tmp/template.yaml" "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
        die "self-test FAILED: ApiHost with a non-empty default passed"
    fi
    ok "a parameter that no longer defaults to empty is refused"
    exit 0
fi

info "the custom-domain provision in $TEMPLATE is dormant without api_host"
run_check
finish_check "custom domain dormant" "$AS_JSON"
