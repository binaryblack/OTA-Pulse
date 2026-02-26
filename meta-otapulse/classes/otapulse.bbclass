# OTAPulse Integration Class
# Single-line integration for OTA updates with A/B partitions
#
# Usage in your image recipe:
#   inherit otapulse
#
# Or in local.conf:
#   INHERIT += "otapulse"
#
# This class automatically:
#   - Adds soc-ota-agent to the image
#   - Enables mender artifact generation
#   - Ensures ext4 rootfs is built (required for mender)
#   - Sets up proper dependencies
#
# Configuration (set in local.conf):
#   OTA_SERVER_URL              - Your OTA server URL (required)
#   MENDER_DEVICE_TYPE          - Device type identifier (default: ${MACHINE})
#   SOC_OTA_SIGNATURE_VERIFICATION - Enable signature verification (default: 0)
#   SOC_OTA_SIGNING_KEY         - Private key path for signing artifacts
#
# Provisioning / Credentials:
#   OTAPULSE_PROVISIONING_TOKEN - Auto-provision device on first boot (recommended)
#   OTAPULSE_TENANT_TOKEN       - Tenant auth token (alternative to provisioning token)
#   OTAPULSE_PROVISIONING_MODE  - Set to 'manual' to skip build-time credential checks
#
# Build-time Sanity Checks (via otapulse-sanity):
#   OTAPULSE_SANITY_LEVEL       - 'error' (default, blocks build), 'warn', or 'off'
#   OTAPULSE_SANITY_SKIP        - Space-separated list of check names to skip
#
# Partition Modes:
#   OTAPULSE_PARTITION_MODE = "dynamic" (default)
#     - Minimal image size (~3-4GB)
#     - A/B partitions created on first boot
#     - Works on any storage size
#     - Fast flashing with any tool
#
#   OTAPULSE_PARTITION_MODE = "static"
#     - Pre-allocated A/B partitions in image
#     - Larger image size (depends on SOC_WKS_ROOTFS_SIZE)
#     - No first-boot setup required
#
# For static mode with large images (Qt, multimedia >1GB):
#   SOC_WKS_ROOTFS_SIZE = "6G"
#   SOC_WKS_DATA_SIZE = "2G"

# Default to dynamic partitioning (minimal image size)
OTAPULSE_PARTITION_MODE ?= "dynamic"

# Provisioning mode: 'token' requires a credential at build time; 'manual' skips that check
OTAPULSE_PROVISIONING_MODE ?= "token"

# Run configuration sanity checks before do_rootfs
inherit otapulse-sanity

# Inherit mender-artifact to generate .mender files
inherit mender-artifact

# Ensure ext4 is generated (required for mender artifact)
IMAGE_FSTYPES:append = " ext4"

# Add OTA agent to the image
IMAGE_INSTALL:append = " soc-ota-agent"

# Add gptfdisk for partition management on target
IMAGE_INSTALL:append = " gptfdisk"

# Add first-boot partition setup (handles both dynamic and static modes)
# Dynamic mode: creates A/B partitions on first boot
# Static mode: detects pre-existing partitions, mounts /data, initializes /data/ota/ state files
IMAGE_INSTALL:append = " otapulse-firstboot"

# A/B Boot Script Configuration
# Set OTAPULSE_BOOT_SCRIPT = "1" in local.conf or machine config to enable
# This is platform-specific and requires a matching boot.cmd for your board
# Supported: i.MX8 (NXP), other platforms need custom boot.cmd
OTAPULSE_BOOT_SCRIPT ?= "0"

# Add A/B boot script only when explicitly enabled
IMAGE_INSTALL:append = " ${@bb.utils.contains('OTAPULSE_BOOT_SCRIPT', '1', 'otapulse-boot-script', '', d)}"

# Add boot.scr to boot partition only when boot script is enabled
IMAGE_BOOT_FILES:append = " ${@bb.utils.contains('OTAPULSE_BOOT_SCRIPT', '1', 'boot.scr', '', d)}"

# WKS dependencies for partition tools
WKS_FILE_DEPENDS:append = " gptfdisk-native e2fsprogs-native"

# Default device type to MACHINE if not set
MENDER_DEVICE_TYPE ?= "${MACHINE}"

# Disable SPDX for this image (externalsrc compatibility)
# Users can remove this if they've already disabled SPDX globally
INHERIT:remove = "create-spdx"

python __anonymous() {
    pn             = d.getVar('PN')                     or '(unknown)'
    server_url     = d.getVar('OTA_SERVER_URL')         or '(not set)'
    partition_mode = d.getVar('OTAPULSE_PARTITION_MODE') or 'dynamic'
    sanity_level   = d.getVar('OTAPULSE_SANITY_LEVEL')  or 'error'

    bb.note("OTAPulse enabled for: %s\n"
            "  Server URL:     %s\n"
            "  Partition Mode: %s\n"
            "  Sanity Level:   %s"
            % (pn, server_url, partition_mode, sanity_level))
}
