#!/bin/bash
# otapulse-verify.sh — Post-build artifact verification for OTAPulse
#
# Inspects the actual built artifacts to catch issues that the bbclass sanity
# checks cannot: corrupted .mender files, placeholder URLs embedded in the
# rootfs, missing OTA agent, unsigned artifacts shipped to production, etc.
#
# Usage:
#   otapulse-verify.sh [BUILD_DIR] [IMAGE_NAME] [MACHINE]
#
#   BUILD_DIR   Path to the Yocto build directory (default: build)
#   IMAGE_NAME  Image recipe name, e.g. core-image-minimal (optional filter)
#   MACHINE     Target machine (default: auto-detected from local.conf)
#
# Examples:
#   otapulse-verify.sh
#   otapulse-verify.sh build core-image-minimal imx8mp-lpddr4-evk
#   otapulse-verify.sh /path/to/build
#
# Exit codes:
#   0  All checks passed (warnings are acceptable)
#   1  One or more checks failed

set -euo pipefail

# ---------------------------------------------------------------------------
# Terminal colours — suppressed automatically for non-TTY or NO_COLOR env var
# ---------------------------------------------------------------------------
if [[ -t 1 ]] && [[ "${NO_COLOR:-}" == "" ]]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    BOLD='\033[1m'
    RESET='\033[0m'
else
    RED='' GREEN='' YELLOW='' BLUE='' BOLD='' RESET=''
fi

# ---------------------------------------------------------------------------
# Result counters
# ---------------------------------------------------------------------------
PASSED=0
WARNINGS=0
FAILURES=0

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
pass() { printf "${GREEN}  [PASS]${RESET} %s\n" "$*"; PASSED=$((PASSED + 1)); }
fail() { printf "${RED}  [FAIL]${RESET} %s\n" "$*"; FAILURES=$((FAILURES + 1)); }
warn() { printf "${YELLOW}  [WARN]${RESET} %s\n" "$*"; WARNINGS=$((WARNINGS + 1)); }
info() { printf "${BLUE}  [INFO]${RESET} %s\n" "$*"; }

section() {
    local num="$1"; shift
    printf "\n${BOLD}${BLUE}── [%s] %s${RESET}\n" "$num" "$*"
    printf '─%.0s' {1..60}
    printf '\n'
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
BUILD_DIR="${1:-build}"
IMAGE_NAME="${2:-}"
MACHINE_ARG="${3:-}"

# ---------------------------------------------------------------------------
# Helper functions
# ---------------------------------------------------------------------------

# Auto-detect MACHINE from the build's local.conf
detect_machine() {
    local conf="${BUILD_DIR}/conf/local.conf"
    [[ -f "$conf" ]] || return 0
    # Match MACHINE = "..." or MACHINE ?= "..." (last assignment wins)
    grep -E '^\s*MACHINE\s*[?]?=' "$conf" \
        | tail -1 \
        | sed -E 's|[^=]+=\s*"?([^"# ]+)"?.*|\1|' \
        | tr -d ' "'
}

# Locate the mender-artifact binary.
# Checks the Buildroot host-tools convention first, then common system paths.
find_mender_artifact_bin() {
    local candidates=(
        "${BUILD_DIR}/host/bin/mender-artifact"
        "${BUILD_DIR}/../host/bin/mender-artifact"
        "/usr/local/bin/mender-artifact"
        "/usr/bin/mender-artifact"
        "${HOME}/.local/bin/mender-artifact"
        "${HOME}/bin/mender-artifact"
    )
    for c in "${candidates[@]}"; do
        [[ -x "$c" ]] && { echo "$c"; return 0; }
    done
    # Fall back to PATH
    command -v mender-artifact 2>/dev/null || true
}

# Check whether a path exists inside an ext4 image using debugfs.
# Returns 0 if found, 1 if not.
debugfs_exists() {
    local image="$1" path="$2"
    local out
    # debugfs always exits 0; check output for "Type:" which appears on success
    out=$(debugfs -R "stat ${path}" "$image" 2>&1) || true
    echo "$out" | grep -q "Type:" && return 0 || return 1
}

# Read a file from an ext4 image using debugfs (stdout, errors suppressed).
debugfs_cat() {
    local image="$1" path="$2"
    debugfs -R "cat ${path}" "$image" 2>/dev/null || true
}

# Human-readable file size (uses du -sh, falls back to ls -lh).
human_size() {
    local f="$1"
    du -sh "$f" 2>/dev/null | cut -f1 || ls -lh "$f" 2>/dev/null | awk '{print $5}' || echo "?"
}

# Return 0 if the URL looks like a placeholder value.
PLACEHOLDER_PATTERNS=(
    "CHANGE_ME"
    "example.com"
    "your-ota-server"
    "your-server"
    "192.168.0.122"
    "docker.mender.io"
    "localhost"
)

is_placeholder_url() {
    local url="$1"
    for pat in "${PLACEHOLDER_PATTERNS[@]}"; do
        [[ "$url" == *"$pat"* ]] && return 0
    done
    return 1
}

# Find artifacts in DEPLOY_DIR, optionally filtered by IMAGE_NAME prefix.
find_artifacts() {
    local suffix="$1"
    local pattern
    if [[ -n "$IMAGE_NAME" ]]; then
        pattern="${IMAGE_NAME}*${suffix}"
    else
        pattern="*${suffix}"
    fi
    # -not -type l avoids double-counting symlinks; sort for determinism
    find "$DEPLOY_DIR" -maxdepth 1 -name "$pattern" -not -type l 2>/dev/null | sort || true
}

# ---------------------------------------------------------------------------
# Resolve build parameters
# ---------------------------------------------------------------------------
MACHINE="$MACHINE_ARG"
if [[ -z "$MACHINE" ]]; then
    MACHINE=$(detect_machine)
fi

if [[ -z "$MACHINE" ]]; then
    printf "${RED}ERROR:${RESET} Cannot determine MACHINE.\n"
    printf "  Pass it as the third argument, or ensure MACHINE is set in:\n"
    printf "  %s/conf/local.conf\n" "$BUILD_DIR"
    exit 1
fi

DEPLOY_DIR="${BUILD_DIR}/tmp/deploy/images/${MACHINE}"

# ---------------------------------------------------------------------------
# Header
# ---------------------------------------------------------------------------
printf "\n${BOLD}OTAPulse Build Verification${RESET}\n"
printf '═%.0s' {1..60}; printf '\n'
info "Build dir:  ${BUILD_DIR}"
info "Deploy dir: ${DEPLOY_DIR}"
info "Machine:    ${MACHINE}"
[[ -n "$IMAGE_NAME" ]] && info "Image name: ${IMAGE_NAME}"

if [[ ! -d "$DEPLOY_DIR" ]]; then
    printf "\n${RED}ERROR:${RESET} Deploy directory not found:\n"
    printf "  %s\n" "$DEPLOY_DIR"
    printf "  Has the build completed?  Run: bitbake %s\n\n" "${IMAGE_NAME:-<image-name>}"
    exit 1
fi

# Collect ext4 files once — used by checks 3, 4
mapfile -t EXT4_FILES < <(find_artifacts ".ext4")

# Primary ext4 image for rootfs inspection (first found)
EXT4_IMAGE=""
if [[ "${#EXT4_FILES[@]}" -gt 0 ]]; then
    EXT4_IMAGE="${EXT4_FILES[0]}"
fi

# ═══════════════════════════════════════════════════════════════════════════
# [1/6] Mender Artifact
# ═══════════════════════════════════════════════════════════════════════════
section "1/6" "Mender Artifact"

mapfile -t MENDER_FILES < <(find_artifacts ".mender")

if [[ "${#MENDER_FILES[@]}" -eq 0 ]]; then
    fail "No .mender artifact found in ${DEPLOY_DIR}"
    info "  Ensure 'mender-artifact' class is inherited and ext4 is in IMAGE_FSTYPES"
else
    MENDER_BIN=$(find_mender_artifact_bin)
    [[ -n "$MENDER_BIN" ]] && info "mender-artifact tool: ${MENDER_BIN}" \
                             || info "mender-artifact tool: not found (validation skipped)"

    for mender_file in "${MENDER_FILES[@]}"; do
        [[ -z "$mender_file" ]] && continue
        size=$(human_size "$mender_file")
        info "Artifact: $(basename "$mender_file") (${size})"

        if [[ -z "$MENDER_BIN" ]]; then
            # No tool available — at least confirm the file is non-empty
            if [[ -s "$mender_file" ]]; then
                pass "Artifact file is non-empty"
            else
                fail "Artifact file is empty: $(basename "$mender_file")"
            fi
        else
            # Validate integrity
            if "$MENDER_BIN" validate "$mender_file" &>/dev/null; then
                pass "mender-artifact validate: OK"
            else
                fail "mender-artifact validate FAILED for $(basename "$mender_file")"
            fi

            # Extract metadata
            read_out=$("$MENDER_BIN" read "$mender_file" 2>/dev/null || true)
            if [[ -n "$read_out" ]]; then
                # Device type
                dev_type=$(echo "$read_out" \
                    | grep -i "device.type" \
                    | head -1 \
                    | sed -E 's/.*:\s*//' \
                    | tr -d ' ')
                if [[ -n "$dev_type" ]]; then
                    pass "Device type in artifact: ${dev_type}"
                else
                    warn "Could not extract device type from artifact metadata"
                fi

                # Signing
                if echo "$read_out" | grep -qiE "signature|signed"; then
                    pass "Artifact contains signature information"
                else
                    warn "No signature information found in artifact metadata"
                fi
            fi
        fi
    done
fi

# ═══════════════════════════════════════════════════════════════════════════
# [2/6] Flashable Image
# ═══════════════════════════════════════════════════════════════════════════
section "2/6" "Flashable Image"

wic_found=false
for ext in ".wic" ".wic.gz" ".wic.bz2" ".img"; do
    mapfile -t files < <(find_artifacts "$ext")
    for f in "${files[@]}"; do
        [[ -z "$f" ]] && continue
        info "$(basename "$f") ($(human_size "$f"))"
        pass "Flashable image found: $(basename "$f")"
        wic_found=true
    done
done

if [[ "${#EXT4_FILES[@]}" -gt 0 ]]; then
    for f in "${EXT4_FILES[@]}"; do
        [[ -z "$f" ]] && continue
        info "$(basename "$f") ($(human_size "$f"))"
        pass "ext4 rootfs found: $(basename "$f")"
    done
else
    warn "No ext4 rootfs found (required for mender-artifact generation)"
fi

$wic_found || warn "No WIC/IMG flashable image found — check IMAGE_FSTYPES"

# ═══════════════════════════════════════════════════════════════════════════
# [3/6] Rootfs Content Inspection
# Uses debugfs to inspect the ext4 image without mounting (no root required).
# ═══════════════════════════════════════════════════════════════════════════
section "3/6" "Rootfs Content Inspection"

if [[ -z "$EXT4_IMAGE" ]]; then
    warn "No ext4 image available — skipping rootfs inspection"
elif ! command -v debugfs &>/dev/null; then
    warn "debugfs not found — skipping rootfs inspection"
    info "  Install e2fsprogs to enable this check: sudo apt install e2fsprogs"
else
    info "Inspecting: $(basename "$EXT4_IMAGE")"

    # ── OTA agent binary ─────────────────────────────────────────────────
    agent_found=false
    for path in "/usr/bin/soc-ota-agent" "/usr/bin/mender"; do
        if debugfs_exists "$EXT4_IMAGE" "$path"; then
            pass "OTA agent found: ${path}"
            agent_found=true
            break
        fi
    done
    $agent_found || fail "OTA agent not found (/usr/bin/soc-ota-agent or /usr/bin/mender)"

    # ── OTA configuration file ───────────────────────────────────────────
    conf_found=false
    for path in "/etc/mender/mender.conf" "/etc/otapulse/otapulse.conf"; do
        if debugfs_exists "$EXT4_IMAGE" "$path"; then
            pass "OTA config found: ${path}"
            conf_found=true
            break
        fi
    done
    $conf_found || fail "OTA config not found (/etc/mender/mender.conf or /etc/otapulse/otapulse.conf)"

    # ── device_type file ─────────────────────────────────────────────────
    dtype_found=false
    for path in "/etc/mender/device_type" "/var/lib/mender/device_type"; do
        if debugfs_exists "$EXT4_IMAGE" "$path"; then
            dtype_content=$(debugfs_cat "$EXT4_IMAGE" "$path" | tr -d '\0')
            pass "device_type found: ${path} (${dtype_content})"
            dtype_found=true
            break
        fi
    done
    $dtype_found || fail "device_type file not found in rootfs"

    # ── memfaultd ────────────────────────────────────────────────────────
    if debugfs_exists "$EXT4_IMAGE" "/usr/bin/memfaultd"; then
        pass "memfaultd found (/usr/bin/memfaultd)"
    else
        warn "memfaultd not found — device monitoring and crash reporting unavailable"
        info "  Add to image: IMAGE_INSTALL:append = \" memfaultd-bin\""
    fi

    # ── systemd service file ─────────────────────────────────────────────
    svc_found=false
    for path in \
        "/lib/systemd/system/soc-ota-agent.service" \
        "/usr/lib/systemd/system/soc-ota-agent.service" \
        "/lib/systemd/system/mender-client.service" \
        "/usr/lib/systemd/system/mender-client.service" \
        "/lib/systemd/system/mender.service"
    do
        if debugfs_exists "$EXT4_IMAGE" "$path"; then
            pass "systemd service found: ${path}"
            svc_found=true
            break
        fi
    done
    $svc_found || fail "No OTA agent systemd service found — agent may not start on boot"
fi

# ═══════════════════════════════════════════════════════════════════════════
# [4/6] Configuration Content
# Reads the embedded OTA config from the ext4 image and checks the server URL.
# ═══════════════════════════════════════════════════════════════════════════
section "4/6" "Configuration Content"

if [[ -z "$EXT4_IMAGE" ]]; then
    warn "No ext4 image available — skipping configuration content check"
elif ! command -v debugfs &>/dev/null; then
    warn "debugfs not found — skipping configuration content check"
else
    conf_content=""
    conf_path_used=""

    for path in "/etc/mender/mender.conf" "/etc/otapulse/otapulse.conf"; do
        if debugfs_exists "$EXT4_IMAGE" "$path"; then
            conf_content=$(debugfs_cat "$EXT4_IMAGE" "$path")
            conf_path_used="$path"
            break
        fi
    done

    if [[ -z "$conf_path_used" ]]; then
        warn "OTA config file not found in rootfs — cannot verify embedded server URL"
    else
        info "Checking embedded config: ${conf_path_used}"

        # Extract ServerURL — handles both JSON (mender.conf) and key=value formats
        server_url=""
        if echo "$conf_content" | grep -qiE '"?ServerURL"?\s*:'; then
            server_url=$(echo "$conf_content" \
                | grep -iE '"?ServerURL"?\s*:' \
                | head -1 \
                | sed -E 's|.*:\s*"?([^",:} ]+)"?.*|\1|' \
                | tr -d ' "')
        elif echo "$conf_content" | grep -qiE '^\s*server_url\s*='; then
            server_url=$(echo "$conf_content" \
                | grep -iE '^\s*server_url\s*=' \
                | head -1 \
                | sed -E 's|[^=]+=\s*"?([^"# ]+)"?.*|\1|' \
                | tr -d ' "')
        fi

        if [[ -z "$server_url" ]]; then
            fail "ServerURL not found in embedded configuration"
            info "  Check that OTA_SERVER_URL is set and the agent config is generated correctly"
        else
            info "Embedded ServerURL: ${server_url}"
            if is_placeholder_url "$server_url"; then
                fail "Embedded ServerURL is a placeholder: ${server_url}"
                info "  The image was built with an unconfigured OTA_SERVER_URL"
                info "  Set it in local.conf:  OTA_SERVER_URL = \"https://your-server.com\""
            elif [[ "$server_url" == http://* ]] || [[ "$server_url" == https://* ]]; then
                pass "Embedded ServerURL is valid: ${server_url}"
            else
                warn "ServerURL does not start with http:// or https://: ${server_url}"
            fi
        fi
    fi
fi

# ═══════════════════════════════════════════════════════════════════════════
# [5/6] Security
# Checks signing status of the .mender artifact.
# ═══════════════════════════════════════════════════════════════════════════
section "5/6" "Security"

signed_count=0
unsigned_count=0

if [[ "${#MENDER_FILES[@]}" -gt 0 ]]; then
    for f in "${MENDER_FILES[@]}"; do
        [[ -z "$f" ]] && continue
        base=$(basename "$f")
        if [[ "$base" == *"-unsigned"* ]]; then
            unsigned_count=$((unsigned_count + 1))
            info "Unsigned artifact: ${base}"
        else
            signed_count=$((signed_count + 1))
            info "Signed artifact:   ${base}"
        fi
    done
fi

if [[ $signed_count -gt 0 ]]; then
    pass "Signed artifact(s) present: ${signed_count} file(s)"
elif [[ $unsigned_count -gt 0 ]]; then
    warn "Only unsigned artifacts found — do not deploy to production"
    info "  Enable: SOC_OTA_SIGNATURE_VERIFICATION = \"1\""
    info "          SOC_OTA_SIGNING_KEY = \"/path/to/private.key\""
else
    warn "No .mender artifacts found — cannot assess signing status"
fi

# Cross-check using mender-artifact read if available
MENDER_BIN=$(find_mender_artifact_bin)
if [[ -n "$MENDER_BIN" ]] && [[ "${#MENDER_FILES[@]}" -gt 0 ]]; then
    first_mender="${MENDER_FILES[0]}"
    if [[ -n "$first_mender" ]] && [[ -f "$first_mender" ]]; then
        sig_out=$("$MENDER_BIN" read "$first_mender" 2>/dev/null || true)
        if echo "$sig_out" | grep -qiE "(signature|signed)"; then
            pass "mender-artifact read confirms signature present"
        else
            warn "mender-artifact read: no signature detected"
            info "  Artifact will be rejected by devices with SOC_OTA_SIGNATURE_VERIFICATION=1"
        fi
    fi
fi

# ═══════════════════════════════════════════════════════════════════════════
# [6/6] Deployable Files
# Lists all output files with sizes for a quick deployment overview.
# ═══════════════════════════════════════════════════════════════════════════
section "6/6" "Deployable Files"

deploy_count=0
for suffix in ".mender" ".wic" ".wic.gz" ".wic.bz2" ".img" ".ext4"; do
    mapfile -t files < <(find_artifacts "$suffix")
    for f in "${files[@]}"; do
        [[ -z "$f" ]] && continue
        size=$(human_size "$f")
        printf "  ${GREEN}%-8s${RESET}  %s\n" "$size" "$(basename "$f")"
        deploy_count=$((deploy_count + 1))
    done
done

if [[ $deploy_count -eq 0 ]]; then
    fail "No deployable files found in ${DEPLOY_DIR}"
else
    pass "${deploy_count} deployable file(s) listed above"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
printf "\n"
printf '═%.0s' {1..60}; printf '\n'
printf "${BOLD}Summary:${RESET}  "
printf "${GREEN}${PASSED} passed${RESET}   "
printf "${YELLOW}${WARNINGS} warnings${RESET}   "
printf "${RED}${FAILURES} failures${RESET}\n"
printf '═%.0s' {1..60}; printf '\n'

if [[ $FAILURES -gt 0 ]]; then
    printf "\n${RED}${BOLD}Result: FAILED${RESET} — fix the issues above before deploying.\n\n"
    exit 1
elif [[ $WARNINGS -gt 0 ]]; then
    printf "\n${YELLOW}${BOLD}Result: PASSED with warnings${RESET} — review before shipping to production.\n\n"
    exit 0
else
    printf "\n${GREEN}${BOLD}Result: ALL CHECKS PASSED${RESET}\n\n"
    exit 0
fi
