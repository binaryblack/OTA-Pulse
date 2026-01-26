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

# Inherit mender-artifact to generate .mender files
inherit mender-artifact

# Ensure ext4 is generated (required for mender artifact)
IMAGE_FSTYPES:append = " ext4"

# Add OTA agent to the image
IMAGE_INSTALL:append = " soc-ota-agent"

# Add gptfdisk for partition management on target
IMAGE_INSTALL:append = " gptfdisk"

# Add first-boot partition setup for dynamic mode
IMAGE_INSTALL:append = " ${@bb.utils.contains('OTAPULSE_PARTITION_MODE', 'dynamic', 'otapulse-firstboot', '', d)}"

# WKS dependencies for partition tools
WKS_FILE_DEPENDS:append = " gptfdisk-native e2fsprogs-native"

# Default device type to MACHINE if not set
MENDER_DEVICE_TYPE ?= "${MACHINE}"

# Disable SPDX for this image (externalsrc compatibility)
# Users can remove this if they've already disabled SPDX globally
INHERIT:remove = "create-spdx"

python __anonymous() {
    # Warn if OTA_SERVER_URL is not configured
    server_url = d.getVar('OTA_SERVER_URL')
    if not server_url or 'example' in server_url.lower() or 'your-' in server_url.lower():
        bb.warn("OTAPulse: OTA_SERVER_URL not configured! Set it in local.conf:")
        bb.warn("  OTA_SERVER_URL = \"https://your-ota-server.com\"")

    # Show partition mode info
    partition_mode = d.getVar('OTAPULSE_PARTITION_MODE')
    if partition_mode == 'dynamic':
        bb.note("OTAPulse: Using DYNAMIC partitioning (minimal image, A/B created on first boot)")
    else:
        bb.note("OTAPulse: Using STATIC partitioning (pre-allocated A/B partitions)")
}
