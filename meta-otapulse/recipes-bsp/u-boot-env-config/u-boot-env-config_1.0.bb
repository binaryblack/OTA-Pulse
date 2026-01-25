SUMMARY = "U-Boot Environment Configuration for OTA"
DESCRIPTION = "Generic U-Boot environment configuration and boot slot switching \
scripts for A/B OTA updates. Supports Rockchip, NXP i.MX, TI, Allwinner, and other SoCs."
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/MIT;md5=0835ade698e0bcf8506ecda2f7b4f302"

SRC_URI = " \
    file://fw_env.config \
    file://setup-uboot-env.sh \
    file://switch-boot-slot.sh \
"

# Depend on u-boot-tools for fw_printenv/fw_setenv
RDEPENDS:${PN} = "u-boot-fw-utils"

do_install() {
    # Install fw_env.config template
    install -d ${D}${sysconfdir}
    install -m 0644 ${WORKDIR}/fw_env.config ${D}${sysconfdir}/fw_env.config
    
    # Install setup script
    install -d ${D}${sbindir}
    install -m 0755 ${WORKDIR}/setup-uboot-env.sh ${D}${sbindir}/setup-uboot-env.sh
    install -m 0755 ${WORKDIR}/switch-boot-slot.sh ${D}${sbindir}/switch-boot-slot.sh
}

FILES:${PN} = " \
    ${sysconfdir}/fw_env.config \
    ${sbindir}/setup-uboot-env.sh \
    ${sbindir}/switch-boot-slot.sh \
"
