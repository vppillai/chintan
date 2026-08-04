#!/usr/bin/env bash
#
# Runs shellcheck over every script. §11.3 imposes conventions on every script
# without exception, and the ones a linter can catch — unquoted expansions, unchecked cd,
# broken test syntax — are exactly the ones that turn a --dry-run into an
# accidental --apply.
#
# -x follows `source` directives so the shared lib is analysed in context.
# -S style is the strictest level; these scripts mutate production data, which is
# the code that must not be untested or unlinted (§11.5).
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
cd "$REPO_ROOT" || exit 1
mapfile -t files < <(shell_files)
if [ "${#files[@]}" -eq 0 ]; then
    die "no shell scripts found; this check would pass vacuously"
fi
shellcheck --external-sources --severity=style --shell=bash "${files[@]}"
ok "shellcheck: ${#files[@]} script(s) clean"
