#!/bin/bash
# otapulse-precheck.sh — Pre-build configuration validator for OTAPulse
#
# Runs BEFORE a build to catch configuration errors early, saving 30+ minutes
# of failed build time. Validates server URL, signing keys, device type, and
# build-system-specific settings.
#
# Usage:
#   otapulse-precheck.sh [OPTIONS] [BUILD_DIR]
#
# Options:
#   --yocto          Force Yocto mode (auto-detected by default)
#   --buildroot      Force Buildroot mode (auto-detected by default)
#   --config FILE    Path to otapulse.conf to validate directly
#   -h, --help       Show this help message
#
# Exit codes:
#   0  All checks passed
#   1  One or more checks failed

set -euo pipefail

# ---------------------------------------------------------------------------
# Terminal colours
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

pass() { printf "${GREEN}  [PASS]${RESET} %s\n" "$*"; PASSED=$((PASSED + 1)); }
fail() { printf "${RED}  [FAIL]${RESET} %s\n" "$*"; FAILURES=$((FAILURES + 1)); }
warn() { printf "${YELLOW}  [WARN]${RESET} %s\n" "$*"; WARNINGS=$((WARNINGS + 1)); }
info() { printf "${BLUE}  [INFO]${RESET} %s\n" "$*"; }

section() {
    local num="$1"; shift
    printf "\n${BOLD}${BLUE}── [%s] %s${RESET}\n" "$num" "$*"
    printf '─%.0s' {1..55}
    printf '\n'
}

# Placeholder patterns (shared with otapulse-verify.sh and otapulse-configure.sh)
PLACEHOLDER_PATTERNS=(
    "CHANGE_ME"
    "example.com"
    "your-ota-server"
    "your-server"
    "192.168.0.122"
    "docker.mender.io"
    "localhost"
)

is_placeholder() {
    local value="$1"
    for pat in "${PLACEHOLDER_PATTERNS[@]}"; do
        [[ "$value" == *"$pat"* ]] && return 0
    done
    return 1
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
BUILD_DIR=""
FORCE_MODE=""
CONFIG_FILE=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --yocto)      FORCE_MODE="yocto"; shift ;;
        --buildroot)  FORCE_MODE="buildroot"; shift ;;
        --config)
            [[ $# -ge 2 ]] || { printf "${RED}Option $1 requires an argument${RESET}\n" >&2; exit 1; }
            CONFIG_FILE="$2"; shift 2 ;;
        -h|--help)
            sed -En '2,/^$/s/^# ?//p' "$0"
            exit 0
            ;;
        -*) printf "${RED}Unknown option: %s${RESET}\n" "$1" >&2; exit 1 ;;
        *)  BUILD_DIR="$1"; shift ;;
    esac
done

BUILD_DIR="${BUILD_DIR:-build}"

# ---------------------------------------------------------------------------
# Detect build system
# ---------------------------------------------------------------------------
BUILD_SYSTEM="$FORCE_MODE"

if [[ -z "$BUILD_SYSTEM" ]]; then
    if [[ -f "${BUILD_DIR}/conf/local.conf" ]]; then
        BUILD_SYSTEM="yocto"
    elif [[ -f "${BUILD_DIR}/.config" ]]; then
        BUILD_SYSTEM="buildroot"
    fi
fi

# ---------------------------------------------------------------------------
# Banner
# ---------------------------------------------------------------------------
printf "\n${BOLD}OTAPulse Pre-Build Configuration Check${RESET}\n"
printf '═%.0s' {1..55}; printf '\n'
info "Build dir:    ${BUILD_DIR}"
info "Build system: ${BUILD_SYSTEM:-standalone}"
[[ -n "$CONFIG_FILE" ]] && info "Config file:  ${CONFIG_FILE}"

# ═══════════════════════════════════════════════════════════════════════════
# [1/5] Server URL Validation
# ═══════════════════════════════════════════════════════════════════════════
section "1/5" "Server URL"

SERVER_URL=""

# Extract from direct config file
if [[ -n "$CONFIG_FILE" ]] && [[ -f "$CONFIG_FILE" ]]; then
    SERVER_URL=$(sed -n 's/.*"ServerURL"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$CONFIG_FILE" | head -1 2>/dev/null || true)
fi

# Extract from Yocto local.conf
if [[ -z "$SERVER_URL" ]] && [[ "$BUILD_SYSTEM" == "yocto" ]]; then
    local_conf="${BUILD_DIR}/conf/local.conf"
    if [[ -f "$local_conf" ]]; then
        # Match OTA_SERVER_URL = "..." or OTAPULSE_SERVER_URL = "..."
        SERVER_URL=$(grep -E '^\s*(OTA_SERVER_URL|OTAPULSE_SERVER_URL)\s*=' "$local_conf" \
            | tail -1 \
            | sed -E 's/[^=]+=\s*"?([^"# ]+)"?.*/\1/' \
            | tr -d ' "' || true)
    fi
fi

# Extract from Buildroot .config
if [[ -z "$SERVER_URL" ]] && [[ "$BUILD_SYSTEM" == "buildroot" ]]; then
    br_config="${BUILD_DIR}/.config"
    if [[ -f "$br_config" ]]; then
        SERVER_URL=$(sed -n 's/^BR2_PACKAGE_OTAPULSE_SERVER_URL="\(.*\)"$/\1/p' "$br_config" || true)
    fi
fi

if [[ -z "$SERVER_URL" ]]; then
    fail "Server URL not found in configuration"
    info "  Set OTA_SERVER_URL in local.conf or ServerURL in otapulse.conf"
elif is_placeholder "$SERVER_URL"; then
    fail "Server URL is a placeholder: ${SERVER_URL}"
    info "  Replace with your actual OTAPulse server URL"
elif [[ "$SERVER_URL" != http://* ]] && [[ "$SERVER_URL" != https://* ]]; then
    fail "Server URL must start with http:// or https://: ${SERVER_URL}"
else
    pass "Server URL: ${SERVER_URL}"
    if [[ "$SERVER_URL" == http://* ]]; then
        warn "Using HTTP (not HTTPS) — not recommended for production"
    fi
fi

# ═══════════════════════════════════════════════════════════════════════════
# [2/5] Device Type
# ═══════════════════════════════════════════════════════════════════════════
section "2/5" "Device Type"

DEVICE_TYPE=""

if [[ -n "$CONFIG_FILE" ]] && [[ -f "$CONFIG_FILE" ]]; then
    # Check for device_type file alongside config
    config_dir=$(dirname "$CONFIG_FILE")
    if [[ -f "${config_dir}/device_type" ]]; then
        DEVICE_TYPE=$(sed -n 's/^device_type=//p' "${config_dir}/device_type" | tr -d ' ')
    fi
fi

if [[ -z "$DEVICE_TYPE" ]] && [[ "$BUILD_SYSTEM" == "yocto" ]]; then
    local_conf="${BUILD_DIR}/conf/local.conf"
    if [[ -f "$local_conf" ]]; then
        # Check MENDER_DEVICE_TYPE first, then MACHINE
        DEVICE_TYPE=$(grep -E '^\s*MENDER_DEVICE_TYPE\s*=' "$local_conf" \
            | tail -1 \
            | sed -E 's/[^=]+=\s*"?([^"# ]+)"?.*/\1/' \
            | tr -d ' "' || true)
        if [[ -z "$DEVICE_TYPE" ]] || [[ "$DEVICE_TYPE" == *'$'* ]]; then
            DEVICE_TYPE=$(grep -E '^\s*MACHINE\s*[?]?=' "$local_conf" \
                | tail -1 \
                | sed -E 's/[^=]+=\s*"?([^"# ]+)"?.*/\1/' \
                | tr -d ' "' || true)
        fi
    fi
fi

if [[ -z "$DEVICE_TYPE" ]] && [[ "$BUILD_SYSTEM" == "buildroot" ]]; then
    br_config="${BUILD_DIR}/.config"
    if [[ -f "$br_config" ]]; then
        DEVICE_TYPE=$(sed -n 's/^BR2_PACKAGE_OTAPULSE_DEVICE_TYPE="\(.*\)"$/\1/p' "$br_config" || true)
    fi
fi

if [[ -z "$DEVICE_TYPE" ]]; then
    fail "Device type not configured"
    info "  Set MENDER_DEVICE_TYPE in local.conf or BR2_PACKAGE_OTAPULSE_DEVICE_TYPE"
elif is_placeholder "$DEVICE_TYPE"; then
    fail "Device type is a placeholder: ${DEVICE_TYPE}"
else
    pass "Device type: ${DEVICE_TYPE}"
fi

# ═══════════════════════════════════════════════════════════════════════════
# [3/5] Signing / Verification Keys
# ═══════════════════════════════════════════════════════════════════════════
section "3/5" "Signing & Verification Keys"

VERIFY_KEY=""

if [[ -n "$CONFIG_FILE" ]] && [[ -f "$CONFIG_FILE" ]]; then
    VERIFY_KEY=$(sed -n 's/.*"ArtifactVerifyKey"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$CONFIG_FILE" 2>/dev/null | head -1 || true)
    if [[ -z "$VERIFY_KEY" ]]; then
        # Check ArtifactVerifyKeys array
        VERIFY_KEY=$(sed -n 's/.*"ArtifactVerifyKeys"[[:space:]]*:[[:space:]]*\[[[:space:]]*"\([^"]*\)".*/\1/p' "$CONFIG_FILE" 2>/dev/null | head -1 || true)
    fi
fi

if [[ -z "$VERIFY_KEY" ]] && [[ "$BUILD_SYSTEM" == "yocto" ]]; then
    local_conf="${BUILD_DIR}/conf/local.conf"
    if [[ -f "$local_conf" ]]; then
        VERIFY_KEY=$(grep -E '^\s*SOC_OTA_ARTIFACT_VERIFY_KEY\s*=' "$local_conf" \
            | tail -1 \
            | sed -E 's/[^=]+=\s*"?([^"# ]+)"?.*/\1/' \
            | tr -d ' "' || true)
    fi
fi

if [[ -z "$VERIFY_KEY" ]] && [[ "$BUILD_SYSTEM" == "buildroot" ]]; then
    br_config="${BUILD_DIR}/.config"
    if [[ -f "$br_config" ]]; then
        VERIFY_KEY=$(sed -n 's/^BR2_PACKAGE_OTAPULSE_VERIFY_KEY="\(.*\)"$/\1/p' "$br_config" || true)
    fi
fi

if [[ -z "$VERIFY_KEY" ]]; then
    warn "No artifact verification key configured"
    info "  Artifacts will not be signature-verified — not recommended for production"
    info "  Generate keys with: otapulse-configure.sh --generate-keys"
elif [[ ! -f "$VERIFY_KEY" ]]; then
    fail "Verification key file not found: ${VERIFY_KEY}"
else
    # Validate PEM format
    if grep -q "BEGIN.*KEY" "$VERIFY_KEY" 2>/dev/null; then
        pass "Verification key valid: ${VERIFY_KEY}"
        # Check if it's a private key (wrong file)
        if grep -q "PRIVATE KEY" "$VERIFY_KEY" 2>/dev/null; then
            fail "Verification key is a PRIVATE key — use the PUBLIC key instead"
            info "  Extract public key: openssl rsa -in $VERIFY_KEY -pubout -out verify.pem"
        fi
    else
        fail "Verification key is not valid PEM format: ${VERIFY_KEY}"
    fi
fi

# ═══════════════════════════════════════════════════════════════════════════
# [4/5] Build System Specific Checks
# ═══════════════════════════════════════════════════════════════════════════
section "4/5" "Build System Configuration"

if [[ "$BUILD_SYSTEM" == "yocto" ]]; then
    local_conf="${BUILD_DIR}/conf/local.conf"
    info "Checking Yocto configuration: ${local_conf}"

    if [[ ! -f "$local_conf" ]]; then
        fail "local.conf not found: ${local_conf}"
    else
        # Check INHERIT += "otapulse"
        if grep -qE '^\s*INHERIT\s*\+=' "$local_conf" && grep -q 'otapulse' "$local_conf"; then
            pass "OTAPulse class inherited"
        else
            warn "INHERIT += \"otapulse\" not found in local.conf"
            info "  Add: INHERIT += \"otapulse\""
        fi

        # Check IMAGE_FSTYPES includes ext4
        if grep -qE '^\s*IMAGE_FSTYPES' "$local_conf"; then
            if grep -E '^\s*IMAGE_FSTYPES' "$local_conf" | grep -q 'ext4'; then
                pass "ext4 in IMAGE_FSTYPES"
            else
                warn "ext4 not found in IMAGE_FSTYPES — needed for .mender artifacts"
            fi
        fi

        # Check for tenant token
        if grep -qE '^\s*OTAPULSE_PROVISIONING_TOKEN\s*=' "$local_conf"; then
            token=$(grep -E '^\s*OTAPULSE_PROVISIONING_TOKEN\s*=' "$local_conf" \
                | tail -1 | sed -E 's/[^=]+=\s*"?([^"#]*)"?.*/\1/' | tr -d ' "')
            if [[ -z "$token" ]] || is_placeholder "$token"; then
                warn "Tenant token appears to be a placeholder"
            else
                pass "Tenant token configured"
            fi
        else
            warn "OTAPULSE_PROVISIONING_TOKEN not set — device requires manual auth"
        fi
    fi

elif [[ "$BUILD_SYSTEM" == "buildroot" ]]; then
    br_config="${BUILD_DIR}/.config"
    info "Checking Buildroot configuration: ${br_config}"

    if [[ ! -f "$br_config" ]]; then
        fail ".config not found: ${br_config}"
    else
        # Check BR2_PACKAGE_OTAPULSE=y
        if grep -q '^BR2_PACKAGE_OTAPULSE=y' "$br_config"; then
            pass "BR2_PACKAGE_OTAPULSE enabled"
        else
            fail "BR2_PACKAGE_OTAPULSE not enabled in .config"
        fi

        # Check systemd init
        if grep -q '^BR2_INIT_SYSTEMD=y' "$br_config"; then
            pass "systemd init system selected"
        else
            warn "systemd not selected — OTAPulse agent requires systemd"
            info "  Set BR2_INIT_SYSTEMD=y in your defconfig"
        fi

        # Check required dependencies
        for dep in BR2_PACKAGE_OPENSSL BR2_PACKAGE_CA_CERTIFICATES; do
            if grep -q "^${dep}=y" "$br_config"; then
                pass "${dep} enabled"
            else
                warn "${dep} not enabled — may be needed"
            fi
        done

        # Check partition config
        for var in BR2_PACKAGE_OTAPULSE_ROOTFS_PART_A BR2_PACKAGE_OTAPULSE_ROOTFS_PART_B; do
            val=$(sed -n "s/^${var}=\"\\(.*\\)\"$/\\1/p" "$br_config" || true)
            if [[ -n "$val" ]]; then
                pass "${var}=${val}"
            else
                warn "${var} not set"
            fi
        done
    fi

elif [[ -n "$CONFIG_FILE" ]]; then
    info "Standalone config validation mode"
    if [[ -f "$CONFIG_FILE" ]]; then
        # Validate JSON syntax
        if command -v python3 &>/dev/null; then
            if python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$CONFIG_FILE" 2>/dev/null; then
                pass "Config file is valid JSON"
            else
                fail "Config file has invalid JSON syntax"
            fi
        elif command -v jq &>/dev/null; then
            if jq empty "$CONFIG_FILE" 2>/dev/null; then
                pass "Config file is valid JSON"
            else
                fail "Config file has invalid JSON syntax"
            fi
        else
            warn "No JSON validator available (install python3 or jq)"
        fi

        # Check required fields
        for field in ServerURL RootfsPartA RootfsPartB; do
            if grep -q "\"$field\"" "$CONFIG_FILE"; then
                pass "Field present: $field"
            else
                warn "Missing recommended field: $field"
            fi
        done
    fi
else
    warn "No build system detected and no --config specified"
    info "  Use --yocto, --buildroot, or --config FILE"
fi

# ═══════════════════════════════════════════════════════════════════════════
# [5/5] Tool Dependencies
# ═══════════════════════════════════════════════════════════════════════════
section "5/5" "Tool Dependencies"

# Check for mender-artifact
if command -v mender-artifact &>/dev/null; then
    ver=$(mender-artifact --version 2>&1 | head -1 || echo "unknown")
    pass "mender-artifact found: ${ver}"
else
    warn "mender-artifact not in PATH — needed for artifact creation and signing"
    info "  Download from: https://docs.mender.io/downloads"
fi

# Check for openssl
if command -v openssl &>/dev/null; then
    ver=$(openssl version 2>&1 | head -1 || echo "unknown")
    pass "openssl found: ${ver}"
else
    warn "openssl not found — needed for key generation"
fi

# Check for Go toolchain (if building agent from source)
if command -v go &>/dev/null; then
    ver=$(go version 2>&1 | head -1 || echo "unknown")
    pass "Go toolchain: ${ver}"
else
    info "Go toolchain not found (only needed for building agent from source)"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
printf "\n"
printf '═%.0s' {1..55}; printf '\n'
printf "${BOLD}Summary:${RESET}  "
printf "${GREEN}${PASSED} passed${RESET}   "
printf "${YELLOW}${WARNINGS} warnings${RESET}   "
printf "${RED}${FAILURES} failures${RESET}\n"
printf '═%.0s' {1..55}; printf '\n'

if [[ $FAILURES -gt 0 ]]; then
    printf "\n${RED}${BOLD}Result: FAILED${RESET} — fix configuration before building.\n"
    printf "  Run otapulse-configure.sh to generate a valid configuration.\n\n"
    exit 1
elif [[ $WARNINGS -gt 0 ]]; then
    printf "\n${YELLOW}${BOLD}Result: PASSED with warnings${RESET} — review before building.\n\n"
    exit 0
else
    printf "\n${GREEN}${BOLD}Result: ALL CHECKS PASSED${RESET} — ready to build!\n\n"
    exit 0
fi
