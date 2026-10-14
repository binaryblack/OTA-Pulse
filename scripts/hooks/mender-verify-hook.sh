#!/usr/bin/env bash
#
# mender-verify-hook.sh -- pre-commit guard for the vendored upstream layers
# (TASK-S109-004). Runs `scripts/mender-sync.sh --verify` against exactly what is about
# to be committed, and only when a relevant path is part of the commit, so unrelated
# commits stay fast. Checked in here; installed by the parent repo's scripts/setup.sh
# (`./scripts/setup.sh hooks`).
#
# Two ways it is called:
#   (no args), cwd inside the ota-pulse checkout
#       The submodule's own pre-commit hook. Looks at the STAGED file list; if anything under
#       meta-otapulse-bsp/, tools/otapulse-convert/, patches/mender/, mender-upstream.lock, mender-upstream.manifests/
#       or the sync tooling itself is staged, it exports the INDEX versions of those paths
#       to a temp dir and verifies that (MENDER_SYNC_ROOT), so an unstaged working-tree edit
#       can neither hide nor cause a failure.
#   --gitlink <parent-repo-root>
#       The parent (TETS-ARE) repo's pre-commit hook, called when the `ota-pulse` gitlink is
#       staged. Resolves the staged submodule commit and verifies the tree of THAT commit.
#       Skipped when the commit does not touch the guarded paths relative to the old
#       gitlink, or when the commit is not available locally yet (then the submodule's own
#       hook already covered it when it was committed).
#
# Exit 0 = fine or not applicable; non-zero = blocked.
#
set -euo pipefail

HOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SYNC="$HOOK_DIR/../mender-sync.sh"
GUARDED_PATHS=(meta-otapulse-bsp tools/otapulse-convert mender-upstream.lock mender-upstream.manifests patches/mender
    scripts/mender-sync.sh scripts/mender_tree_hash.py scripts/mender_lock.py)
GUARD_RE='^(meta-otapulse-bsp/|tools/otapulse-convert/|mender-upstream\.lock$|mender-upstream\.manifests/|patches/mender/|scripts/mender-sync\.sh$|scripts/mender_tree_hash\.py$|scripts/mender_lock\.py$)'

TMP=""
cleanup() { if [ -n "$TMP" ]; then rm -rf -- "$TMP"; fi; }
trap cleanup EXIT

blocked() {
    {
        echo ""
        echo "Commit blocked: the vendored upstream layers (meta-otapulse-bsp/) no longer match"
        echo "mender-upstream.lock. Vendored files are never edited by hand: put the change in"
        echo "patches/mender/<component>/ and run scripts/mender-sync.sh --sync <component> <ref>."
        echo "See patches/mender/README.md."
    } >&2
    exit 1
}

if [ "${1:-}" = "--gitlink" ]; then
    parent="${2:?--gitlink needs the parent repo root}"
    new="$(git -C "$parent" ls-files -s -- ota-pulse | awk '$1 == "160000" {print $2}')"
    [ -n "$new" ] || exit 0
    sub="$(cd "$HOOK_DIR/../.." && pwd)"
    git -C "$sub" cat-file -e "$new^{commit}" 2>/dev/null || exit 0
    git -C "$sub" cat-file -e "$new:mender-upstream.lock" 2>/dev/null || exit 0
    old="$(git -C "$parent" ls-tree HEAD -- ota-pulse 2>/dev/null | awk '$1 == "160000" {print $3}')"
    if [ -n "$old" ] && git -C "$sub" cat-file -e "$old^{commit}" 2>/dev/null \
        && git -C "$sub" diff --quiet "$old" "$new" -- "${GUARDED_PATHS[@]}"; then
        exit 0
    fi
    TMP="$(mktemp -d "${TMPDIR:-/tmp}/mender-verify-hook.XXXXXX")"
    present=()
    for p in "${GUARDED_PATHS[@]}"; do
        if git -C "$sub" cat-file -e "$new:$p" 2>/dev/null; then present+=("$p"); fi
    done
    git -C "$sub" archive "$new" -- "${present[@]}" | tar -x -C "$TMP"
    echo "mender-verify-hook: verifying vendored layers of ota-pulse commit ${new:0:12}"
    MENDER_SYNC_ROOT="$TMP" "$SYNC" --verify || blocked
    exit 0
fi

top="$(git rev-parse --show-toplevel)"
cd "$top"
# -z: paths are NUL separated (no quoting of odd names); every embedded newline just
# yields extra "lines", the first of which still starts with the guarded prefix. No -q on
# grep: an early exit would SIGPIPE git under pipefail and read as "no match".
if ! git diff --cached --name-only -z | tr '\0' '\n' | grep -E "$GUARD_RE" >/dev/null; then
    exit 0
fi
TMP="$(mktemp -d "${TMPDIR:-/tmp}/mender-verify-hook.XXXXXX")"
git ls-files -z -- "${GUARDED_PATHS[@]}" | xargs -0 -r git checkout-index --prefix="$TMP/" --
echo "mender-verify-hook: vendored-layer paths are staged; running mender-sync.sh --verify on the index"
MENDER_SYNC_ROOT="$TMP" "$SYNC" --verify || blocked
