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
    file://otapulse-auto-provision \
    file://otapulse-auto-provision.service \
    file://otapulse-machine-id \
    file://otapulse-machine-id.service \
    file://switch-boot-slot.sh \
    file://99-otapulse-bsp-aliases.rules \
"

S = "${WORKDIR}"

# Runtime dependencies
RDEPENDS:${PN} = " \
    bash \
    gptfdisk \
    e2fsprogs \
    util-linux \
    curl \
    python3-core \
"

inherit systemd

SYSTEMD_SERVICE:${PN} = "otapulse-partition-setup.service otapulse-auto-provision.service otapulse-machine-id.service"
SYSTEMD_AUTO_ENABLE = "enable"

do_install() {
    # Install partition setup script
    install -d ${D}${bindir}
    install -m 0755 ${WORKDIR}/otapulse-partition-setup ${D}${bindir}/otapulse-partition-setup

    # Install auto-provisioning script
    install -m 0755 ${WORKDIR}/otapulse-auto-provision ${D}${bindir}/otapulse-auto-provision

    # Install boot slot switching script
    install -m 0755 ${WORKDIR}/switch-boot-slot.sh ${D}${bindir}/switch-boot-slot.sh

    # Install persistent machine-id script
    install -m 0755 ${WORKDIR}/otapulse-machine-id ${D}${bindir}/otapulse-machine-id

    # Install systemd services
    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${WORKDIR}/otapulse-partition-setup.service ${D}${systemd_system_unitdir}/
    install -m 0644 ${WORKDIR}/otapulse-auto-provision.service ${D}${systemd_system_unitdir}/
    install -m 0644 ${WORKDIR}/otapulse-machine-id.service ${D}${systemd_system_unitdir}/

    # Install udev rules — alias BSP-specific partition labels (e.g. Tegra
    # APP/APP_b) to OTA-Pulse standard names. No-op on BSPs that don't use
    # those labels.
    install -d ${D}${nonarch_base_libdir}/udev/rules.d
    install -m 0644 ${WORKDIR}/99-otapulse-bsp-aliases.rules \
        ${D}${nonarch_base_libdir}/udev/rules.d/99-otapulse-bsp-aliases.rules

    # Create marker directories
    install -d ${D}${localstatedir}/lib/otapulse
}

FILES:${PN} = " \
    ${bindir}/otapulse-partition-setup \
    ${bindir}/otapulse-auto-provision \
    ${bindir}/otapulse-machine-id \
    ${bindir}/switch-boot-slot.sh \
    ${systemd_system_unitdir}/otapulse-partition-setup.service \
    ${systemd_system_unitdir}/otapulse-auto-provision.service \
    ${systemd_system_unitdir}/otapulse-machine-id.service \
    ${nonarch_base_libdir}/udev/rules.d/99-otapulse-bsp-aliases.rules \
    ${localstatedir}/lib/otapulse \
"
