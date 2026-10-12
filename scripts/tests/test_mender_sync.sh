#!/usr/bin/env bash
#
# Tests for scripts/mender-sync.sh (TASK-S109-004).
#
# Hermetic by default: the "upstreams" are local bare git repos reached through the
# MENDER_SYNC_URL_<COMPONENT> override, and the script is pointed at a scratch root via
# MENDER_SYNC_ROOT, so no network is used and the real vendored trees are never modified.
#
# Optional networked tests (real https upstreams, no credentials), only with
#   MENDER_SYNC_TEST_ONLINE=1 scripts/tests/test_mender_sync.sh
# They run against a scratch COPY of the real lock + vendored trees.
#
# Usage: scripts/tests/test_mender_sync.sh      (exit 0 = all passed)
#
# `cond && pass "x" || bad "x"` is deliberate throughout: pass() always returns 0, so the
# SC2015 "C may run when A is true" hazard cannot occur.
# shellcheck disable=SC2015
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OTA_ROOT="$(cd "$HERE/../.." && pwd)"
SYNC="$OTA_ROOT/scripts/mender-sync.sh"
TREEPY="$OTA_ROOT/scripts/mender_tree_hash.py"

T="$(mktemp -d "${TMPDIR:-/tmp}/test-mender-sync.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT

PASS=0
FAILED=0
OUT=""
RC=0

pass() { PASS=$((PASS + 1)); printf '  ok   %s\n' "$1"; }
bad()  { FAILED=$((FAILED + 1)); printf '  FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '       %s\n' "$2"; }
section() { printf '\n== %s\n' "$1"; }

# run <cmd...>: capture combined output in OUT and exit status in RC
run() { OUT="$("$@" 2>&1)" && RC=0 || RC=$?; }

expect_rc0()   { if [ "$RC" -eq 0 ]; then pass "$1"; else bad "$1" "expected exit 0, got $RC: $(printf '%s' "$OUT" | tail -n 5 | tr '\n' '|')"; fi; }
expect_rcnz()  { if [ "$RC" -ne 0 ]; then pass "$1"; else bad "$1" "expected non-zero exit, got 0"; fi; }
expect_out()   { if printf '%s' "$OUT" | grep -qF -- "$2"; then pass "$1"; else bad "$1" "output lacks '$2': $(printf '%s' "$OUT" | tail -n 6 | tr '\n' '|')"; fi; }
expect_no_out(){ if printf '%s' "$OUT" | grep -qF -- "$2"; then bad "$1" "output unexpectedly contains '$2'"; else pass "$1"; fi; }
expect_eq()    { if [ "$2" = "$3" ]; then pass "$1"; else bad "$1" "'$2' != '$3'"; fi; }

GITC=(git -c user.name=test -c user.email=test@example.invalid -c commit.gpgsign=false -c tag.gpgsign=false)

snap() { tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --exclude=.git -cf - -C "$1" . | sha256sum | cut -d' ' -f1; }
lock_col() { # <lock> <component> <col>
    awk -F'\t' -v c="$2" -v n="$3" '$1 == c {print $n}' "$1"
}

# ---------------------------------------------------------------------------
# fixtures: three fake upstream repos
# ---------------------------------------------------------------------------
mk_fixtures() {
    local w="$T/w" up="$T/up"
    mkdir -p "$w" "$up"

    # --- fake meta-mender: two layers + an (ignored) commercial dir + repo-root LICENSE
    git init -q -b main "$w/mm"
    (
        cd "$w/mm" || exit 1
        mkdir -p meta-mender-core/conf meta-mender-core/recipes meta-mender-core/scripts \
            meta-mender-qemu/conf meta-mender-commercial
        printf 'LICENSE v1 (fake upstream)\n' >LICENSE
        printf 'BBFILE_COLLECTIONS += "core"\n' >meta-mender-core/conf/layer.conf
        seq -f 'line%g' 1 14 >meta-mender-core/recipes/foo.bb
        printf '#!/bin/sh\necho hi\n' >meta-mender-core/scripts/run.sh
        chmod 755 meta-mender-core/scripts/run.sh
        ln -s recipes/foo.bb meta-mender-core/link.bb
        printf 'BBFILE_COLLECTIONS += "qemu"\n' >meta-mender-qemu/conf/layer.conf
        printf 'proprietary\n' >meta-mender-commercial/secret.txt
        git add -A && "${GITC[@]}" commit -qm v1
        "${GITC[@]}" tag -a scarthgap-v2026.09 -m "release 2026.09"
        sed -i 's/^line2$/line2-upstream-changed/' meta-mender-core/recipes/foo.bb
        echo line15 >>meta-mender-core/recipes/foo.bb
        printf 'LICENSE v2 (fake upstream)\n' >LICENSE
        git add -A && "${GITC[@]}" commit -qm v2
        "${GITC[@]}" tag -a scarthgap-v2026.10 -m "release 2026.10"
    )
    git clone -q --bare "$w/mm" "$up/mm.git"

    # --- fake meta-mender-community: branch scarthgap, no tags
    git init -q -b scarthgap "$w/mc"
    (
        cd "$w/mc" || exit 1
        mkdir -p meta-mender-raspberrypi/conf meta-mender-nxp/conf meta-mender-demo
        printf 'COMMUNITY LICENSE v1\n' >LICENSE
        printf 'rpi layer v1\n' >meta-mender-raspberrypi/conf/layer.conf
        printf 'nxp layer v1\n' >meta-mender-nxp/conf/layer.conf
        printf 'not vendored\n' >meta-mender-demo/x
        git add -A && "${GITC[@]}" commit -qm c1
        git rev-parse HEAD >"$T/mc.sha1"
        printf 'rpi layer v2\n' >meta-mender-raspberrypi/conf/layer.conf
        git add -A && "${GITC[@]}" commit -qm c2
        git rev-parse HEAD >"$T/mc.sha2"
    )
    git clone -q --bare "$w/mc" "$up/mc.git"

    # --- fake grub-mender-grubenv: standalone repo with its own LICENSE
    git init -q -b master "$w/gg"
    (
        cd "$w/gg" || exit 1
        printf 'GRUB LICENSE\n' >LICENSE
        printf 'set a=1\n' >10_boot.cfg
        git add -A && "${GITC[@]}" commit -qm g1
        git rev-parse HEAD >"$T/gg.sha1"
    )
    git clone -q --bare "$w/gg" "$up/gg.git"

    local r
    for r in mm mc gg; do git -C "$up/$r.git" config uploadpack.allowAnySHA1InWant true; done

    export MENDER_SYNC_URL_META_MENDER_CORE="file://$up/mm.git"
    export MENDER_SYNC_URL_META_MENDER_QEMU="file://$up/mm.git"
    export MENDER_SYNC_URL_META_MENDER_RASPBERRYPI="file://$up/mc.git"
    export MENDER_SYNC_URL_META_MENDER_NXP="file://$up/mc.git"
    export MENDER_SYNC_URL_GRUB_MENDER_GRUBENV="file://$up/gg.git"
}

# make a patch (git diff against a scratch repo of the pristine tree) for meta-mender-core
# mk_core_patch <out-file> <old-text> <new-text>   (replaces one line of recipes/foo.bb)
mk_core_patch() {
    local out="$1" old="$2" new="$3" pc="$T/pc.$RANDOM"
    git clone -q "file://$T/up/mm.git" "$pc.src"
    git -C "$pc.src" checkout -q scarthgap-v2026.09
    mkdir "$pc"
    cp -a "$pc.src/meta-mender-core/." "$pc/"
    (
        cd "$pc" || exit 1
        git init -q && git add -A && "${GITC[@]}" commit -qm base
        sed -i "s/^$old\$/$new/" recipes/foo.bb
        {
            printf 'Test patch: change %s to %s.\n\n' "$old" "$new"
            git diff
        } >"$out"
    )
    rm -rf "$pc" "$pc.src"
}

ms() { MENDER_SYNC_ROOT="$ROOT" "$SYNC" "$@"; }
COMPONENTS=(meta-mender-core meta-mender-qemu meta-mender-raspberrypi meta-mender-nxp grub-mender-grubenv)

mk_root() {
    ROOT="$T/root"
    rm -rf "$ROOT"
    mkdir -p "$ROOT"
    grep '^#' "$OTA_ROOT/mender-upstream.lock" >"$ROOT/mender-upstream.lock"
    local c
    for c in "${COMPONENTS[@]}"; do mkdir -p "$ROOT/patches/mender/$c"; done
}

# ---------------------------------------------------------------------------
section "bootstrap: --sync into an empty scratch root"
mk_fixtures
mk_root
run ms --sync meta-mender-core scarthgap-v2026.09;  expect_rc0 "sync core by annotated tag"
run ms --sync meta-mender-qemu scarthgap-v2026.09;  expect_rc0 "sync qemu by annotated tag"
run ms --sync meta-mender-raspberrypi "$(cat "$T/mc.sha1")"; expect_rc0 "sync raspberrypi by full sha"
run ms --sync meta-mender-nxp scarthgap;            expect_rc0 "sync nxp by branch name"
run ms --sync grub-mender-grubenv master;           expect_rc0 "sync grub-mender-grubenv by branch name"
run ms --verify;                                    expect_rc0 "--verify passes after bootstrap"
expect_out "verify says OK" "--verify OK"
[ ! -e "$ROOT/meta-otapulse-bsp/core/meta-mender-commercial" ] && pass "commercial dir of the fake upstream not vendored" || bad "commercial dir vendored"
[ ! -d "$ROOT/meta-otapulse-bsp/core/.git" ] && pass "no .git in vendored tree" || bad ".git present"
[ -L "$ROOT/meta-otapulse-bsp/core/link.bb" ] && pass "symlink preserved" || bad "symlink lost"
[ -x "$ROOT/meta-otapulse-bsp/core/scripts/run.sh" ] && pass "exec bit preserved" || bad "exec bit lost"
expect_eq "LICENSE added for core (v1)" "$(cat "$ROOT/meta-otapulse-bsp/core/LICENSE")" "LICENSE v1 (fake upstream)"
expect_eq "grub keeps its own LICENSE; license col is '-'" "$(lock_col "$ROOT/mender-upstream.lock" grub-mender-grubenv 10)" "-"
expect_eq "raspberrypi labelled branch:scarthgap" "$(lock_col "$ROOT/mender-upstream.lock" meta-mender-raspberrypi 3)" "branch:scarthgap"
expect_eq "nxp pinned at the second commit" "$(lock_col "$ROOT/mender-upstream.lock" meta-mender-nxp 4)" "$(cat "$T/mc.sha2")"
# the real UPSTREAM files carry a "the tag object is <sha>" note; give the scratch one too
printf 'note: the tag object is %s; meta-mender-commercial is NEVER vendored\n' \
    "$(git -C "$T/w/mm" rev-parse scarthgap-v2026.09)" >>"$ROOT/meta-otapulse-bsp/core/UPSTREAM"
run ms --verify; expect_rc0 "verify unaffected by the UPSTREAM note"
(cd "$ROOT" && git init -q && git add -A && "${GITC[@]}" commit -qm baseline)

# pristine hash of the fake core tree computed independently (git archive of the tag)
mkdir "$T/arch"
git -C "$T/w/mm" archive scarthgap-v2026.09 meta-mender-core | tar -x -C "$T/arch"
EXPECT_PRISTINE_CORE="$(python3 "$TREEPY" "$T/arch/meta-mender-core")"
expect_eq "lock tree-sha256 equals an independent hash of the upstream subtree" \
    "$(lock_col "$ROOT/mender-upstream.lock" meta-mender-core 7)" "$EXPECT_PRISTINE_CORE"

# ---------------------------------------------------------------------------
section "(1) sync to the already-pinned ref is a no-op"
before_snap="$(snap "$ROOT")"; before_lock="$(sha256sum "$ROOT/mender-upstream.lock")"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rc0 "sync core to its pinned tag"
expect_out "reports no change" "no change"
expect_eq "git status clean (empty diff)" "$(git -C "$ROOT" status --porcelain)" ""
expect_eq "lock byte-identical" "$(sha256sum "$ROOT/mender-upstream.lock")" "$before_lock"
run ms --sync meta-mender-nxp "$(cat "$T/mc.sha2")"
expect_rc0 "sync nxp to its pinned full sha"
expect_eq "still empty diff (sha form)" "$(git -C "$ROOT" status --porcelain)" ""
run ms --sync grub-mender-grubenv master
expect_eq "still empty diff (branch form)" "$(git -C "$ROOT" status --porcelain)" ""
expect_eq "whole scratch root snapshot unchanged" "$(snap "$ROOT")" "$before_snap"

section "dry-run changes nothing"
run ms --sync meta-mender-core scarthgap-v2026.10 --dry-run
expect_rc0 "dry-run to the next tag"
expect_out "dry-run says so" "dry-run"
expect_eq "snapshot unchanged after dry-run" "$(snap "$ROOT")" "$before_snap"

# ---------------------------------------------------------------------------
section "(2) tampered vendored file -> --verify names it"
F="$ROOT/meta-otapulse-bsp/core/recipes/foo.bb"
cp "$F" "$T/foo.bb.orig"
echo "tampered" >>"$F"
run ms --verify
expect_rcnz "verify fails on a modified file"
expect_out "names the modified file" "meta-otapulse-bsp/core/recipes/foo.bb"
cp "$T/foo.bb.orig" "$F"
echo x >"$ROOT/meta-otapulse-bsp/qemu/extra-file.txt"
run ms --verify
expect_rcnz "verify fails on an extra file"
expect_out "names the extra file" "meta-otapulse-bsp/qemu/extra-file.txt"
rm "$ROOT/meta-otapulse-bsp/qemu/extra-file.txt"
rm "$ROOT/meta-otapulse-bsp/qemu/conf/layer.conf"
run ms --verify
expect_rcnz "verify fails on a deleted file"
expect_out "names the deleted file" "meta-otapulse-bsp/qemu/conf/layer.conf"
git -C "$ROOT" checkout -q -- meta-otapulse-bsp/qemu/conf/layer.conf
chmod 755 "$ROOT/meta-otapulse-bsp/core/conf/layer.conf"
run ms --verify
expect_rcnz "verify fails on a mode change"
expect_out "names the mode-changed file" "meta-otapulse-bsp/core/conf/layer.conf"
chmod 644 "$ROOT/meta-otapulse-bsp/core/conf/layer.conf"
run ms --verify
expect_rc0 "verify passes again once restored"

# ---------------------------------------------------------------------------
section "(7) LICENSE checks"
cp "$ROOT/meta-otapulse-bsp/boards/nxp/LICENSE" "$T/lic.orig"
echo "tampered licence" >"$ROOT/meta-otapulse-bsp/boards/nxp/LICENSE"
run ms --verify
expect_rcnz "verify fails when the added LICENSE differs from the recorded sha256"
expect_out "names the LICENSE path" "meta-otapulse-bsp/boards/nxp/LICENSE"
cp "$T/lic.orig" "$ROOT/meta-otapulse-bsp/boards/nxp/LICENSE"
rm "$ROOT/meta-otapulse-bsp/boards/nxp/LICENSE"
run ms --verify
expect_rcnz "verify fails when the added LICENSE is missing"
cp "$T/lic.orig" "$ROOT/meta-otapulse-bsp/boards/nxp/LICENSE"
cp "$ROOT/mender-upstream.lock" "$T/lock.orig"
awk -F'\t' -v OFS='\t' '$1 == "meta-mender-qemu" {$10 = "0000000000000000000000000000000000000000000000000000000000000000"} {print}' "$T/lock.orig" >"$ROOT/mender-upstream.lock"
run ms --verify
expect_rcnz "verify fails when the lock's license-sha256 does not match"
expect_out "names the qemu LICENSE" "meta-otapulse-bsp/qemu/LICENSE"
cp "$T/lock.orig" "$ROOT/mender-upstream.lock"
sed -i '/^added-file-sha256:/d' "$ROOT/meta-otapulse-bsp/qemu/UPSTREAM"
run ms --verify
expect_rcnz "verify fails when UPSTREAM lost its added-file-sha256 line"
git -C "$ROOT" checkout -q -- meta-otapulse-bsp/qemu/UPSTREAM
run ms --verify; expect_rc0 "clean again"
expect_eq "scratch root clean" "$(git -C "$ROOT" status --porcelain)" ""

# ---------------------------------------------------------------------------
section "(6) refusals: unknown / commercial components, bad refs"
run ms --sync meta-mender-commercial scarthgap-v2026.09
expect_rcnz "commercial component refused"
expect_out "says it is never vendored" "NEVER vendored"
run ms --sync Meta-Mender-Commercial main
expect_rcnz "commercial (any case) refused"
run ms --sync meta-mender-demo main
expect_rcnz "unknown component refused"
expect_out "says unknown component" "unknown component"
run ms --sync meta-mender-core abc1234
expect_rcnz "abbreviated sha refused"
expect_out "asks for the full sha" "40-character"
run ms --sync meta-mender-core 0000000000000000000000000000000000000000
expect_rcnz "non-existent full sha refused"
run ms --sync meta-mender-core no-such-ref
expect_rcnz "unknown ref refused"
run ms --sync meta-mender-core -x
expect_rcnz "option-looking ref refused"
# a commit that exists upstream but is on no branch/tag (unreachable) must be refused
orphan="$(git -C "$T/up/mm.git" -c user.name=t -c user.email=t@e.invalid commit-tree -m orphan "$(git -C "$T/up/mm.git" rev-parse 'scarthgap-v2026.09^{tree}')")"
run ms --sync meta-mender-core "$orphan"
expect_rcnz "sha not reachable from any ref refused"
expect_eq "snapshot untouched by all refusals" "$(snap "$ROOT")" "$before_snap"
mkdir -p "$ROOT/meta-otapulse-bsp/meta-mender-commercial"
run ms --verify
expect_rcnz "verify fails when a commercial dir is present"
expect_out "names the commercial path" "meta-otapulse-bsp/meta-mender-commercial"
rmdir "$ROOT/meta-otapulse-bsp/meta-mender-commercial"
mkdir -p "$ROOT/meta-otapulse-bsp/core/recipes-x/commercial-extra"
touch "$ROOT/meta-otapulse-bsp/core/recipes-x/commercial-extra/f"
run ms --verify
expect_rcnz "verify fails on a nested path containing 'commercial'"
rm -rf "$ROOT/meta-otapulse-bsp/core/recipes-x"
mkdir -p "$ROOT/meta-otapulse-bsp/stray"
run ms --verify
expect_rcnz "verify fails on a directory the lock does not cover"
rmdir "$ROOT/meta-otapulse-bsp/stray"
run ms --verify; expect_rc0 "clean again"

# ---------------------------------------------------------------------------
section "(4) a good patch applies; lock records pristine + patched hashes"
mk_core_patch "$ROOT/patches/mender/meta-mender-core/0001-good.patch" line9 line9-patched
run ms --verify
expect_rcnz "verify notices a patch added without a sync"
expect_out "tells to re-run --sync" "run --sync meta-mender-core"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rc0 "sync applies the patch"
expect_out "reports the patch count" "1 patch(es) applied"
grep -q '^line9-patched$' "$ROOT/meta-otapulse-bsp/core/recipes/foo.bb" && pass "patched content on disk" || bad "patched content missing"
expect_eq "lock col7 (pristine) unchanged = independent upstream hash" "$(lock_col "$ROOT/mender-upstream.lock" meta-mender-core 7)" "$EXPECT_PRISTINE_CORE"
PATCHED_HASH="$(lock_col "$ROOT/mender-upstream.lock" meta-mender-core 8)"
[[ "$PATCHED_HASH" =~ ^[0-9a-f]{64}$ && "$PATCHED_HASH" != "$EXPECT_PRISTINE_CORE" ]] && pass "lock col8 records a distinct patched hash" || bad "col8 is '$PATCHED_HASH'"
expect_eq "col8 equals the on-disk hash" "$(python3 "$TREEPY" "$ROOT/meta-otapulse-bsp/core")" "$PATCHED_HASH"
[[ "$(lock_col "$ROOT/mender-upstream.lock" meta-mender-core 9)" =~ ^[0-9a-f]{64}$ ]] && pass "lock col9 records the patch series hash" || bad "col9 not a hash"
grep -q '^patches: 0001-good.patch$' "$ROOT/meta-otapulse-bsp/core/UPSTREAM" && pass "UPSTREAM patches: line" || bad "UPSTREAM patches: line missing"
run ms --verify; expect_rc0 "verify passes with a patched tree"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_out "re-sync with patches is a no-op" "no change"
echo "tamper" >>"$ROOT/meta-otapulse-bsp/core/recipes/foo.bb"
run ms --verify
expect_rcnz "tampering the PATCHED file fails verify"
expect_out "names the patched file" "meta-otapulse-bsp/core/recipes/foo.bb"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rc0 "sync repairs a tampered vendored tree from upstream + patches"
run ms --verify; expect_rc0 "verify passes after the repair"
echo "# edited" >>"$ROOT/patches/mender/meta-mender-core/0001-good.patch"
run ms --verify
expect_rcnz "editing a patch without a sync fails verify"
expect_out "names the patch series" "patch series"
mk_core_patch "$ROOT/patches/mender/meta-mender-core/0001-good.patch" line9 line9-patched
run ms --sync meta-mender-core scarthgap-v2026.09
run ms --verify; expect_rc0 "clean again after regenerating the patch"

section "bad patch names / forbidden targets are refused"
printf 'x\n' >"$ROOT/patches/mender/meta-mender-core/bad name.patch"
snap_bad="$(snap "$ROOT")"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rcnz "badly named patch refused"
expect_eq "nothing changed" "$(snap "$ROOT")" "$snap_bad"
rm "$ROOT/patches/mender/meta-mender-core/bad name.patch"
cat >"$ROOT/patches/mender/meta-mender-core/0002-license.patch" <<'EOF'
--- a/LICENSE
+++ b/LICENSE
@@ -1 +1 @@
-x
+y
EOF
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rcnz "patch touching LICENSE refused"
rm "$ROOT/patches/mender/meta-mender-core/0002-license.patch"

# ---------------------------------------------------------------------------
section "planted added-file: line must not hide an extra file"
run ms --verify; expect_rc0 "baseline verifies"
(cd "$ROOT" && git add -A && "${GITC[@]}" commit -qm "pre-planted baseline")
C="$ROOT/meta-otapulse-bsp/core"
echo "evil" >"$C/evil.bb"
printf 'added-file: evil.bb\n' >>"$C/UPSTREAM"
run python3 "$TREEPY" "$C"
expect_eq "the hash helper itself refuses (duplicate key, exit 2)" "$RC" "2"
expect_out "and names the planted line" "evil.bb"
run ms --verify
expect_rcnz "verify fails on a planted added-file line"
expect_out "names the planted path" "evil.bb"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rc0 "sync repairs the planted file + line"
[ ! -e "$C/evil.bb" ] && ! grep -q 'evil' "$C/UPSTREAM" && pass "planted file and line are gone after the sync" || bad "planted content survived the sync"
expect_eq "scratch root back to the baseline commit" "$(git -C "$ROOT" status --porcelain)" ""
# a second added-file: LICENSE line, and a component that adds nothing
printf 'added-file: LICENSE\n' >>"$C/UPSTREAM"
run ms --verify
expect_rcnz "a duplicated 'added-file: LICENSE' line is refused"
run ms --sync meta-mender-core scarthgap-v2026.09; expect_rc0 "sync normalises the duplicate"
expect_eq "root clean again" "$(git -C "$ROOT" status --porcelain)" ""
G2="$ROOT/meta-otapulse-bsp/grub-mender-grubenv"
echo "evil" >"$G2/evil.cfg"
printf 'added-file: evil.cfg\n' >>"$G2/UPSTREAM"
run ms --verify
expect_rcnz "added-file on a component that adds nothing is refused"
expect_out "names the planted path (grub)" "evil.cfg"
run ms --sync grub-mender-grubenv master; expect_rc0 "sync repairs grub"
expect_eq "root clean again (grub)" "$(git -C "$ROOT" status --porcelain)" ""

section "UPSTREAM line-break variants cannot hide an added-file: line (one parser, fail closed)"
for variant in CR FF VT FS NEL LS PS; do
    case "$variant" in
        CR) sep=$'\r' ;;
        FF) sep=$'\f' ;;
        VT) sep=$'\v' ;;
        FS) sep=$'\x1c' ;;
        NEL) sep=$'\xc2\x85' ;;
        LS) sep=$'\xe2\x80\xa8' ;;
        PS) sep=$'\xe2\x80\xa9' ;;
    esac
    echo "evil" >"$C/evil.bb"
    printf 'note: hi%sadded-file: evil.bb\n' "$sep" >>"$C/UPSTREAM"
    run ms --verify
    expect_rcnz "verify fails on a $variant-separated added-file line"
    expect_out "$variant: output names UPSTREAM" "UPSTREAM"
    run python3 "$TREEPY" "$C"
    expect_eq "$variant: the hash helper itself refuses (exit 2)" "$RC" "2"
    run ms --sync meta-mender-core scarthgap-v2026.09
    expect_rc0 "$variant: sync repairs it"
    [ ! -e "$C/evil.bb" ] && ! grep -q 'added-file: evil' "$C/UPSTREAM" && pass "$variant: planted file and added-file line are gone" || bad "$variant: planted content survived"
    run ms --verify; expect_rc0 "$variant: verify passes after the repair"
    # the harmless "note: hi" prose from the planted line is legitimately kept; reset it
    git -C "$ROOT" show "HEAD:meta-otapulse-bsp/core/UPSTREAM" >"$C/UPSTREAM"
    expect_eq "$variant: root clean again" "$(git -C "$ROOT" status --porcelain)" ""
done
# the helper's --added-files output is the single source the verify check uses
run python3 "$TREEPY" --added-files "$C"
expect_eq "--added-files lists exactly LICENSE" "$OUT" "LICENSE"

section "paths that could forge manifest lines are refused (newline / tab / invalid UTF-8)"
# Reference implementation of the v1 tree hash WITHOUT the path check, to prove the attack
# really worked against the old algorithm (the precondition makes the test meaningful).
legacy_hash() {
    python3 - "$1" <<'PYEOF'
import hashlib, os, stat, sys
root = sys.argv[1]
skip = {"UPSTREAM", "LICENSE"}
entries = []
for d, dn, fn in os.walk(root):
    dn[:] = [x for x in dn if x != ".git"]
    for n in list(dn):
        if os.path.islink(os.path.join(d, n)):
            dn.remove(n)
            fn.append(n)
    for n in fn:
        p = os.path.join(d, n)
        rel = os.path.relpath(p, root)
        if rel in skip:
            continue
        if os.path.islink(p):
            sha = hashlib.sha256(os.readlink(p).encode()).hexdigest()
            line = "L 120000 %s %s\n" % (sha, rel)
        else:
            mode = "755" if os.stat(p).st_mode & stat.S_IXUSR else "644"
            line = "F %s %s %s\n" % (mode, hashlib.sha256(open(p, "rb").read()).hexdigest(), rel)
        entries.append((rel.encode(), line))
entries.sort()
print(hashlib.sha256("".join(l for _, l in entries).encode()).hexdigest())
PYEOF
}

# build the forged tree: delete X and Y (adjacent in sort order), and rename X's file to a
# path whose NEWLINE embeds Y's manifest line. Same hash under the old algorithm.
forge_newline_path() { # <core-dir>
    local c="$1" shay
    shay="$(sha256sum "$c/scripts/run.sh" | cut -d' ' -f1)"
    local d="$c/recipes/foo.bb"$'\n'"F 755 $shay scripts"
    mkdir -p "$d"
    mv "$c/recipes/foo.bb" "$d/run.sh"
    rm "$c/scripts/run.sh"
}

orig_hash="$(python3 "$TREEPY" "$C")"
forge_newline_path "$C"
expect_eq "(precondition) the OLD algorithm is fooled: forged tree has the same hash" "$(legacy_hash "$C")" "$orig_hash"
run ms --verify
expect_rcnz "verify fails on a newline-forged path"
expect_out "output names the offending path" "recipes/foo.bb"
expect_out "output says the path is unsafe" "unsafe path"
run python3 "$TREEPY" "$C";                                        expect_eq "hash helper exits 2 on it" "$RC" "2"
run python3 "$TREEPY" --list "$C";                                 expect_eq "--list refuses it (exit 2)" "$RC" "2"
run python3 "$TREEPY" --diff "$ROOT/mender-upstream.manifests/meta-mender-core.manifest" "$C"
expect_eq "--diff refuses it (exit 2)" "$RC" "2"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rc0 "sync repairs the forged tree"
expect_eq "root clean again after the repair" "$(git -C "$ROOT" status --porcelain)" ""

# --- the pre-commit hook blocks it (scratch repo with the real hook + tooling) -------------
mk_hk() {
    rm -rf "$T/hk"; mkdir -p "$T/hk/scripts"
    cp -a "$OTA_ROOT/scripts/hooks" "$OTA_ROOT/scripts/mender-sync.sh" "$OTA_ROOT/scripts/mender_tree_hash.py" \
        "$OTA_ROOT/scripts/mender_lock.py" "$T/hk/scripts/"
    cp -a "$ROOT/meta-otapulse-bsp" "$ROOT/mender-upstream.lock" "$ROOT/mender-upstream.manifests" "$ROOT/patches" "$T/hk/"
    (cd "$T/hk" && git init -q -b main && git add -A && "${GITC[@]}" commit -qm base --no-verify)
    # shellcheck disable=SC2016  # the $ expansions belong to the generated hook, not to this shell
    printf '#!/usr/bin/env bash\nhook="$(git rev-parse --show-toplevel)/scripts/hooks/mender-verify-hook.sh"\n[ -x "$hook" ] || exit 0\nexec "$hook" "$@"\n' >"$T/hk/.git/hooks/pre-commit"
    chmod +x "$T/hk/.git/hooks/pre-commit"
}
mk_hk
forge_newline_path "$T/hk/meta-otapulse-bsp/core"
(cd "$T/hk" && git add -A)
run bash -c 'cd "$1" && shift && "$@"' _ "$T/hk" "${GITC[@]}" commit -qm forged
expect_rcnz "pre-commit hook blocks the newline-forged path"
expect_out "hook output names the path" "recipes/foo.bb"
expect_out "hook says the commit is blocked" "Commit blocked"
mk_hk
echo "# harmless" >>"$T/hk/patches/mender/README.md"
(cd "$T/hk" && git add -A)
run bash -c 'cd "$1" && shift && "$@"' _ "$T/hk" "${GITC[@]}" commit -qm harmless
expect_rc0 "control: a harmless guarded-path commit still passes the hook"

# --- tab in a filename, invalid UTF-8 filename -----------------------------------------------
touch "$C/tab"$'\t'"file.bb"
run ms --verify
expect_rcnz "verify fails on a filename containing a TAB"
expect_out "names the TAB path" "tab"
rm "$C/tab"$'\t'"file.bb"
touch "$C/bad$(printf '\377').bb"
run ms --verify
expect_rcnz "verify fails on a non-UTF-8 filename"
expect_out "names the non-UTF-8 path" "bad"
expect_out "says not valid UTF-8" "not valid UTF-8"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rc0 "sync repairs the non-UTF-8 stray file"
expect_eq "root clean again" "$(git -C "$ROOT" status --porcelain)" ""
touch "$C/ctl"$'\x7f'"x.bb"
run ms --verify; expect_rcnz "verify fails on a DEL character in a filename"
rm "$C/ctl"$'\x7f'"x.bb"
touch "$C/nel"$'\xc2\x85'"x.bb"
run ms --verify; expect_rcnz "verify fails on a NEL in a filename"
rm "$C/nel"$'\xc2\x85'"x.bb"
run ms --verify; expect_rc0 "clean again"

# --- a hostile UPSTREAM tree (path with a newline) is refused by --sync, nothing changes ------
git init -q -b master "$T/w/hostile"
(
    cd "$T/w/hostile" || exit 1
    printf 'GRUB LICENSE\n' >LICENSE
    blob="$(printf 'x\n' | git hash-object -w --stdin)"
    git add LICENSE
    git update-index --add --cacheinfo "100644,$blob,evil"$'\n'"F 644 $blob forged"
    "${GITC[@]}" commit -qm hostile
)
git clone -q --bare "$T/w/hostile" "$T/up/hostile.git"
hostile_snap="$(snap "$ROOT")"
run env MENDER_SYNC_URL_GRUB_MENDER_GRUBENV="file://$T/up/hostile.git" MENDER_SYNC_ROOT="$ROOT" "$SYNC" --sync grub-mender-grubenv master
expect_rcnz "sync refuses a hostile upstream tree"
expect_out "names the hostile path" "unsafe path"
expect_eq "nothing changed" "$(snap "$ROOT")" "$hostile_snap"

section "UPSTREAM hardening: NUL bytes, duplicate keys; LICENSE must be a regular file"
printf 'note: a\0b\n' >>"$C/UPSTREAM"
run ms --verify
expect_rcnz "verify fails on a NUL byte in UPSTREAM"
expect_out "says NUL" "NUL"
run ms --sync meta-mender-core scarthgap-v2026.09; expect_rc0 "sync repairs it"
run ms --verify; expect_rc0 "verify passes after the repair"
git -C "$ROOT" show "HEAD:meta-otapulse-bsp/core/UPSTREAM" >"$C/UPSTREAM"
printf 'commit: 0000000000000000000000000000000000000000\n' >>"$C/UPSTREAM"
run ms --verify
expect_rcnz "verify fails on a duplicate key in UPSTREAM"
expect_out "says duplicate key" "duplicate key 'commit'"
run ms --sync meta-mender-core scarthgap-v2026.09; expect_rc0 "sync repairs the duplicate key"
expect_eq "root clean again" "$(git -C "$ROOT" status --porcelain)" ""
cp "$C/LICENSE" "$T/lic.target"
rm "$C/LICENSE"; ln -s "$T/lic.target" "$C/LICENSE"
run ms --verify
expect_rcnz "verify fails when LICENSE is a symlink with the right content"
expect_out "says not a regular file" "not a regular file"
run ms --sync meta-mender-core scarthgap-v2026.09; expect_rc0 "sync replaces the symlink"
expect_eq "root clean again (LICENSE)" "$(git -C "$ROOT" status --porcelain)" ""
G3="$ROOT/meta-otapulse-bsp/grub-mender-grubenv"
cp "$G3/LICENSE" "$T/glic"; rm "$G3/LICENSE"; ln -s "$T/glic" "$G3/LICENSE"
run ms --verify; expect_rcnz "verify fails on a symlinked own-LICENSE too"
run ms --sync grub-mender-grubenv master; expect_rc0 "sync repairs it"
expect_eq "root clean again (grub)" "$(git -C "$ROOT" status --porcelain)" ""

section "patch guard: header spelling cannot bypass the UPSTREAM/LICENSE refusal"
printf -- '--- a/LICENSE\t2026-01-01 00:00:00\n+++ b/LICENSE\t2026-01-01 00:00:00\n@@ -1 +1 @@\n-x\n+y\n' >"$ROOT/patches/mender/meta-mender-core/0002-tab.patch"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rcnz "tab/timestamp after the LICENSE path is still refused"
printf 'diff --git a/UPSTREAM b/UPSTREAM\nnew file mode 100644\n--- /dev/null\n+++ b/UPSTREAM\t\n@@ -0,0 +1 @@\n+x\n' >"$ROOT/patches/mender/meta-mender-core/0002-tab.patch"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rcnz "diff --git header naming UPSTREAM is refused"
rm "$ROOT/patches/mender/meta-mender-core/0002-tab.patch"

section "symlinked *.patch entries are refused consistently"
ln -s 0001-good.patch "$ROOT/patches/mender/meta-mender-core/0003-link.patch"
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rcnz "sync refuses a symlinked patch"
expect_out "says symlinks are refused" "symlink"
run ms --verify
expect_rcnz "verify refuses a symlinked patch"
rm "$ROOT/patches/mender/meta-mender-core/0003-link.patch"
run ms --verify; expect_rc0 "clean again"

section "interrupt between the two renames restores the previous tree, lock and manifest"
fault_snap="$(snap "$ROOT")"
run env MENDER_SYNC_FAULT=after-old-moved MENDER_SYNC_ROOT="$ROOT" "$SYNC" --sync meta-mender-core scarthgap-v2026.10
expect_rcnz "sync killed (SIGTERM) after the old tree was moved aside"
expect_out "reports the rollback" "previous tree, lock and manifest restored"
expect_eq "vendored dir, lock and manifest are exactly as before" "$(snap "$ROOT")" "$fault_snap"
expect_eq "no sync leftovers" "$(find "$ROOT" -name '*.sync-*' | wc -l)" "0"
run ms --verify; expect_rc0 "verify passes after the interrupted sync"

# ---------------------------------------------------------------------------
section "(3) conflicting patch: hard fail, tree + lock untouched"
# upstream v2026.10 changes line2 AND appends line15. This patch changes line2 -> conflict.
mk_core_patch "$ROOT/patches/mender/meta-mender-core/0002-conflict.patch" line2 line2-ours
run ms --sync meta-mender-core scarthgap-v2026.09
expect_rc0 "conflict patch applies fine at the pinned tag"
run ms --verify; expect_rc0 "verified with both patches"
(cd "$ROOT" && git add -A && "${GITC[@]}" commit -qm "two patches")
conflict_snap="$(snap "$ROOT")"; conflict_lock="$(sha256sum "$ROOT/mender-upstream.lock")"
run ms --sync meta-mender-core scarthgap-v2026.10
expect_rcnz "sync to v2026.10 fails on the conflicting patch"
expect_out "message says regenerate the patch, never force" "regenerate the patch, never force"
expect_out "message names the patch" "0002-conflict.patch"
expect_eq "vendored trees + lock + manifests byte-identical" "$(snap "$ROOT")" "$conflict_snap"
expect_eq "lock byte-identical" "$(sha256sum "$ROOT/mender-upstream.lock")" "$conflict_lock"
expect_eq "git diff empty" "$(git -C "$ROOT" status --porcelain)" ""
expect_eq "no temp leftovers in the root" "$(find "$ROOT" -name '*.sync-*' | wc -l)" "0"
run ms --verify; expect_rc0 "verify still passes after the refused sync"

section "after removing the conflicting patch the bump succeeds (and refreshes LICENSE)"
rm "$ROOT/patches/mender/meta-mender-core/0002-conflict.patch"
run ms --sync meta-mender-core scarthgap-v2026.10
expect_rc0 "sync to v2026.10 with the remaining patch"
expect_eq "LICENSE re-added from the new repo-root LICENSE" "$(cat "$ROOT/meta-otapulse-bsp/core/LICENSE")" "LICENSE v2 (fake upstream)"
grep -q '^line2-upstream-changed$' "$ROOT/meta-otapulse-bsp/core/recipes/foo.bb" && grep -q '^line9-patched$' "$ROOT/meta-otapulse-bsp/core/recipes/foo.bb" && pass "new upstream content + patch" || bad "bumped content wrong"
expect_eq "lock ref updated" "$(lock_col "$ROOT/mender-upstream.lock" meta-mender-core 3)" "tag:scarthgap-v2026.10"
expect_eq "lock commit updated" "$(lock_col "$ROOT/mender-upstream.lock" meta-mender-core 4)" "$(git -C "$T/w/mm" rev-parse 'scarthgap-v2026.10^{commit}')"
grep -q '^ref: tag:scarthgap-v2026.10$' "$ROOT/meta-otapulse-bsp/core/UPSTREAM" && pass "UPSTREAM ref updated" || bad "UPSTREAM ref not updated"
grep -q "^commit: $(git -C "$T/w/mm" rev-parse 'scarthgap-v2026.10^{commit}')\$" "$ROOT/meta-otapulse-bsp/core/UPSTREAM" && pass "UPSTREAM commit updated" || bad "UPSTREAM commit not updated"
grep -q "the tag object is $(git -C "$T/w/mm" rev-parse scarthgap-v2026.10)" "$ROOT/meta-otapulse-bsp/core/UPSTREAM" && pass "UPSTREAM note carries the new tag object" || bad "tag object note stale"
run ms --verify; expect_rc0 "verify passes after the bump"
run ms --status
expect_rc0 "--status (fake upstreams, no network)"
expect_out "status lists core" "meta-mender-core"
expect_out "qemu is behind the new tag" "UPDATE AVAILABLE"
expect_out "core is up-to-date" "up-to-date"
expect_out "branch-pinned nxp up to date" "branch:scarthgap"

section "sha sync keeps the branch label when the sha is on that branch"
run ms --sync meta-mender-raspberrypi "$(cat "$T/mc.sha2")"
expect_rc0 "advance raspberrypi to the second commit by full sha"
expect_eq "label still branch:scarthgap" "$(lock_col "$ROOT/mender-upstream.lock" meta-mender-raspberrypi 3)" "branch:scarthgap"
grep -q 'rpi layer v2' "$ROOT/meta-otapulse-bsp/boards/raspberrypi/conf/layer.conf" && pass "content advanced" || bad "content not advanced"
run ms --verify; expect_rc0 "verify passes"

# ---------------------------------------------------------------------------
section "legacy 7-column lock rows still verify; --upgrade-lock restores the full format"
cp "$ROOT/mender-upstream.lock" "$T/lock.full"
# a legacy row for the PATCHED core tree cannot verify (it only knows the pristine hash)
awk -F'\t' -v OFS='\t' '/^#/ {print; next} $1 == "meta-mender-core" {print $1,$2,$3,$4,$5,$6,$7; next} {print}' "$T/lock.full" >"$ROOT/mender-upstream.lock"
run ms --verify
expect_rcnz "legacy row for a patched tree fails verify"
# a legacy row for an unpatched tree keeps verifying (with a warning)
awk -F'\t' -v OFS='\t' '/^#/ {print; next} $1 == "meta-mender-qemu" {print $1,$2,$3,$4,$5,$6,$7; next} {print}' "$T/lock.full" >"$ROOT/mender-upstream.lock"
rm -f "$ROOT/mender-upstream.manifests/meta-mender-qemu.manifest"
run ms --verify
expect_rc0 "legacy 7-column row (unpatched tree) keeps verifying"
expect_out "warns that the LICENSE is not checked" "legacy lock row"
run ms --upgrade-lock
expect_rc0 "--upgrade-lock fills the missing columns + manifest"
expect_eq "lock is byte-identical to the full-format lock again" "$(sha256sum <"$ROOT/mender-upstream.lock")" "$(sha256sum <"$T/lock.full")"
run ms --verify; expect_rc0 "verify clean after upgrade"
cp "$ROOT/meta-otapulse-bsp/qemu/conf/layer.conf" "$T/q.conf"
echo tamper >>"$ROOT/meta-otapulse-bsp/qemu/conf/layer.conf"
run ms --upgrade-lock
expect_rcnz "--upgrade-lock refuses to bless a tampered tree"
cp "$T/q.conf" "$ROOT/meta-otapulse-bsp/qemu/conf/layer.conf"
run ms --verify; expect_rc0 "verify clean after undoing the tamper"

# ---------------------------------------------------------------------------
section "(5) --status over https without credentials (needs network)"
if [ "${MENDER_SYNC_TEST_ONLINE:-0}" = 1 ]; then
    REAL="$T/real"
    mkdir -p "$REAL"
    cp -a "$OTA_ROOT/mender-upstream.lock" "$OTA_ROOT/mender-upstream.manifests" "$OTA_ROOT/meta-otapulse-bsp" "$OTA_ROOT/patches" "$REAL/"
    # no MENDER_SYNC_URL_* overrides, no GH/GIT credentials in the environment
    run env -i PATH="$PATH" HOME="$T/emptyhome" GIT_TERMINAL_PROMPT=0 MENDER_SYNC_ROOT="$REAL" "$SYNC" --status
    expect_rc0 "--status with env -i (no credential env) over https"
    printf '%s\n' "$OUT" | sed 's/^/       | /'
    for c in "${COMPONENTS[@]}"; do expect_out "status row for $c" "$c"; done
    expect_out "core pinned tag shown" "scarthgap-v2026.09"
    expect_no_out "no password prompt" "Username for"

    section "(1, real network) --sync to the already-pinned ref against real upstreams"
    (cd "$REAL" && git init -q && git add -A && "${GITC[@]}" commit -qm real-baseline)
    run env -i PATH="$PATH" HOME="$T/emptyhome" GIT_TERMINAL_PROMPT=0 MENDER_SYNC_ROOT="$REAL" "$SYNC" --sync meta-mender-core scarthgap-v2026.09
    expect_rc0 "real --sync meta-mender-core scarthgap-v2026.09"
    expect_out "no change" "no change"
    expect_eq "real: git diff empty" "$(git -C "$REAL" status --porcelain)" ""
    run env -i PATH="$PATH" HOME="$T/emptyhome" GIT_TERMINAL_PROMPT=0 MENDER_SYNC_ROOT="$REAL" "$SYNC" --sync meta-mender-raspberrypi 57f10880a166ccca9b49fa451983f44e72a39561
    expect_rc0 "real --sync meta-mender-raspberrypi <pinned sha>"
    expect_eq "real: git diff still empty" "$(git -C "$REAL" status --porcelain)" ""
    run env -i PATH="$PATH" HOME="$T/emptyhome" GIT_TERMINAL_PROMPT=0 MENDER_SYNC_ROOT="$REAL" "$SYNC" --verify --online
    expect_rc0 "real --verify --online (fresh clones + LICENSE cmp)"
else
    printf '  skip (set MENDER_SYNC_TEST_ONLINE=1 to run the networked --status / real no-op sync tests)\n'
fi

# ---------------------------------------------------------------------------
section "(8) the real repo tree verifies (offline)"
run env -u MENDER_SYNC_ROOT -u MENDER_SYNC_URL_META_MENDER_CORE -u MENDER_SYNC_URL_META_MENDER_QEMU \
    -u MENDER_SYNC_URL_META_MENDER_RASPBERRYPI -u MENDER_SYNC_URL_META_MENDER_NXP \
    -u MENDER_SYNC_URL_GRUB_MENDER_GRUBENV "$SYNC" --verify
expect_rc0 "scripts/mender-sync.sh --verify on the actual repo state"
expect_out "5 trees" "5 vendored trees match"

printf '\n%d passed, %d failed\n' "$PASS" "$FAILED"
[ "$FAILED" -eq 0 ]
