# OTAPulse First-Boot Partition Setup
# Creates A/B partitions dynamically on first boot
#
# Benefits:
# - Minimal initial image size (only boot + rootfs needed)
# - Works on any storage size (auto-detects and allocates)
# - Platform agnostic - no WKS customization per platform
#
# Usage:
#   IMAGE_INSTALL:append = " otapulse-firstboot"

SUMMARY = "OTAPulse first-boot partition setup for A/B OTA updates"
DESCRIPTION = "Automatically creates rootfs_b and data partitions on first boot, \
enabling A/B OTA updates without pre-allocating partitions in the image. \
This allows minimal image sizes that work on any storage device."
HOMEPAGE = "https://github.com/binaryblack/OTA-Pulse"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

SRC_URI = " \
    file://otapulse-partition-setup \
    file://otapulse-partition-setup.service \
"

S = "${WORKDIR}"

# Runtime dependencies
RDEPENDS:${PN} = " \
    bash \
    gptfdisk \
    e2fsprogs \
    util-linux \
"

inherit systemd

SYSTEMD_SERVICE:${PN} = "otapulse-partition-setup.service"
SYSTEMD_AUTO_ENABLE = "enable"

do_install() {
    # Install partition setup script
    install -d ${D}${bindir}
    install -m 0755 ${WORKDIR}/otapulse-partition-setup ${D}${bindir}/otapulse-partition-setup

    # Install systemd service
    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${WORKDIR}/otapulse-partition-setup.service ${D}${systemd_system_unitdir}/

    # Create marker directory
    install -d ${D}${localstatedir}/lib/otapulse
}

FILES:${PN} = " \
    ${bindir}/otapulse-partition-setup \
    ${systemd_system_unitdir}/otapulse-partition-setup.service \
    ${localstatedir}/lib/otapulse \
"
