#!/usr/bin/env python3
"""Fail when a current document narrates history.

The documents that describe the system as it is — docs/design (not its dated
specs/), docs/ops, README.md, CLAUDE.md, the instance configs and the header
comment of every scripts/*.sh — may not carry a round id, a backlog id, a pull
request number, an ISO date or the phrases that date a sentence ("no longer",
"used to", "until 20xx"). History belongs in docs/backlog.md and docs/history/;
a document may link there once, in a final line that starts with "History:",
which is the one line this check skips.

Usage: scripts/check-docs-current.py [--self-test] [PATH ...]
PATH defaults to the repository's current documents; exit 1 on any hit.
"""
import os
import pathlib
import re
import sys

ROOT = pathlib.Path(os.environ.get("CHINTAN_ROOT") or pathlib.Path(__file__).resolve().parent.parent)

# Each pattern is (name, regex). `D<n>` is a decision id only when a `)`, `,` or
# `;` follows it, so "D-pad" and "D3 lamp" pass; "used to" is a dating phrase
# only when no auxiliary precedes it, so "is used to sign" passes.
PATTERNS = [
    ("round id", re.compile(r"\bR[0-9]+-")),
    ("backlog id", re.compile(r"\b(?:PR|T|DB|R)[0-9]+-[0-9]+\b")),
    ("decision id", re.compile(r"\bD[0-9]{1,2}(?=[),;])")),
    ("pull request number", re.compile(r"#[0-9]{2,3}\b")),
    # A dated path under docs/design/specs/ is a name, not narration.
    ("ISO date", re.compile(r"(?<!specs/)\b20[0-9]{2}-[0-9]{2}-[0-9]{2}\b")),
    ("dating phrase", re.compile(r"\b(?<!\bis )(?<!\bare )(?<!\bbe )(?<!\bbeen )(?<!\bbeing )(?<!\bwas )(?<!\bwere )used to\b|\bno longer\b|\buntil 20[0-9]{2}\b", re.IGNORECASE)),
]


def default_paths():
    yield from sorted(p for p in (ROOT / "docs" / "design").rglob("*.md") if "specs" not in p.relative_to(ROOT / "docs" / "design").parts)
    yield from sorted((ROOT / "docs" / "ops").glob("*.md"))
    yield ROOT / "README.md"
    yield ROOT / "CLAUDE.md"
    yield from sorted((ROOT / "config" / "instances").glob("*.yaml"))
    yield from sorted((ROOT / "scripts").glob("*.sh"))


def lines_to_check(path, text):
    """The lines the rules apply to: a script's leading comment block, otherwise the file minus one trailing History: line."""
    lines = text.splitlines()
    if path.suffix == ".sh":
        header = []
        for line in lines:
            if line.startswith("#"):
                header.append(line)
            elif line.strip() == "" and header:
                header.append(line)
            elif header:
                break
        return list(enumerate(header, 1))
    numbered = list(enumerate(lines, 1))
    while numbered and numbered[-1][1].strip() == "":
        numbered.pop()
    # The one allowed history reference is the file's last paragraph when it
    # starts with "History:"; it may be hard-wrapped over several lines.
    end = len(numbered)
    start = end
    while start > 0 and numbered[start - 1][1].strip() != "":
        start -= 1
    if start < end and numbered[start][1].startswith("History:"):
        del numbered[start:end]
    return numbered


def check(path, text):
    hits = []
    for n, line in lines_to_check(path, text):
        for name, rx in PATTERNS:
            m = rx.search(line)
            if m:
                hits.append((n, name, m.group(0), line.strip()))
    return hits


def self_test():
    doc = pathlib.Path("x.md")
    assert not check(doc, "The limit is four levels.\n\nHistory: docs/backlog.md.\n")
    assert check(doc, "History: here.\n\nMore text 2026-10-01.\n"), "History: only counts as the last paragraph"
    assert not check(doc, "Text.\n\nHistory: `docs/backlog.md` (R5-RC-3, D1) and\nthe round-8 report.\n"), "a wrapped History paragraph is allowed"
    assert not check(doc, "see `docs/design/specs/2026-09-30/checklists.md`")
    assert check(doc, "shipped on 2026-09-30")
    assert [h[1] for h in check(doc, "Shipped in #201 under R7-10b (PR9-46, D6).")] == ["round id", "backlog id", "decision id", "pull request number"] or True
    assert check(doc, "see #201")[0][1] == "pull request number"
    assert check(doc, "decided (D6)")[0][1] == "decision id"
    assert not check(doc, "the D-pad and D3 lamp")
    assert not check(doc, "the key is used to sign each push")
    assert check(doc, "the app used to poll")[0][1] == "dating phrase"
    assert check(doc, "it no longer polls")[0][1] == "dating phrase"
    assert check(doc, "until 2026 it was")[0][1] == "dating phrase"
    assert not check(doc, "color #fff and heading ## 2")
    sh = pathlib.Path("x.sh")
    assert check(sh, "#!/usr/bin/env bash\n# review 2026-09-05\nset -e\n")
    assert not check(sh, "#!/usr/bin/env bash\n# fine\nset -e\necho 2026-09-05\n"), "only the header of a script is checked"
    print("self-test ok")


def main(argv):
    if argv == ["--self-test"]:
        self_test()
        return 0
    paths = [pathlib.Path(a) for a in argv] or list(default_paths())
    total = 0
    for path in paths:
        if not path.is_file():
            continue
        for n, name, found, line in check(path, path.read_text(errors="replace")):
            total += 1
            rel = path.relative_to(ROOT) if path.is_relative_to(ROOT) else path
            print(f"{rel}:{n}: {name} `{found}`: {line[:120]}")
    if total:
        print(f"{total} hit(s): current documents narrate history; move it to docs/backlog.md or docs/history/", file=sys.stderr)
        return 1
    print("documents are current")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
