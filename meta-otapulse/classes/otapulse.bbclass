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
# For large images (Qt, multimedia >1GB), also set:
#   SOC_WKS_ROOTFS_SIZE = "6G"
#   SOC_WKS_DATA_SIZE = "2G"

# Inherit mender-artifact to generate .mender files
inherit mender-artifact

# Ensure ext4 is generated (required for mender artifact)
IMAGE_FSTYPES:append = " ext4"

# Add OTA agent to the image
IMAGE_INSTALL:append = " soc-ota-agent"

# Add gptfdisk for partition management on target
IMAGE_INSTALL:append = " gptfdisk"

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
}
