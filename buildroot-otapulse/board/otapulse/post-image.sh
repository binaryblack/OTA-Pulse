#!/bin/bash
#
# OTA-Pulse Buildroot Post-Image Script
#
# This script runs after Buildroot creates the filesystem image.
# It generates the final SD card image and optionally creates
# Mender-compatible OTA artifacts.
#

set -e

BOARD_DIR="$(dirname $0)"
GENIMAGE_CFG="${BOARD_DIR}/genimage-ab.cfg"
GENIMAGE_TMP="${BUILD_DIR}/genimage.tmp"

echo "=== OTA-Pulse Post-Image Script ==="

# Allow override of genimage config
if [ -n "${BR2_PACKAGE_OTAPULSE_GENIMAGE_CFG}" ]; then
    GENIMAGE_CFG="${BR2_PACKAGE_OTAPULSE_GENIMAGE_CFG}"
fi

# Export partition sizes for genimage
export OTAPULSE_ROOTFS_SIZE="${BR2_PACKAGE_OTAPULSE_ROOTFS_SIZE:-1G}"
export OTAPULSE_DATA_SIZE="${BR2_PACKAGE_OTAPULSE_DATA_SIZE:-512M}"

# Run genimage to create SD card image
rm -rf "${GENIMAGE_TMP}"

genimage \
    --rootpath "${TARGET_DIR}" \
    --tmppath "${GENIMAGE_TMP}" \
    --inputpath "${BINARIES_DIR}" \
    --outputpath "${BINARIES_DIR}" \
    --config "${GENIMAGE_CFG}"

echo "=== SD Card Image Generated ==="

# Generate Mender artifact if mender-artifact tool is available
if command -v mender-artifact >/dev/null 2>&1; then
    echo "Generating Mender-compatible OTA artifact..."

    ARTIFACT_NAME="${BR2_PACKAGE_OTAPULSE_DEVICE_TYPE:-generic}-$(date +%Y%m%d%H%M%S)"
    ROOTFS_IMAGE="${BINARIES_DIR}/rootfs.ext4"

    if [ -f "${ROOTFS_IMAGE}" ]; then
        # Check for signing key
        SIGNING_KEY="${BR2_PACKAGE_OTAPULSE_SIGNING_KEY:-}"

        if [ -n "${SIGNING_KEY}" ] && [ -f "${SIGNING_KEY}" ]; then
            echo "Creating signed artifact..."
            mender-artifact write rootfs-image \
                --device-type "${BR2_PACKAGE_OTAPULSE_DEVICE_TYPE:-generic}" \
                --artifact-name "${ARTIFACT_NAME}" \
                --file "${ROOTFS_IMAGE}" \
                --key "${SIGNING_KEY}" \
                --compression gzip \
                --output-path "${BINARIES_DIR}/${ARTIFACT_NAME}.mender"
        else
            echo "WARNING: No signing key configured. Creating unsigned artifact for testing only."
            mender-artifact write rootfs-image \
                --device-type "${BR2_PACKAGE_OTAPULSE_DEVICE_TYPE:-generic}" \
                --artifact-name "${ARTIFACT_NAME}" \
                --file "${ROOTFS_IMAGE}" \
                --compression gzip \
                --output-path "${BINARIES_DIR}/${ARTIFACT_NAME}-unsigned.mender"
        fi

        echo "Mender artifact created: ${ARTIFACT_NAME}.mender"
    else
        echo "WARNING: rootfs.ext4 not found, skipping artifact generation"
    fi
else
    echo "INFO: mender-artifact tool not found, skipping artifact generation"
    echo "      Install with: go install github.com/mendersoftware/mender-artifact@latest"
fi

echo "=== OTA-Pulse Post-Image Complete ==="
echo ""
echo "Output files in ${BINARIES_DIR}:"
ls -la "${BINARIES_DIR}"/*.img "${BINARIES_DIR}"/*.mender 2>/dev/null || true
