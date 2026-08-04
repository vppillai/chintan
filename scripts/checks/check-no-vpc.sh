#!/usr/bin/env bash
# §0.5A inventory entry. Body shared with the other template checks in
# check-infra-invariants.sh — one parser, so the four cannot drift apart.
exec "$(dirname "${BASH_SOURCE[0]}")/check-infra-invariants.sh" no-vpc "$@"
