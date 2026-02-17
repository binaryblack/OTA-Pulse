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

# Generate signed Mender OTA artifact (mandatory - hard-fail on any missing dependency)
MENDER_ARTIFACT="${HOST_DIR}/bin/mender-artifact"
ARTIFACT_NAME="${BR2_PACKAGE_OTAPULSE_DEVICE_TYPE:-generic}-$(date +%Y%m%d%H%M%S)"
ROOTFS_IMAGE="${BINARIES_DIR}/rootfs.ext4"
SIGNING_KEY="${BR2_PACKAGE_OTAPULSE_SIGNING_KEY:-}"

if [ ! -x "${MENDER_ARTIFACT}" ]; then
    echo "ERROR: mender-artifact not found at ${MENDER_ARTIFACT}"
    echo "  Enable BR2_PACKAGE_HOST_MENDER_ARTIFACT=y in your defconfig."
    exit 1
fi

if [ ! -f "${ROOTFS_IMAGE}" ]; then
    echo "ERROR: rootfs.ext4 not found at ${ROOTFS_IMAGE}"
    echo "  Ensure BR2_TARGET_ROOTFS_EXT2=y and BR2_TARGET_ROOTFS_EXT2_4=y in your defconfig."
    exit 1
fi

if [ -z "${SIGNING_KEY}" ] || [ ! -f "${SIGNING_KEY}" ]; then
    echo "ERROR: Artifact signing key not found: '${SIGNING_KEY}'"
    echo "  Set BR2_PACKAGE_OTAPULSE_SIGNING_KEY to your private key path."
    echo "  Generate a key pair: openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:4096 \\"
    echo "    -out keys/otapulse-private.pem && openssl rsa -pubout \\"
    echo "    -in keys/otapulse-private.pem -out keys/otapulse-public.pem"
    exit 1
fi

echo "Generating signed Mender OTA artifact..."
"${MENDER_ARTIFACT}" write rootfs-image \
    --device-type "${BR2_PACKAGE_OTAPULSE_DEVICE_TYPE:-generic}" \
    --artifact-name "${ARTIFACT_NAME}" \
    --file "${ROOTFS_IMAGE}" \
    --key "${SIGNING_KEY}" \
    --compression gzip \
    --output-path "${BINARIES_DIR}/${ARTIFACT_NAME}.mender"

echo "Mender artifact created: ${ARTIFACT_NAME}.mender"

echo "=== OTA-Pulse Post-Image Complete ==="
echo ""
echo "Output files in ${BINARIES_DIR}:"
ls -la "${BINARIES_DIR}"/*.img "${BINARIES_DIR}"/*.mender 2>/dev/null || true
