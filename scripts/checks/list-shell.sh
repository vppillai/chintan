#!/usr/bin/env bash
# List every shell script, for shellcheck and shfmt. One list, so the linter and
# the formatter cannot disagree about what is covered.
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
shell_files
