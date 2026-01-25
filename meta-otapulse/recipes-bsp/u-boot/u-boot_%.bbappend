# U-Boot Configuration Append for SoC Monitoring
# Enables A/B partition support, secure boot, and OTA updates

FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

SRC_URI += " \
    file://ab-update.cfg \
    file://fw_env.config \
    "

# Enable U-Boot tools in target image
RDEPENDS:${PN}:append = " u-boot-fw-utils"

# U-Boot environment configuration
do_install:append() {
    # Install fw_env.config for u-boot-fw-utils
    install -d ${D}${sysconfdir}
    install -m 0644 ${WORKDIR}/fw_env.config ${D}${sysconfdir}/fw_env.config
}

FILES:${PN} += "${sysconfdir}/fw_env.config"

# Environment variables for A/B boot selection
UBOOT_ENV_VARS = " \
    boot_slot=a \
    boot_count=0 \
    boot_limit=3 \
    upgrade_available=0 \
    "
