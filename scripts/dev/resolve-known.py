#!/usr/bin/env python3
"""Resolve rebase conflicts in files where keeping both sides is right (append-only tables and import lists)."""
import re, subprocess, sys
KEEP_BOTH = {"docs/backlog.md", "docs/reviews/README.md", "frontend/src/styles/index.css", "frontend/src/components/Icon.tsx"}
TAKE_THEIRS_FROM_MAIN = {"frontend/src/api/__fixtures__/responses.ts", "frontend/src/api/__fixtures__/requests.json"}
out = subprocess.run(["git", "status", "--short"], capture_output=True, text=True).stdout
bad = False
# docs/backlog-open.md sorts before docs/backlog.md; the view must be regenerated after the ledger is resolved.
lines = sorted(out.splitlines(), key=lambda l: l.endswith("docs/backlog-open.md"))
for line in lines:
    if not line[:2] in ("UU", "AA"): continue
    path = line[3:].strip()
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
sys.exit(2 if bad else 0)
