#!/bin/bash
#
# OTA-Pulse Buildroot Sanity Check Script
#
# Validates OTAPulse configuration in a Buildroot build.
# Runs as a post-build script BEFORE post-image.sh; receives TARGET_DIR as $1.
#
# Environment variables:
#   OTAPULSE_SANITY_LEVEL  - "error" (default, blocks build), "warn" (continue), or "off" (skip)
#   OTAPULSE_SANITY_SKIP   - space-separated check names to skip
#
# Check names: server  provisioning  mender-artifact  partitions  signing
#              device-type  packages  systemd  config-content  networking
#
# Note: set -e is intentionally omitted — checks accumulate failures rather
# than aborting on the first non-zero return code.
#

BOARD_DIR="$(dirname $0)"
TARGET_DIR="$1"

SANITY_LEVEL="${OTAPULSE_SANITY_LEVEL:-error}"
SANITY_SKIP="${OTAPULSE_SANITY_SKIP:-}"

PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0
SKIP_COUNT=0

# ==============================================================================
# Color output helpers
# ==============================================================================

GREEN="\033[0;32m"
RED="\033[0;31m"
YELLOW="\033[0;33m"
BLUE="\033[0;34m"
RESET="\033[0m"

pass() {
    echo -e "  ${GREEN}✓${RESET} $*"
    PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
    local check_name="$1"
    shift
    if [ "${SANITY_LEVEL}" = "error" ]; then
        echo -e "  ${RED}✗${RESET} $*"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    else
        echo -e "  ${YELLOW}!${RESET} $*"
        WARN_COUNT=$((WARN_COUNT + 1))
    fi
}

warn() {
    echo -e "  ${YELLOW}!${RESET} $*"
    WARN_COUNT=$((WARN_COUNT + 1))
}

skip() {
    echo -e "  ${BLUE}-${RESET} $*"
    SKIP_COUNT=$((SKIP_COUNT + 1))
}

info() {
    echo -e "    ${BLUE}→${RESET} $*"
}

# ==============================================================================
# BR2_CONFIG helpers
# ==============================================================================

# Auto-detect BR2_CONFIG if not set
if [ -z "${BR2_CONFIG:-}" ]; then
    if [ -n "${BUILD_DIR:-}" ] && [ -f "${BUILD_DIR}/../.config" ]; then
        BR2_CONFIG="${BUILD_DIR}/../.config"
    elif [ -f "$(dirname "${TARGET_DIR}")/../.config" ]; then
        BR2_CONFIG="$(dirname "${TARGET_DIR}")/../.config"
    elif [ -n "${O:-}" ] && [ -f "${O}/.config" ]; then
        BR2_CONFIG="${O}/.config"
    fi
fi

# Read a quoted string value from BR2_CONFIG
# Usage: br2_get BR2_PACKAGE_OTAPULSE_SERVER_URL
br2_get() {
    sed -n "s/^${1}=\"\(.*\)\"$/\1/p" "${BR2_CONFIG:-/dev/null}"
}

# Check if a boolean config option is set to =y in BR2_CONFIG
# Usage: br2_is_set BR2_TARGET_ROOTFS_EXT2
br2_is_set() {
    grep -q "^${1}=y$" "${BR2_CONFIG:-/dev/null}" 2>/dev/null
}

# Check if a check name is in SANITY_SKIP
check_skipped() {
    echo "$SANITY_SKIP" | grep -qw "$1" 2>/dev/null
}

# ==============================================================================
# Placeholder detection helpers
# ==============================================================================

is_placeholder_url() {
    echo "$1" | grep -qiE \
        "ota\.example\.com|your-ota-server\.com|your-server\.com|\
otapulse\.example\.com|monitoring\.example\.com|docker\.mender\.io|\
CHANGE_ME"
}

is_placeholder_token() {
    echo "$1" | grep -qiE \
        "your-tenant-token|your-token-here|prov_xxxx|YOUR_TOKEN|CHANGE_ME|Paste your"
}

# ==============================================================================
# Begin
# ==============================================================================

echo "=== OTA-Pulse Sanity Check ==="

if [ "${SANITY_LEVEL}" = "off" ]; then
    echo "Sanity checks disabled (OTAPULSE_SANITY_LEVEL=off)."
    exit 0
fi

if [ -z "${TARGET_DIR}" ]; then
    echo "ERROR: TARGET_DIR not set (pass as \$1)."
    exit 1
fi

echo "  Target:  ${TARGET_DIR}"
echo "  Config:  ${BR2_CONFIG:-<not found>}"
echo "  Level:   ${SANITY_LEVEL}"
[ -n "${SANITY_SKIP}" ] && echo "  Skipping: ${SANITY_SKIP}"
echo ""

# ==============================================================================
# [1/10] Server URL
# ==============================================================================

echo "[1/10] Server URL"

if check_skipped "server"; then
    skip "server check skipped"
else
    SERVER_URL="$(br2_get BR2_PACKAGE_OTAPULSE_SERVER_URL)"

    if [ -z "${SERVER_URL}" ]; then
        fail "server" "OTA server URL is not set."
        info "Set BR2_PACKAGE_OTAPULSE_SERVER_URL in menuconfig or your defconfig."
    elif is_placeholder_url "${SERVER_URL}"; then
        fail "server" "OTA server URL is a placeholder: ${SERVER_URL}"
        info "Replace with your actual OTAPulse server URL."
    elif ! echo "${SERVER_URL}" | grep -q "^https\?://"; then
        fail "server" "OTA server URL does not start with http:// or https://: ${SERVER_URL}"
    else
        pass "OTA server URL: ${SERVER_URL}"

        # Also check the embedded config in the rootfs
        ROOTFS_CONF="${TARGET_DIR}/etc/otapulse/otapulse.conf"
        if [ -f "${ROOTFS_CONF}" ]; then
            CONF_URL="$(grep -o '"ServerURL"[[:space:]]*:[[:space:]]*"[^"]*"' \
                        "${ROOTFS_CONF}" 2>/dev/null | \
                        sed 's/.*"ServerURL"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/' || true)"
            if [ -n "${CONF_URL}" ] && is_placeholder_url "${CONF_URL}"; then
                fail "server" "Embedded rootfs config contains placeholder URL: ${CONF_URL}"
                info "Check: ${ROOTFS_CONF}"
            fi
        fi
    fi
fi

echo ""

# ==============================================================================
# [2/10] Provisioning Credentials
# ==============================================================================

echo "[2/10] Provisioning Credentials"

if check_skipped "provisioning"; then
    skip "provisioning check skipped"
else
    TENANT_TOKEN="$(br2_get BR2_PACKAGE_OTAPULSE_TENANT_TOKEN)"
    PROV_TOKEN="$(br2_get BR2_PACKAGE_OTAPULSE_PROVISIONING_TOKEN)"

    TENANT_VALID=0
    PROV_VALID=0

    if [ -n "${TENANT_TOKEN}" ] && ! is_placeholder_token "${TENANT_TOKEN}"; then
        TENANT_VALID=1
    elif [ -n "${TENANT_TOKEN}" ]; then
        warn "Tenant token appears to be a placeholder: ${TENANT_TOKEN}"
    fi

    if [ -n "${PROV_TOKEN}" ] && ! is_placeholder_token "${PROV_TOKEN}"; then
        PROV_VALID=1
    elif [ -n "${PROV_TOKEN}" ]; then
        warn "Provisioning token appears to be a placeholder: ${PROV_TOKEN}"
    fi

    # Look for a provisioning tool in the rootfs
    PROV_TOOL=""
    if [ -x "${TARGET_DIR}/usr/bin/soc-ota-provision" ]; then
        PROV_TOOL="${TARGET_DIR}/usr/bin/soc-ota-provision"
    elif [ -x "${TARGET_DIR}/usr/bin/otapulse-provision" ]; then
        PROV_TOOL="${TARGET_DIR}/usr/bin/otapulse-provision"
    fi

    if [ "${TENANT_VALID}" -eq 0 ] && [ "${PROV_VALID}" -eq 0 ]; then
        if [ -n "${PROV_TOOL}" ]; then
            warn "No valid credential set — provisioning tool present, assuming manual mode."
            info "Tool: ${PROV_TOOL}"
        else
            fail "provisioning" "No valid credential and no provisioning tool found in rootfs."
            info "Set BR2_PACKAGE_OTAPULSE_TENANT_TOKEN or BR2_PACKAGE_OTAPULSE_PROVISIONING_TOKEN."
        fi
    else
        [ "${TENANT_VALID}" -eq 1 ] && pass "Tenant token configured."
        [ "${PROV_VALID}"  -eq 1 ] && pass "Provisioning token configured."
    fi
fi

echo ""

# ==============================================================================
# [3/10] Mender Artifact Tooling
# ==============================================================================

echo "[3/10] Mender Artifact Tooling"

if check_skipped "mender-artifact"; then
    skip "mender-artifact check skipped"
else
    MENDER_ARTIFACT=""
    if [ -x "${HOST_DIR:-}/bin/mender-artifact" ]; then
        MENDER_ARTIFACT="${HOST_DIR}/bin/mender-artifact"
    elif command -v mender-artifact >/dev/null 2>&1; then
        MENDER_ARTIFACT="$(command -v mender-artifact)"
    fi

    if [ -z "${MENDER_ARTIFACT}" ]; then
        fail "mender-artifact" "mender-artifact tool not found."
        info "Enable BR2_PACKAGE_HOST_MENDER_ARTIFACT=y in your defconfig."
    else
        pass "mender-artifact found: ${MENDER_ARTIFACT}"
    fi

    if br2_is_set BR2_TARGET_ROOTFS_EXT2; then
        pass "BR2_TARGET_ROOTFS_EXT2 is set."
    else
        fail "mender-artifact" "BR2_TARGET_ROOTFS_EXT2 is not set — ext4 rootfs image required for Mender artifacts."
        info "Add BR2_TARGET_ROOTFS_EXT2=y and BR2_TARGET_ROOTFS_EXT2_4=y to your defconfig."
    fi
fi

echo ""

# ==============================================================================
# [4/10] A/B Partition Layout
# ==============================================================================

echo "[4/10] A/B Partition Layout"

if check_skipped "partitions"; then
    skip "partitions check skipped"
else
    GENIMAGE_CFG_OVERRIDE="$(br2_get BR2_PACKAGE_OTAPULSE_GENIMAGE_CFG)"
    GENIMAGE_FOUND=0
    PARTITION_MODE="static"

    if [ -n "${GENIMAGE_CFG_OVERRIDE}" ] && [ -f "${GENIMAGE_CFG_OVERRIDE}" ]; then
        pass "Genimage config: ${GENIMAGE_CFG_OVERRIDE}"
        GENIMAGE_FOUND=1
    elif [ -f "${BOARD_DIR}/genimage-ab.cfg" ]; then
        pass "Genimage config: ${BOARD_DIR}/genimage-ab.cfg (default)"
        GENIMAGE_FOUND=1
    fi

    if br2_is_set BR2_PACKAGE_OTAPULSE_PARTITION_DYNAMIC; then
        PARTITION_MODE="dynamic"

        # Check for firstboot script or service in rootfs
        if [ -x "${TARGET_DIR}/usr/sbin/otapulse-firstboot" ] || \
           [ -f "${TARGET_DIR}/usr/lib/systemd/system/otapulse-firstboot.service" ]; then
            pass "Dynamic partitioning: firstboot script/service present."
        else
            warn "Dynamic partitioning: firstboot script not found in rootfs."
            info "Expected: ${TARGET_DIR}/usr/sbin/otapulse-firstboot"
        fi
    else
        if [ "${GENIMAGE_FOUND}" -eq 0 ]; then
            fail "partitions" "Static partitioning selected but no genimage config found."
            info "Set BR2_PACKAGE_OTAPULSE_GENIMAGE_CFG or provide ${BOARD_DIR}/genimage-ab.cfg."
        fi
    fi

    [ "${GENIMAGE_FOUND}" -eq 1 ] && info "Partition mode: ${PARTITION_MODE}"
fi

echo ""

# ==============================================================================
# [5/10] Signing & Verification Keys
# ==============================================================================

echo "[5/10] Signing & Verification Keys"

if check_skipped "signing"; then
    skip "signing check skipped"
else
    VERIFY_KEY="$(br2_get BR2_PACKAGE_OTAPULSE_VERIFY_KEY)"

    if [ -z "${VERIFY_KEY}" ]; then
        fail "signing" "Artifact verification key is not set (signature verification is mandatory)."
        info "Set BR2_PACKAGE_OTAPULSE_VERIFY_KEY to your public key path."
    elif [ ! -f "${VERIFY_KEY}" ]; then
        fail "signing" "Verification key file not found: ${VERIFY_KEY}"
    else
        pass "Verification key: ${VERIFY_KEY}"

        # Validate key format with openssl
        if openssl rsa -pubin -in "${VERIFY_KEY}" -noout 2>/dev/null; then
            KEY_BITS="$(openssl rsa -pubin -in "${VERIFY_KEY}" -text -noout 2>/dev/null | \
                        grep "^Public-Key:" | grep -o '[0-9]*' || true)"
            [ -n "${KEY_BITS}" ] && info "RSA public key: ${KEY_BITS} bits"
            if [ -n "${KEY_BITS}" ] && [ "${KEY_BITS}" -lt 2048 ] 2>/dev/null; then
                warn "RSA key is less than 2048 bits (${KEY_BITS}); recommend 4096."
            fi
        elif openssl ec -pubin -in "${VERIFY_KEY}" -noout 2>/dev/null; then
            info "ECDSA public key validated."
        else
            warn "Could not validate key format with openssl."
        fi

        # Check key is installed in the rootfs
        ROOTFS_KEY="${TARGET_DIR}/etc/otapulse/keys/artifact-verify-key.pem"
        if [ -f "${ROOTFS_KEY}" ]; then
            pass "Verification key installed in rootfs."
        else
            warn "Verification key not found in rootfs: ${ROOTFS_KEY}"
            info "Make sure BR2_PACKAGE_OTAPULSE_VERIFY_KEY points to the correct file."
        fi
    fi

    SIGNING_KEY="$(br2_get BR2_PACKAGE_OTAPULSE_SIGNING_KEY)"

    if [ -z "${SIGNING_KEY}" ]; then
        warn "Artifact signing key is not set."
        info "Set BR2_PACKAGE_OTAPULSE_SIGNING_KEY to your private key path."
    elif [ ! -f "${SIGNING_KEY}" ]; then
        fail "signing" "Signing key file not found: ${SIGNING_KEY}"
        info "Set BR2_PACKAGE_OTAPULSE_SIGNING_KEY to a valid private key path."
    else
        pass "Signing key: ${SIGNING_KEY}"

        # Validate it is a private key
        if openssl rsa -in "${SIGNING_KEY}" -noout 2>/dev/null || \
           openssl ec  -in "${SIGNING_KEY}" -noout 2>/dev/null; then
            info "Private key format validated."
        else
            warn "Could not validate signing key format with openssl."
        fi
    fi
fi

echo ""

# ==============================================================================
# [6/10] Device Type
# ==============================================================================

echo "[6/10] Device Type"

if check_skipped "device-type"; then
    skip "device-type check skipped"
else
    DEVICE_TYPE="$(br2_get BR2_PACKAGE_OTAPULSE_DEVICE_TYPE)"

    if [ -z "${DEVICE_TYPE}" ]; then
        fail "device-type" "Device type is not set."
        info "Set BR2_PACKAGE_OTAPULSE_DEVICE_TYPE in menuconfig or your defconfig."
    elif [ "${DEVICE_TYPE}" = "generic-arm" ]; then
        fail "device-type" "Device type is still the placeholder default: ${DEVICE_TYPE}"
        info "Set a unique identifier, e.g. rpi4-64, imx8mp-evk, rockchip-rk3588."
    else
        pass "Device type: ${DEVICE_TYPE}"
    fi

    # Cross-check against the rootfs device_type file
    ROOTFS_DT="${TARGET_DIR}/etc/otapulse/device_type"
    if [ -f "${ROOTFS_DT}" ]; then
        ROOTFS_DEVICE_TYPE="$(cat "${ROOTFS_DT}" 2>/dev/null | tr -d '[:space:]' || true)"
        if [ -n "${DEVICE_TYPE}" ] && [ -n "${ROOTFS_DEVICE_TYPE}" ] && \
           [ "${DEVICE_TYPE}" != "${ROOTFS_DEVICE_TYPE}" ]; then
            warn "Device type mismatch: BR2 config='${DEVICE_TYPE}', rootfs='${ROOTFS_DEVICE_TYPE}'."
        fi
    fi
fi

echo ""

# ==============================================================================
# [7/10] Required Packages in Rootfs
# ==============================================================================

echo "[7/10] Required Packages in Rootfs"

if check_skipped "packages"; then
    skip "packages check skipped"
else
    # Required: at least one OTA agent binary (symlinks count)
    OTA_BIN=""
    for bin in otapulse soc-ota-agent mender; do
        if [ -f "${TARGET_DIR}/usr/bin/${bin}" ]; then
            OTA_BIN="${bin}"
            break
        fi
    done

    if [ -n "${OTA_BIN}" ]; then
        pass "OTA agent binary found: /usr/bin/${OTA_BIN}"
    else
        fail "packages" "No OTA agent binary found in rootfs."
        info "Expected one of: /usr/bin/otapulse, /usr/bin/soc-ota-agent, /usr/bin/mender"
        info "Ensure BR2_PACKAGE_OTAPULSE=y is set."
    fi

    # Recommended packages (warn only)
    for rec_bin in memfaultd sgdisk fw_printenv; do
        if [ -f "${TARGET_DIR}/usr/bin/${rec_bin}" ] || \
           [ -f "${TARGET_DIR}/usr/sbin/${rec_bin}" ]; then
            info "Recommended package present: ${rec_bin}"
        else
            warn "Recommended package not found: ${rec_bin}"
        fi
    done
fi

echo ""

# ==============================================================================
# [8/10] Init System
# ==============================================================================

echo "[8/10] Init System"

if check_skipped "systemd"; then
    skip "systemd check skipped"
else
    if [ -d "${TARGET_DIR}/usr/lib/systemd" ] || [ -d "${TARGET_DIR}/lib/systemd" ]; then
        pass "systemd found in rootfs."
    else
        fail "systemd" "systemd not found in rootfs."
        info "OTA-Pulse requires BR2_INIT_SYSTEMD=y."
    fi

    # Check OTA agent service file
    OTA_SERVICE=""
    for svc in otapulse.service soc-ota-agent.service; do
        if [ -f "${TARGET_DIR}/usr/lib/systemd/system/${svc}" ]; then
            OTA_SERVICE="${svc}"
            break
        fi
    done

    if [ -n "${OTA_SERVICE}" ]; then
        pass "OTA agent service: ${OTA_SERVICE}"
    else
        fail "systemd" "OTA agent systemd service not found."
        info "Expected: ${TARGET_DIR}/usr/lib/systemd/system/otapulse.service"
        info "Ensure BR2_PACKAGE_OTAPULSE_SYSTEMD=y is set."
    fi

    # Monitoring service (warn only)
    if [ -f "${TARGET_DIR}/usr/lib/systemd/system/memfaultd.service" ]; then
        info "Monitoring service present: memfaultd.service"
    else
        warn "Monitoring service not found: memfaultd.service"
    fi
fi

echo ""

# ==============================================================================
# [9/10] Config Content in Rootfs
# ==============================================================================

echo "[9/10] Config Content in Rootfs"

if check_skipped "config-content"; then
    skip "config-content check skipped"
else
    CONF_FILE=""
    if [ -f "${TARGET_DIR}/etc/otapulse/otapulse.conf" ]; then
        CONF_FILE="${TARGET_DIR}/etc/otapulse/otapulse.conf"
    elif [ -f "${TARGET_DIR}/etc/mender/mender.conf" ]; then
        CONF_FILE="${TARGET_DIR}/etc/mender/mender.conf"
    fi

    if [ -z "${CONF_FILE}" ]; then
        fail "config-content" "No OTAPulse config file found in rootfs."
        info "Expected: ${TARGET_DIR}/etc/otapulse/otapulse.conf"
    else
        pass "Config file found: ${CONF_FILE}"

        # Validate JSON syntax with jq if available
        if command -v jq >/dev/null 2>&1; then
            if jq empty "${CONF_FILE}" 2>/dev/null; then
                info "JSON syntax valid."
            else
                fail "config-content" "Config file contains invalid JSON: ${CONF_FILE}"
            fi
        fi

        # Check for placeholder values
        if grep -qiE "example\.com|CHANGE_ME|your-.*server|your-.*token|Paste.your" \
                "${CONF_FILE}" 2>/dev/null; then
            fail "config-content" "Config file contains placeholder values."
            info "Replace all example/CHANGE_ME values before shipping: ${CONF_FILE}"
        fi

        # Extract and check ServerURL from the embedded config
        CONF_URL="$(grep -o '"ServerURL"[[:space:]]*:[[:space:]]*"[^"]*"' \
                    "${CONF_FILE}" 2>/dev/null | \
                    sed 's/.*"ServerURL"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/' || true)"
        if [ -n "${CONF_URL}" ] && is_placeholder_url "${CONF_URL}"; then
            fail "config-content" "ServerURL in embedded config is a placeholder: ${CONF_URL}"
        fi
    fi
fi

echo ""

# ==============================================================================
# [10/10] Network Stack
# ==============================================================================

echo "[10/10] Network Stack"

if check_skipped "networking"; then
    skip "networking check skipped"
else
    # Check for a network manager
    NET_MGR=""
    for mgr in NetworkManager connmand systemd-networkd wpa_supplicant; do
        if [ -f "${TARGET_DIR}/usr/sbin/${mgr}" ] || \
           [ -f "${TARGET_DIR}/usr/bin/${mgr}" ]; then
            NET_MGR="${mgr}"
            break
        fi
    done

    if [ -n "${NET_MGR}" ]; then
        pass "Network manager found: ${NET_MGR}"
    else
        warn "No network manager found (NetworkManager, connmand, systemd-networkd, wpa_supplicant)."
    fi

    # Check for an HTTP client
    if [ -f "${TARGET_DIR}/usr/bin/curl"  ] || [ -f "${TARGET_DIR}/usr/sbin/curl"  ] || \
       [ -f "${TARGET_DIR}/usr/bin/wget"  ] || [ -f "${TARGET_DIR}/usr/sbin/wget"  ]; then
        info "HTTP client present (curl/wget)."
    else
        warn "Neither curl nor wget found in rootfs."
    fi

    # CA certificates are required for TLS to the OTA server
    if [ -d "${TARGET_DIR}/etc/ssl/certs" ] && \
       [ -n "$(ls -A "${TARGET_DIR}/etc/ssl/certs" 2>/dev/null)" ]; then
        pass "CA certificates found: ${TARGET_DIR}/etc/ssl/certs"
    else
        fail "networking" "CA certificates not found at ${TARGET_DIR}/etc/ssl/certs."
        info "Enable BR2_PACKAGE_CA_CERTIFICATES=y in your defconfig."
    fi
fi

echo ""

# ==============================================================================
# Summary
# ==============================================================================

echo "┌─────────────────────────────────────────┐"
echo "│       OTA-Pulse Sanity Check Summary     │"
echo "│                                         │"
printf "│   Passed:    %-27s │\n" "${PASS_COUNT}"
printf "│   Failed:    %-27s │\n" "${FAIL_COUNT}"
printf "│   Warnings:  %-27s │\n" "${WARN_COUNT}"
printf "│   Skipped:   %-27s │\n" "${SKIP_COUNT}"
echo "│                                         │"
echo "└─────────────────────────────────────────┘"

if [ "${FAIL_COUNT}" -gt 0 ]; then
    if [ "${SANITY_LEVEL}" = "error" ]; then
        echo ""
        echo "FATAL: ${FAIL_COUNT} sanity check(s) failed. Fix the errors above and rebuild."
        echo "       Set OTAPULSE_SANITY_LEVEL=warn to demote failures to warnings."
        exit 1
    else
        echo ""
        echo "WARNING: ${FAIL_COUNT} sanity check(s) failed (OTAPULSE_SANITY_LEVEL=warn, continuing)."
    fi
fi

echo "=== OTA-Pulse Sanity Check Complete ==="
exit 0
