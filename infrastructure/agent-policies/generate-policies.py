#!/usr/bin/env python3
"""Generate boundary.json and deny.json from policy-source.json.

The two policies were maintained by hand as near-identical 300-line files. They
differ by exactly one statement: the boundary opens with a ceiling of allowed
services, and the deny policy has no Allow at all. Keeping both by hand meant
every guardrail edit had to be made twice, and nothing checked that it was.

Run with no arguments to write both files. Run with --check to verify the files
on disk match the source, which is what CI does.

An IAM managed policy may not exceed MAX_POLICY_CHARS characters, and IAM does
not count whitespace. boundary.json sits close enough to that limit that a
careless addition would fail at deploy time, after the guardrail was already
believed to be in place. This script fails first, here, with the number.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent
SOURCE = HERE / "policy-source.json"
BOUNDARY = HERE / "boundary.json"
DENY = HERE / "deny.json"
# Not generated, but attached as managed policies and so bound by the same
# ceiling. Checked here because there is nowhere else that counts them.
HAND_WRITTEN = [HERE / "permissions.json"]

# https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_iam-quotas.html
MAX_POLICY_CHARS = 6144
# Warn while there is still room to act. Below this, the next guardrail
# statement will not fit and the policy needs splitting or tightening first.
LOW_HEADROOM_CHARS = 256


def policy_size(document: dict) -> int:
    """Characters IAM counts: the document with whitespace removed."""
    return len(json.dumps(document, separators=(",", ":")))


def build(source: dict) -> dict[pathlib.Path, dict]:
    version = source["Version"]
    return {
        BOUNDARY: {"Version": version, "Statement": [source["Ceiling"]] + source["Shared"]},
        DENY: {"Version": version, "Statement": source["Shared"]},
    }


def render(document: dict) -> str:
    return json.dumps(document, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="verify the generated files are current instead of writing them",
    )
    args = parser.parse_args()

    source = json.loads(SOURCE.read_text())
    generated = build(source)

    measured = dict(generated)
    for path in HAND_WRITTEN:
        measured[path] = json.loads(path.read_text())

    over_limit = False
    for path, document in measured.items():
        size = policy_size(document)
        headroom = MAX_POLICY_CHARS - size
        note = ""
        if headroom < 0:
            note = f"  OVER THE {MAX_POLICY_CHARS}-CHARACTER IAM LIMIT"
            over_limit = True
        elif headroom < LOW_HEADROOM_CHARS:
            note = "  (low headroom)"
        print(f"{path.name}: {size} chars, {headroom} remaining{note}")
    if over_limit:
        print("\nAn oversized policy cannot be attached. Tighten it before committing.", file=sys.stderr)
        return 1

    drifted = []
    for path, document in generated.items():
        text = render(document)
        if args.check:
            if not path.exists() or path.read_text() != text:
                drifted.append(path.name)
        else:
            path.write_text(text)

    if args.check and drifted:
        print(
            f"\n{', '.join(drifted)} differ(s) from policy-source.json. "
            f"Run {pathlib.Path(__file__).name} and commit the result.",
            file=sys.stderr,
        )
        return 1

    print("\nup to date" if args.check else "\nwritten")
    return 0


if __name__ == "__main__":
    sys.exit(main())
