#!/bin/sh
# Host-side regression test for setup-uboot-env.sh (BUG-352).
# NOT installed into any image (the recipe's SRC_URI lists files/ entries
# explicitly; this tests/ directory is never fetched).
#
# Usage: sh test-setup-uboot-env.sh [path/to/setup-uboot-env.sh]
#
# Uses the script's OTAPULSE_DT_BASE / OTAPULSE_FW_ENV_CONFIG seams so it
# never reads the host's /proc/device-tree nor writes /etc/fw_env.config,
# and puts a recording fake fw_printenv first on PATH so no real U-Boot env
# tool is ever invoked.

set -u

HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="${1:-$HERE/../files/setup-uboot-env.sh}"
FAILS=0
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

mkdir -p "$TMP/bin"
cat > "$TMP/bin/fw_printenv" <<'EOF'
#!/bin/sh
echo called >> "$FAKE_FW_LOG"
exit 0
EOF
chmod +x "$TMP/bin/fw_printenv"

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; FAILS=$((FAILS + 1)); }

# The exact device-line predicate switch-boot-slot.sh's try_fw_setenv safety
# gate uses to decide whether a real U-Boot env exists.
gate_device() {
    awk '!/^[[:space:]]*#/ && NF>=3 {print $1; exit}' "$1" 2>/dev/null
}

# run_case NAME COMPATIBLE-ENTRIES... ; sets RC, OUT, CFG, FWLOG
run_case() {
    _name="$1"; shift
    _dt="$TMP/$_name/dt"
    mkdir -p "$_dt"
    : > "$_dt/compatible"
    for _c in "$@"; do printf '%s\0' "$_c" >> "$_dt/compatible"; done
    printf 'Test %s\0' "$_name" > "$_dt/model"
    CFG="$TMP/$_name/fw_env.config"
    FWLOG="$TMP/$_name/fw_printenv.log"
    OUT=$(PATH="$TMP/bin:$PATH" FAKE_FW_LOG="$FWLOG" \
          OTAPULSE_DT_BASE="$_dt" OTAPULSE_FW_ENV_CONFIG="$CFG" \
          sh "$SCRIPT" 2>&1)
    RC=$?
}

# ── Case 1: Raspberry Pi 4 (VideoCore, no U-Boot env) — BUG-352 ────────────
run_case rpi4 "raspberrypi,4-model-b" "brcm,bcm2711"
echo "$OUT" | grep -q "Detected SoC type: broadcom" \
    && pass "rpi4: soc detected as broadcom" || fail "rpi4: soc not detected as broadcom"
[ "$RC" -eq 0 ] && pass "rpi4: exit 0" || fail "rpi4: exit $RC (expected 0)"
if [ -f "$CFG" ]; then
    dev=$(gate_device "$CFG")
    [ -z "$dev" ] && pass "rpi4: config has no device line (try_fw_setenv gate refuses)" \
        || fail "rpi4: config carries a fabricated device line '$dev' (try_fw_setenv gate would pass)"
else
    fail "rpi4: no fw_env.config written (every later install would re-run setup)"
fi
echo "$OUT" | grep -q "0x3F8000" \
    && fail "rpi4: Rockchip default offset 0x3F8000 was selected" \
    || pass "rpi4: no Rockchip default offset selected"
[ -s "$FWLOG" ] && fail "rpi4: fw_printenv was invoked on a board with no U-Boot env" \
    || pass "rpi4: fw_printenv not invoked"

# ── Case 2: Allwinner control — unchanged behaviour ───────────────────────
run_case allwinner "xunlong,orangepi-zero2w" "allwinner,sun50i-h618"
echo "$OUT" | grep -q "Detected SoC type: allwinner" && echo "$OUT" | grep -q "Environment offset: 0x88000, size: 0x8000" \
    && pass "allwinner: offset selection unchanged (0x88000/0x8000)" \
    || fail "allwinner: offset selection changed"
if [ "$RC" -eq 0 ]; then
    [ "$(gate_device "$CFG")" != "" ] && grep -q "0x88000" "$CFG" \
        && pass "allwinner: generated config carries the Allwinner offset" \
        || fail "allwinner: generated config wrong"
else
    echo "$OUT" | grep -q "ERROR: Could not detect boot device" \
        && pass "allwinner: no boot device on this host (expected off-target) — generation path not exercised" \
        || fail "allwinner: unexpected exit $RC"
fi

# ── Case 3: no device tree (unknown SoC) — unchanged default ──────────────
_dt="$TMP/none/dt"; mkdir -p "$_dt"
OUT=$(PATH="$TMP/bin:$PATH" FAKE_FW_LOG="$TMP/none.log" OTAPULSE_DT_BASE="$_dt" \
      OTAPULSE_FW_ENV_CONFIG="$TMP/none.cfg" sh "$SCRIPT" 2>&1)
echo "$OUT" | grep -q "Detected SoC type: unknown" && echo "$OUT" | grep -q "Environment offset: 0x3F8000, size: 0x8000" \
    && pass "unknown: default offset selection unchanged" \
    || fail "unknown: default offset selection changed"

echo
if [ "$FAILS" -eq 0 ]; then
    echo "ALL PASSED"
    exit 0
fi
echo "$FAILS FAILED"
exit 1
