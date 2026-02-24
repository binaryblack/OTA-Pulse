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

# Read Buildroot config variables from .config (BR2_PACKAGE_* are not exported
# to the shell environment by Buildroot — parse BR2_CONFIG instead).
br2_config_get() {
    sed -n "s/^${1}=\"\(.*\)\"$/\1/p" "${BR2_CONFIG:-/dev/null}"
}

GENIMAGE_CFG_OVERRIDE="$(br2_config_get BR2_PACKAGE_OTAPULSE_GENIMAGE_CFG)"
if [ -n "${GENIMAGE_CFG_OVERRIDE}" ]; then
    GENIMAGE_CFG="${GENIMAGE_CFG_OVERRIDE}"
else
    # Auto-detect QEMU target from device type
    DEVICE_TYPE_AUTO="$(br2_config_get BR2_PACKAGE_OTAPULSE_DEVICE_TYPE)"
    if echo "${DEVICE_TYPE_AUTO}" | grep -qi "qemu"; then
        QEMU_CFG="${BOARD_DIR}/genimage-qemu-aarch64.cfg"
        if [ -f "${QEMU_CFG}" ]; then
            GENIMAGE_CFG="${QEMU_CFG}"
            echo "Auto-selected QEMU genimage config: ${GENIMAGE_CFG}"
        fi
    fi
fi

# Export partition sizes for genimage
export OTAPULSE_ROOTFS_SIZE="$(br2_config_get BR2_PACKAGE_OTAPULSE_ROOTFS_SIZE)"
OTAPULSE_ROOTFS_SIZE="${OTAPULSE_ROOTFS_SIZE:-1G}"
export OTAPULSE_DATA_SIZE="$(br2_config_get BR2_PACKAGE_OTAPULSE_DATA_SIZE)"
OTAPULSE_DATA_SIZE="${OTAPULSE_DATA_SIZE:-512M}"

# Run genimage to create SD card image
rm -rf "${GENIMAGE_TMP}"

genimage \
    --rootpath "${TARGET_DIR}" \
    --tmppath "${GENIMAGE_TMP}" \
    --inputpath "${BINARIES_DIR}" \
    --outputpath "${BINARIES_DIR}" \
    --config "${GENIMAGE_CFG}"

echo "=== SD Card Image Generated ==="

# Format the data partition and pre-populate OTA state files
# This applies to images with a pre-allocated data partition (e.g., QEMU static layout)
DISK_IMAGE=""
if [ -f "${BINARIES_DIR}/sdcard-ab-qemu.img" ]; then
    DISK_IMAGE="${BINARIES_DIR}/sdcard-ab-qemu.img"
    DATA_PART_NUM=4
elif [ -f "${BINARIES_DIR}/sdcard-ab-static.img" ]; then
    DISK_IMAGE="${BINARIES_DIR}/sdcard-ab-static.img"
    DATA_PART_NUM=4
fi

if [ -n "${DISK_IMAGE}" ]; then
    echo "--- Pre-populating data partition in ${DISK_IMAGE} ---"

    # Extract the data partition offset and size using sfdisk
    DATA_INFO=$(sfdisk -J "${DISK_IMAGE}" 2>/dev/null | \
        python3 -c "import sys,json; parts=json.load(sys.stdin)['partitiontable']['partitions']; \
        p=parts[$((DATA_PART_NUM-1))]; print(p['start'],p['size'])" 2>/dev/null || true)

    if [ -n "${DATA_INFO}" ]; then
        DATA_START_SECT=$(echo "${DATA_INFO}" | awk '{print $1}')
        DATA_SIZE_SECT=$(echo "${DATA_INFO}" | awk '{print $2}')
        DATA_OFFSET=$((DATA_START_SECT * 512))
        DATA_SIZE_BYTES=$((DATA_SIZE_SECT * 512))

        # Create a temporary ext4 image for the data partition
        DATA_TMP="${GENIMAGE_TMP}/data-prepopulate.ext4"
        mkdir -p "${GENIMAGE_TMP}"
        dd if=/dev/zero of="${DATA_TMP}" bs=1 count=0 seek="${DATA_SIZE_BYTES}" 2>/dev/null
        mkfs.ext4 -F -L "data" "${DATA_TMP}" >/dev/null 2>&1

        # Mount and populate OTA state files
        DATA_MNT="${GENIMAGE_TMP}/data-mnt"
        mkdir -p "${DATA_MNT}"
        mount -o loop "${DATA_TMP}" "${DATA_MNT}" 2>/dev/null || \
            fuse2fs -o fakeroot "${DATA_TMP}" "${DATA_MNT}" 2>/dev/null || true

        if mountpoint -q "${DATA_MNT}" 2>/dev/null; then
            mkdir -p "${DATA_MNT}/ota"
            mkdir -p "${DATA_MNT}/etc"
            mkdir -p "${DATA_MNT}/var/lib/otapulse"
            echo "2" > "${DATA_MNT}/ota/mender_boot_part"
            echo "0" > "${DATA_MNT}/ota/upgrade_available"
            echo "0" > "${DATA_MNT}/ota/boot_count"
            echo "a" > "${DATA_MNT}/ota/current_slot"
            umount "${DATA_MNT}"
            echo "  OTA state files written to data partition"
        else
            # Fallback: use debugfs to populate if mount not available (host build env)
            mkdir -p "${GENIMAGE_TMP}/data-tree/ota"
            mkdir -p "${GENIMAGE_TMP}/data-tree/etc"
            mkdir -p "${GENIMAGE_TMP}/data-tree/var/lib/otapulse"
            echo "2" > "${GENIMAGE_TMP}/data-tree/ota/mender_boot_part"
            echo "0" > "${GENIMAGE_TMP}/data-tree/ota/upgrade_available"
            echo "0" > "${GENIMAGE_TMP}/data-tree/ota/boot_count"
            echo "a" > "${GENIMAGE_TMP}/data-tree/ota/current_slot"

            # Use e2tools or debugfs to copy files into the ext4 image
            if command -v e2cp >/dev/null 2>&1; then
                e2mkdir "${DATA_TMP}:/ota" 2>/dev/null || true
                e2mkdir "${DATA_TMP}:/etc" 2>/dev/null || true
                e2mkdir "${DATA_TMP}:/var" 2>/dev/null || true
                e2mkdir "${DATA_TMP}:/var/lib" 2>/dev/null || true
                e2mkdir "${DATA_TMP}:/var/lib/otapulse" 2>/dev/null || true
                e2cp "${GENIMAGE_TMP}/data-tree/ota/mender_boot_part" "${DATA_TMP}:/ota/"
                e2cp "${GENIMAGE_TMP}/data-tree/ota/upgrade_available" "${DATA_TMP}:/ota/"
                e2cp "${GENIMAGE_TMP}/data-tree/ota/boot_count" "${DATA_TMP}:/ota/"
                e2cp "${GENIMAGE_TMP}/data-tree/ota/current_slot" "${DATA_TMP}:/ota/"
                echo "  OTA state files written via e2tools"
            elif command -v debugfs >/dev/null 2>&1; then
                debugfs -w -R "mkdir ota" "${DATA_TMP}" 2>/dev/null || true
                debugfs -w -R "mkdir etc" "${DATA_TMP}" 2>/dev/null || true
                for f in mender_boot_part upgrade_available boot_count current_slot; do
                    debugfs -w -R "write ${GENIMAGE_TMP}/data-tree/ota/${f} ota/${f}" "${DATA_TMP}" 2>/dev/null || true
                done
                echo "  OTA state files written via debugfs"
            else
                echo "  WARNING: Cannot populate data partition (no mount/e2tools/debugfs)"
                echo "  OTA state will be initialized by firstboot script instead"
            fi
        fi

        # Write the populated data partition back into the disk image
        dd if="${DATA_TMP}" of="${DISK_IMAGE}" bs=512 seek="${DATA_START_SECT}" \
            count="${DATA_SIZE_SECT}" conv=notrunc 2>/dev/null
        echo "  Data partition written back to ${DISK_IMAGE}"
        rm -f "${DATA_TMP}"
        rm -rf "${DATA_MNT}" "${GENIMAGE_TMP}/data-tree"
    else
        echo "  WARNING: Could not parse data partition info, skipping pre-population"
        echo "  OTA state will be initialized by firstboot script instead"
    fi
fi

echo "--- Pre-Artifact Validation ---"

# Generate signed Mender OTA artifact (mandatory - hard-fail on any missing dependency)
MENDER_ARTIFACT="${HOST_DIR}/bin/mender-artifact"
DEVICE_TYPE="$(br2_config_get BR2_PACKAGE_OTAPULSE_DEVICE_TYPE)"
ARTIFACT_NAME="${DEVICE_TYPE:-generic}-$(date +%Y%m%d%H%M%S)"
ROOTFS_IMAGE="${BINARIES_DIR}/rootfs.ext4"
SIGNING_KEY="$(br2_config_get BR2_PACKAGE_OTAPULSE_SIGNING_KEY)"

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

# Validate signing key is actually a valid private key
if [ -n "${SIGNING_KEY}" ] && [ -f "${SIGNING_KEY}" ]; then
    if ! openssl rsa -in "${SIGNING_KEY}" -noout 2>/dev/null && \
       ! openssl ec -in "${SIGNING_KEY}" -noout 2>/dev/null; then
        echo "ERROR: ${SIGNING_KEY} is not a valid RSA or ECDSA private key"
        exit 1
    fi
fi

echo "Generating signed Mender OTA artifact..."
"${MENDER_ARTIFACT}" write rootfs-image \
    --device-type "${DEVICE_TYPE:-generic}" \
    --artifact-name "${ARTIFACT_NAME}" \
    --file "${ROOTFS_IMAGE}" \
    --key "${SIGNING_KEY}" \
    --compression gzip \
    --output-path "${BINARIES_DIR}/${ARTIFACT_NAME}.mender"

echo "Mender artifact created: ${ARTIFACT_NAME}.mender"

echo ""
echo "--- Artifact Verification ---"

# Validate artifact structure
if "${MENDER_ARTIFACT}" validate "${BINARIES_DIR}/${ARTIFACT_NAME}.mender" 2>/dev/null; then
    echo "  OK: Artifact structure valid"
else
    echo "  ERROR: Artifact validation FAILED"
    exit 1
fi

# Cross-verify: check signature with public key
VERIFY_KEY="$(br2_config_get BR2_PACKAGE_OTAPULSE_VERIFY_KEY)"
if [ -n "${VERIFY_KEY}" ] && [ -f "${VERIFY_KEY}" ]; then
    if "${MENDER_ARTIFACT}" validate "${BINARIES_DIR}/${ARTIFACT_NAME}.mender" -k "${VERIFY_KEY}" 2>/dev/null; then
        echo "  OK: Signature verified with public key — key pair matches"
    else
        echo "  ERROR: Signature verification FAILED!"
        echo "         Signing key and verification key do NOT match."
        echo "         Devices will REJECT this artifact!"
        exit 1
    fi
fi

echo "=== OTA-Pulse Post-Image Complete ==="
echo ""
echo "Output files in ${BINARIES_DIR}:"
ls -la "${BINARIES_DIR}"/*.img "${BINARIES_DIR}"/*.mender 2>/dev/null || true
