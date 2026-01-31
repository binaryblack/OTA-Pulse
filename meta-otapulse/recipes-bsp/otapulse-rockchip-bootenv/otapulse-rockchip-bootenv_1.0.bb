SUMMARY = "OTA-Pulse Rockchip Boot Environment Integration"
DESCRIPTION = "State scripts for A/B boot switching on Rockchip platforms. \
Uses PARTUUID swapping to work with Rockchip's Android bootimg format boot partition."
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/MIT;md5=0835ade698e0bcf8506ecda2f7b4f302"

SRC_URI = " \
    file://ArtifactCommit_Enter_00_switch-boot-slot \
"

# Depends on the boot switching infrastructure
RDEPENDS:${PN} = "u-boot-env-config gptfdisk"

do_install() {
    # Install state scripts to OTA-Pulse scripts directory
    # Mender/OTA-Pulse looks for state scripts in these locations
    install -d ${D}${sysconfdir}/otapulse/scripts
    install -d ${D}${localstatedir}/lib/otapulse/scripts

    # Install the commit state script
    install -m 0755 ${WORKDIR}/ArtifactCommit_Enter_00_switch-boot-slot \
        ${D}${sysconfdir}/otapulse/scripts/

    # Create symlink in var/lib location (some agent versions look here)
    ln -sf ${sysconfdir}/otapulse/scripts/ArtifactCommit_Enter_00_switch-boot-slot \
        ${D}${localstatedir}/lib/otapulse/scripts/ArtifactCommit_Enter_00_switch-boot-slot
}

FILES:${PN} = " \
    ${sysconfdir}/otapulse/scripts/* \
    ${localstatedir}/lib/otapulse/scripts/* \
"
