# OTAPulse Large Image Integration Class
# For Qt, multimedia, and other large images (>1GB rootfs)
# Requires 16GB+ eMMC/SD card
#
# Usage in your image recipe:
#   inherit otapulse-large
#
# This class:
#   - Includes everything from otapulse.bbclass
#   - Sets partition sizes for 6GB rootfs (A/B = 12GB total)
#   - Uses the large WKS file template

# Inherit base otapulse functionality
inherit otapulse

# Override partition sizes for large images
SOC_WKS_BOOT_SIZE = "256M"
SOC_WKS_ROOTFS_SIZE = "6G"
SOC_WKS_DATA_SIZE = "2G"

# Use the large WKS template from meta-otapulse
# Note: If you have a custom WKS, set WKS_FILE in your recipe/local.conf
WKS_FILE ?= "soc-monitoring-large.wks"

python __anonymous() {
    bb.note("OTAPulse Large Image: Configured for 6GB rootfs with A/B partitions")
    bb.note("  Minimum storage required: 16GB")
    bb.note("  Partition layout: boot(256M) + rootfs-a(6G) + rootfs-b(6G) + data(2G)")
}
