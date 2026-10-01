#!/usr/bin/env bash
# mk-sync.sh <worktree-dir-name> <mirror-suffix> — writes <suffix>-sync.sh, a
# script that mirrors the worktree to the Linux VM where the toolchains live:
# tar it over (no .git, node_modules, dist), delete on the mirror what is gone
# locally, and print a manifest hash both sides agree on. Run the generated
# script after every batch of edits; the mirror is <orb>:~/temp/chintan-<suffix>.
#
#   CHINTAN_WT_ROOT   where the worktrees live  (default: three levels up from this script)
#   CHINTAN_SYNC_OUT  where to write the script (default: the current directory)
#   CHINTAN_ORB       the ssh destination        (default: ubuntu@orb)
set -eu
T=$(cd "$(dirname "$0")" && pwd)
WT_ROOT=${CHINTAN_WT_ROOT:-$(cd "$T/../../.." && pwd)}
WT=$WT_ROOT/$1
SUF=$2
ORB=${CHINTAN_ORB:-ubuntu@orb}
OUT=${CHINTAN_SYNC_OUT:-$(pwd)}/$SUF-sync.sh
TMP=${TMPDIR:-/tmp}
[ -d "$WT" ] || {
    echo "no worktree at $WT" >&2
    exit 1
}
cat >"$OUT" <<EOS
#!/usr/bin/env bash
set -eu
cd "$WT"
ssh "$ORB" 'mkdir -p ~/temp/chintan-$SUF'
COPYFILE_DISABLE=1 tar --exclude=.git --exclude=node_modules --exclude=dist --exclude='._*' -cf - . | ssh "$ORB" 'cd ~/temp/chintan-$SUF && tar -xf -'
# prune: files present on the mirror (outside node_modules/dist) but gone locally. A worktree's .git is a file, so it is named too.
( cd "$WT" && find . -type f -not -path './.git' -not -path './.git/*' -not -path '*/node_modules/*' -not -path '*/dist/*' | LC_ALL=C sort ) > "$TMP/$SUF-local.txt"
ssh "$ORB" "cd ~/temp/chintan-$SUF && find . -type f -not -path '*/node_modules/*' -not -path '*/dist/*' | LC_ALL=C sort" > "$TMP/$SUF-remote.txt"
LC_ALL=C comm -13 "$TMP/$SUF-local.txt" "$TMP/$SUF-remote.txt" | ssh "$ORB" "cd ~/temp/chintan-$SUF && xargs -r rm -f"
L=\$(cd "$WT" && xargs md5 -q < "$TMP/$SUF-local.txt" 2>/dev/null | tr -d '\n' | md5 -q)
R=\$(cd "$WT" && ssh "$ORB" "cd ~/temp/chintan-$SUF && xargs md5sum | awk '{print \\\$1}' | tr -d '\n' | md5sum | awk '{print \\\$1}'" < "$TMP/$SUF-local.txt")
N=\$(wc -l < "$TMP/$SUF-local.txt" | tr -d ' ')
echo "$SUF synced (\$N files); local-manifest-hash=\${L:0:8} (remote check: \${R:0:8})"
EOS
chmod +x "$OUT"
echo "wrote $OUT"
