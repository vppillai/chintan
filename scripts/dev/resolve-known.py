#!/usr/bin/env python3
"""Resolve rebase conflicts in files where keeping both sides is right (append-only tables and import lists).

Run inside the worktree being rebased. Exit 0 when every unmerged path was
settled, 2 when one needs a human — an unknown file, or a known one in a state
with no two sides to keep (deleted on one side, added on one side) — so the
merge drivers STOP instead of retrying a rebase that cannot continue.
`--self-test` proves both answers in a scratch repository.
"""
import os, re, shutil, subprocess, sys, tempfile
KEEP_BOTH = {"docs/backlog.md", "docs/history/reviews/README.md", "frontend/src/styles/index.css", "frontend/src/components/Icon.tsx"}
TAKE_THEIRS_FROM_MAIN = {"frontend/src/api/__fixtures__/responses.ts", "frontend/src/api/__fixtures__/requests.json"}
# Every status `git status --short` gives an unmerged path; only the first two have both sides in the file.
BOTH_SIDES = ("UU", "AA")
ONE_SIDED = ("DD", "AU", "UD", "UA", "DU")


def resolve():
    out = subprocess.run(["git", "status", "--short"], capture_output=True, text=True).stdout
    bad = False
    # docs/backlog-open.md sorts before docs/backlog.md; the view must be regenerated after the ledger is resolved.
    lines = sorted(out.splitlines(), key=lambda l: l.endswith("docs/backlog-open.md"))
    for line in lines:
        status, path = line[:2], line[3:].strip()
        if status in ONE_SIDED:
            print("NEEDS HUMAN:", status, path); bad = True
            continue
        if status not in BOTH_SIDES: continue
        if path in KEEP_BOTH:
            s = open(path).read()
            def both(m):
                # Keep both sides, but not twice: identical lines (the same appended row on
                # each side) and a table header that would follow another header are dropped.
                a = m.group(1).splitlines(True); b = m.group(2).splitlines(True)
                seen = set(a); out = list(a)
                for line in b:
                    if line in seen: continue
                    if line.startswith("|---") and out and out[-1].startswith("|---"): continue
                    hdr = lambda l: l.lower().startswith(("| # |", "| id |"))
                    if hdr(line) and out and hdr(out[-1]): continue
                    out.append(line); seen.add(line)
                return "".join(out)
            s2 = re.sub(r"<<<<<<< [^\n]*\n(.*?)=======\n(.*?)>>>>>>> [^\n]*\n", both, s, flags=re.S)
            open(path, "w").write(s2); subprocess.run(["git", "add", path]); print("resolved:", path)
        elif path == "docs/backlog-open.md":
            # Generated from docs/backlog.md (resolved above, keep-both): regenerate rather than merge.
            subprocess.run(["python3", "scripts/backlog-view.py"], check=False)
            subprocess.run(["git", "add", path]); print("regenerated:", path)
        elif path in TAKE_THEIRS_FROM_MAIN:
            # during a rebase "ours" is the branch being rebased onto (main)
            subprocess.run(["git", "checkout", "--ours", path]); subprocess.run(["git", "add", path]); print("resolved (main's):", path)
        else:
            print("NEEDS HUMAN:", path); bad = True
    return 2 if bad else 0


def self_test():
    """A both-appended ledger resolves (0); a file main deleted and the branch edited does not (2)."""
    def git(*args):
        return subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@example.invalid", *args], capture_output=True, text=True)
    home, made = os.getcwd(), []
    def conflict(path, main_edit, branch_edit):
        d = tempfile.mkdtemp(); made.append(d); os.chdir(d); os.makedirs("docs")
        git("init", "-q", "-b", "main"); open(path, "w").write("| id |\n|---|\n| 1 |\n"); git("add", "."); git("commit", "-qm", "base")
        git("checkout", "-qb", "branch"); branch_edit(path); git("add", "-A"); git("commit", "-qm", "branch")
        git("checkout", "-q", "main"); main_edit(path); git("add", "-A"); git("commit", "-qm", "main")
        git("checkout", "-q", "branch"); git("rebase", "main")
    append = lambda row: lambda p: open(p, "a").write(row)
    conflict("docs/backlog.md", append("| 2 |\n"), append("| 3 |\n"))
    assert resolve() == 0 and open("docs/backlog.md").read().endswith("| 2 |\n| 3 |\n"), "a both-appended ledger must resolve to both rows"
    conflict("docs/backlog.md", os.remove, append("| 3 |\n"))
    assert resolve() == 2, "a modify/delete conflict must exit 2, or the drivers retry the rebase for ever"
    os.chdir(home)
    for d in made: shutil.rmtree(d, ignore_errors=True)
    print("self-test ok")


if __name__ == "__main__":
    if sys.argv[1:] == ["--self-test"]:
        self_test(); sys.exit(0)
    sys.exit(resolve())
