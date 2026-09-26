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
# Schema — every field except `name`, `display_name` and `description` has a
# default:
#
#   name          instance name; the <instance> in chintan-<instance>-<env>.
#                 Required. Lowercase letters, digits and hyphens, <= 32 chars.
#   environment   prod | staging | dev. Default: prod.
#   region        AWS region. Default: $AWS_REGION, else us-west-2.
#   site_path     GitHub Pages sub-path for this instance's bundle.
#                 Default: <name> for prod, <name>-<environment> otherwise.
#   display_name  What the app calls itself: the document title, the manifest's
#                 `name`, the shell's wordmark, the About heading. Required.
#   short_name    The home-screen label under the installed icon. At most 12
#                 characters, which is what launchers show before truncating.
#                 Default: display_name — which must then fit.
#   description   One sentence: <meta name="description">, the manifest's
#                 `description`, the lede on About. Required.
#   api_host      Custom hostname for this stack's API, e.g. api.example.com.
#                 Optional; a bare lowercase hostname, no scheme, no path. The
#                 stack then fronts the HTTP API with it under a free ACM
#                 certificate and its ApiEndpoint output — so the bundle's
#                 VITE_API_URL and the Devices card — becomes https://<api_host>.
#                 Reaches the template as ApiHost.
#   app_host      The GitHub Pages custom domain the bundles are served from,
#                 e.g. app.example.com. Optional. Pages has ONE domain per
#                 site, so every config sets the same value or none does; a
#                 mix is refused. Sets every stack's Cognito callback URLs and
#                 CORS origin (scripts/ci-deploy-stack.sh) and makes
#                 scripts/ci-build-site.sh build for the site root. GitHub is
#                 told the domain by scripts/setup.sh, because Pages ignores a
#                 CNAME file published from a workflow.
#   dns_zone_id   Route 53 hosted zone that holds api_host. Optional; set, the
#                 stack writes the certificate's validation record and the API
#                 alias itself. Refused without api_host. Reaches the template
#                 as DnsZoneId.
#
# None of the three may contain ", <, > or &. Vite writes them into
# frontend/index.html by plain substitution (%VITE_APP_NAME% in <title>, the
# other two in attribute values) with no HTML escaping, so any of those
# characters would end the title, the attribute or the element and the page
# would ship broken — or, in a fork whose configs are not its own, with
# markup nobody wrote. Refused here, where every config is read.
#
# The identity fields reach the bundle as VITE_APP_NAME, VITE_APP_SHORT_NAME
# and VITE_APP_DESCRIPTION, exported by scripts/ci-build-site.sh. Colours are
# deliberately not here: the design tokens own them (frontend/manifest.config.ts
# says why the manifest's are a constant).
#
# An unknown field fails the run. The whole point of a single reader is that a
# field which nothing reads cannot sit in a config looking as though it works.
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
#   scripts/list-instances.sh --app-host             # the one Pages custom domain, or nothing
#   scripts/list-instances.sh --self-test            # prove the character check fails

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

FILTER_ENV=""
FORMAT="json"
SELF_TEST=0

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
        --app-host) FORMAT="app-host" ;;
        --self-test) SELF_TEST=1 ;;
        -h | --help)
            usage_from_header "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

case "$FORMAT" in
    json | text | app-host) ;;
    *) die "--format must be json or text" ;;
esac

require_cmd python3 jq

if [ "$SELF_TEST" = "1" ]; then
    info "self-test: asserting the character check refuses what index.html cannot carry"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    mkdir -p "$tmp/config/instances"
    cat >"$tmp/config/instances/one.yaml" <<'YAML'
name: one
display_name: One
description: A sentence with an apostrophe's worth of punctuation, and a dash.
YAML
    if ! CHINTAN_REPO_ROOT="$tmp" "${BASH_SOURCE[0]}" --format text >/dev/null 2>&1; then
        die "self-test inconclusive: a clean config was refused"
    fi
    ok "control: a clean config resolves"

    # Each of the four characters, in each of the three fields, must be
    # refused and named in the message. YAML single quotes, inside which all
    # four are literal.
    for field in display_name short_name description; do
        for ch in '"' '<' '>' '&'; do
            {
                printf 'name: one\n'
                printf 'display_name: One\n'
                printf 'description: Fine.\n'
                printf "%s: 'Bad %s here'\n" "$field" "$ch"
            } >"$tmp/config/instances/one.yaml"
            if out="$(CHINTAN_REPO_ROOT="$tmp" "${BASH_SOURCE[0]}" --format text 2>&1)"; then
                die "self-test FAILED: '$field' containing $ch resolved"
            fi
            case "$out" in
                *"'$field' must not contain"*) ;;
                *) die "self-test FAILED: '$field' containing $ch was refused for another reason: $out" ;;
            esac
        done
    done
    ok "self-test: every one of the four characters is refused in every identity field"

    # The custom-domain fields. A host with a scheme would reach the template
    # as ApiHost and fail its AllowedPattern one deploy later; two configs
    # naming different app_hosts would give one stack callback URLs on a
    # domain Pages does not serve. Both are refused here.
    printf 'name: one\ndisplay_name: One\ndescription: Fine.\napi_host: https://api.example.com\n' >"$tmp/config/instances/one.yaml"
    if CHINTAN_REPO_ROOT="$tmp" "${BASH_SOURCE[0]}" --format text >/dev/null 2>&1; then
        die "self-test FAILED: an api_host with a scheme resolved"
    fi
    printf 'name: one\ndisplay_name: One\ndescription: Fine.\napp_host: app.example.com\n' >"$tmp/config/instances/one.yaml"
    printf 'name: one\nenvironment: staging\ndisplay_name: One\ndescription: Fine.\napp_host: other.example.com\n' >"$tmp/config/instances/two.yaml"
    if CHINTAN_REPO_ROOT="$tmp" "${BASH_SOURCE[0]}" --format text >/dev/null 2>&1; then
        die "self-test FAILED: two configs disagreeing on app_host resolved"
    fi
    printf 'name: one\nenvironment: staging\ndisplay_name: One\ndescription: Fine.\n' >"$tmp/config/instances/two.yaml"
    if CHINTAN_REPO_ROOT="$tmp" "${BASH_SOURCE[0]}" --format text >/dev/null 2>&1; then
        die "self-test FAILED: app_host in one config and not the other resolved"
    fi
    printf 'name: one\nenvironment: staging\ndisplay_name: One\ndescription: Fine.\napp_host: app.example.com\napi_host: api-staging.example.com\n' >"$tmp/config/instances/two.yaml"
    got="$(CHINTAN_REPO_ROOT="$tmp" "${BASH_SOURCE[0]}" --app-host)"
    [ "$got" = "app.example.com" ] || die "self-test FAILED: --app-host printed '$got', expected app.example.com"
    params="$(CHINTAN_REPO_ROOT="$tmp" "${BASH_SOURCE[0]}" | jq -r '.[] | select(.environment=="staging") | .parameters[]')"
    case "$params" in
        *"ApiHost=api-staging.example.com"*) ;;
        *) die "self-test FAILED: api_host did not reach the parameters: $params" ;;
    esac
    ok "self-test: hosts are bare, app_host is one value for the site, api_host reaches the stack"
    exit 0
fi

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
import re
import sys

try:
    import yaml
except ImportError:
    sys.exit("PyYAML is required: python3 -m pip install pyyaml")

config_dir = pathlib.Path(os.environ["CHINTAN_CONFIG_DIR"])
default_region = os.environ["CHINTAN_DEFAULT_REGION"]
valid_envs = {"prod", "staging", "dev"}

# Every key a config may carry. Kept next to the loop that reads them so adding
# a field means adding it in both places, in the same file.
KNOWN_FIELDS = {
    "name",
    "environment",
    "region",
    "site_path",
    "display_name",
    "short_name",
    "description",
    "alarm_email",
    "monthly_budget_usd",
    "log_retention_days",
    "daily_spend_cap_micros",
    "refresh_token_validity_days",
    "enable_alarms",
    "api_host",
    "app_host",
    "dns_zone_id",
}

# A bare hostname: labels of lowercase letters, digits and hyphens, at least
# one dot. No scheme, path or port — the template's ApiHost AllowedPattern is
# this same shape, and refusing here is one deploy round trip cheaper.
HOST = re.compile(r"^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$")

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

    # The app's user-visible identity. Required rather than defaulted from the
    # instance name: "dev" is not a name to put in a browser tab, and a default
    # here would let a config ship a title nobody chose. The short name is
    # capped where launchers cap it, so a config cannot promise a label the
    # home screen then truncates.
    def text(key, *, required):
        value = doc.get(key)
        if value is None or (isinstance(value, str) and not value.strip()):
            if required:
                sys.exit(
                    f"{path}: '{key}' is required — it is what the app calls itself "
                    f"(see the schema in scripts/list-instances.sh)"
                )
            return None
        if not isinstance(value, str):
            sys.exit(f"{path}: '{key}' must be a string")
        # These reach frontend/index.html by plain substitution — %VITE_APP_NAME%
        # inside <title>, the other two inside attribute values — and Vite does
        # not HTML-escape them, so any of these characters ends the element or
        # the attribute and the page ships broken. Refused rather than escaped:
        # the same strings also reach the manifest and the bundle as JSON, and
        # one representation that is right everywhere beats two that must agree.
        for ch in '"<>&':
            if ch in value:
                sys.exit(
                    f"{path}: '{key}' must not contain {ch!r} — it is written into "
                    f"frontend/index.html (the title, the meta description, the "
                    f"home-screen title) without HTML escaping"
                )
        return value.strip()

    display_name = text("display_name", required=True)
    description = text("description", required=True)
    short_name = text("short_name", required=False) or display_name
    if len(short_name) > 12:
        sys.exit(
            f"{path}: 'short_name' ({short_name!r}) must be 12 characters or less — "
            f"set one when 'display_name' is longer than that"
        )

    def host(key):
        value = doc.get(key)
        if value is None or value == "":
            return ""
        if not isinstance(value, str) or not HOST.match(value):
            sys.exit(
                f"{path}: '{key}' must be a bare lowercase hostname such as "
                f"api.example.com — no scheme, no path, no port (got {value!r})"
            )
        return value

    api_host = host("api_host")
    app_host = host("app_host")
    dns_zone_id = doc.get("dns_zone_id")
    if dns_zone_id and not api_host:
        sys.exit(f"{path}: 'dns_zone_id' without 'api_host' has nothing to write; remove it or set api_host")

    unknown = sorted(set(doc) - KNOWN_FIELDS)
    if unknown:
        sys.exit(
            f"{path}: unknown field(s) {', '.join(unknown)} — nothing reads them; "
            f"see the schema in scripts/list-instances.sh"
        )

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
        # CloudWatch bills alarms beyond ten alarm-months for the account, and
        # this template declares up to six per stack, so a second environment
        # with alarms on crosses into the paid band. Absent means the template
        # default, true.
        "EnableAlarms": doc.get("enable_alarms"),
        # The custom API domain. Both template parameters default to '' and
        # every resource behind them is conditional, so absent here means the
        # execute-api URL and nothing created.
        "ApiHost": api_host,
        "DnsZoneId": dns_zone_id,
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
            "display_name": display_name,
            "short_name": short_name,
            "description": description,
            "app_host": app_host,
            "parameters": parameters,
        }
    )

if not out:
    sys.exit(f"{config_dir}: no instance configs found")

# One Pages site, one custom domain. A stack whose callbacks name the Pages
# host while Pages redirects that host to the custom domain can never finish a
# sign-in (redirect_mismatch), so a mix is refused, not defaulted.
hosts = {e["app_host"] for e in out}
if len(hosts) > 1:
    if "" in hosts:
        sys.exit(
            "app_host is set in some configs and not others; GitHub Pages serves every "
            "instance from one domain, so set it in every config or in none"
        )
    sys.exit(
        f"configs disagree on app_host ({', '.join(sorted(hosts))}); "
        f"GitHub Pages has one custom domain per site"
    )

json.dump(out, sys.stdout)
PY
)"

if [ -n "$FILTER_ENV" ]; then
    validate_environment "$FILTER_ENV"
    entries="$(printf '%s' "$entries" | jq -c --arg e "$FILTER_ENV" '[.[] | select(.environment == $e)]')"
fi

if [ "$FORMAT" = "text" ]; then
    printf '%s' "$entries" | jq -r '.[] | "\(.stack) \(.region) \(.site_path)"'
elif [ "$FORMAT" = "app-host" ]; then
    # Every entry carries the same value (enforced above), so the first is the
    # answer; an empty line when no config sets one.
    printf '%s\n' "$(printf '%s' "$entries" | jq -r '.[0].app_host // ""')"
else
    printf '%s\n' "$(printf '%s' "$entries" | jq -c .)"
fi
