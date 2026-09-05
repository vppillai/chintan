#!/usr/bin/env bash
#
# Resolve config/instances/*.yaml into the deployment matrix.
#
# This script is the one reader of config/instances/*.yaml. Both deploy
# workflows call it, so a field added here is a field that reaches
# CloudFormation, and a field removed from a config breaks the deploy rather
# than being silently ignored. A config that nothing reads is documentation that
# cannot go stale because it was never true; a single reader is what keeps the
# schema below honest.
#
# Schema — every field except `name` has a default:
#
#   name          instance name; the <instance> in chintan-<instance>-<env>.
#                 Required. Lowercase letters, digits and hyphens, <= 32 chars.
#   environment   prod | staging | dev. Default: prod.
#   region        AWS region. Default: $AWS_REGION, else us-west-2.
#   site_path     GitHub Pages sub-path for this instance's bundle.
#                 Default: <name> for prod, <name>-<environment> otherwise.
#   display_name  Human label. Default: the instance name.
#
# Optional CloudFormation parameters, each with a template default so omitting
# them is always safe:
#
#   alarm_email                    subscribed to the alarm topic and the budget
#   monthly_budget_usd             AWS Budgets limit for this stack
#   log_retention_days             CloudWatch retention
#   daily_spend_cap_micros         INSTANCE-WIDE daily provider spend ceiling,
#                                  in MICRODOLLARS (1000000 = $1). Absent means
#                                  the template default, $5/day; an explicit 0
#                                  disables the cap (the breaker records and
#                                  enforces nothing, and the spend-cap alarm is
#                                  not created) — do that only deliberately
#   refresh_token_validity_days    Cognito refresh token lifetime
#
# Two files may share a `name` as long as their `environment` differs: that is
# exactly how a staging copy of an instance is expressed, and it is why the stack
# name carries both.
#
# Usage:
#   scripts/list-instances.sh                        # every instance, as JSON
#   scripts/list-instances.sh --environment staging  # only staging entries
#   scripts/list-instances.sh --format text          # one "stack region" per line

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

FILTER_ENV=""
FORMAT="json"

while [ $# -gt 0 ]; do
    case "$1" in
        --environment)
            FILTER_ENV="${2:?--environment needs a value}"
            shift
            ;;
        --format)
            FORMAT="${2:?--format needs a value}"
            shift
            ;;
        -h | --help)
            usage_from_header "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

case "$FORMAT" in
    json | text) ;;
    *) die "--format must be json or text" ;;
esac

require_cmd python3 jq

CONFIG_DIR="$REPO_ROOT/config/instances"
[ -d "$CONFIG_DIR" ] || die "no config/instances directory at $CONFIG_DIR"

# Parsed in Python rather than with yq: yq is not installed on a stock GitHub
# runner, whereas python3 with PyYAML is present on every runner image and in
# the dev container.
entries="$(
    CHINTAN_CONFIG_DIR="$CONFIG_DIR" \
        CHINTAN_DEFAULT_REGION="${AWS_REGION:-us-west-2}" \
        python3 - <<'PY'
import json
import os
import pathlib
import sys

try:
    import yaml
except ImportError:
    sys.exit("PyYAML is required: python3 -m pip install pyyaml")

config_dir = pathlib.Path(os.environ["CHINTAN_CONFIG_DIR"])
default_region = os.environ["CHINTAN_DEFAULT_REGION"]
valid_envs = {"prod", "staging", "dev"}

out = []
seen = set()
for path in sorted(config_dir.glob("*.yaml")):
    doc = yaml.safe_load(path.read_text()) or {}
    if not isinstance(doc, dict):
        sys.exit(f"{path}: expected a mapping at the top level")

    name = doc.get("name")
    if not name:
        sys.exit(f"{path}: 'name' is required")
    if not isinstance(name, str) or not name.replace("-", "").isalnum() or name != name.lower():
        sys.exit(f"{path}: 'name' must be lowercase letters, digits and hyphens")
    if len(name) > 32:
        sys.exit(f"{path}: 'name' must be 32 characters or less")

    env = str(doc.get("environment", "prod"))
    if env not in valid_envs:
        sys.exit(f"{path}: 'environment' must be one of {sorted(valid_envs)}")

    stack = f"chintan-{name}-{env}"
    if stack in seen:
        sys.exit(f"{path}: duplicate stack {stack}; two configs resolve to the same stack")
    seen.add(stack)

    site_path = str(doc.get("site_path") or (name if env == "prod" else f"{name}-{env}"))

    # Optional CloudFormation parameters. Every one of these has a default in
    # infrastructure/template.yaml, so a config that omits them deploys cleanly;
    # setting one here is what makes the file more than a filename. Emitted as
    # Key=Value strings because that is what scripts/deploy.sh --parameter takes.
    optional = {
        "AlarmEmail": doc.get("alarm_email"),
        "MonthlyBudgetUSD": doc.get("monthly_budget_usd"),
        "LogRetentionDays": doc.get("log_retention_days"),
        # MICRODOLLARS: 1000000 = $1. Absent here means the template default,
        # $5/day (5000000). An explicit 0 disables the cap and leaves
        # HasSpendCap false and the spend-cap alarm uncreated.
        "DailySpendCapMicros": doc.get("daily_spend_cap_micros"),
        "RefreshTokenValidityDays": doc.get("refresh_token_validity_days"),
        # CloudWatch bills alarms beyond ten alarm-months, and this template
        # declares exactly ten, so a second environment doubles into the paid
        # band. Absent means the template default, true.
        "EnableAlarms": doc.get("enable_alarms"),
    }

    def render(v):
        # YAML `false` parses to Python False, and f"{False}" is "False" —
        # which the template rejects, because AllowedValues are the lowercase
        # JSON spellings. Booleans have to be spelled back out deliberately.
        if isinstance(v, bool):
            return "true" if v else "false"
        return str(v)

    # `is None` rather than a falsy test: 0 and False are meaningful values here.
    # DailySpendCapMicros=0 is "record but never enforce", and EnableAlarms=false
    # is the whole point of the field; a truthiness check would drop both and
    # silently restore the template default.
    parameters = [f"{k}={render(v)}" for k, v in optional.items() if v is not None and v != ""]

    out.append(
        {
            "config": str(path.relative_to(config_dir.parent.parent)),
            "instance": name,
            "environment": env,
            "stack": stack,
            "region": str(doc.get("region") or default_region),
            "site_path": site_path,
            "display_name": str(doc.get("display_name") or name),
            "parameters": parameters,
        }
    )

if not out:
    sys.exit(f"{config_dir}: no instance configs found")

json.dump(out, sys.stdout)
PY
)"

if [ -n "$FILTER_ENV" ]; then
    validate_environment "$FILTER_ENV"
    entries="$(printf '%s' "$entries" | jq -c --arg e "$FILTER_ENV" '[.[] | select(.environment == $e)]')"
fi

if [ "$FORMAT" = "text" ]; then
    printf '%s' "$entries" | jq -r '.[] | "\(.stack) \(.region) \(.site_path)"'
else
    printf '%s\n' "$(printf '%s' "$entries" | jq -c .)"
fi
