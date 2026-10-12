# OTA-Pulse: grow /data to fill the storage medium (bootloader compatibility mode only)
#
# TODO-157 / sprint S109, TASK-S109-009. In bootloader mode the partition table
# comes from the board support's disk image, whose /data is only as large as the
# build declared (OTAPULSE_DATA_PART_SIZE_MB), while the medium it is written to is
# usually much larger. On every boot this extends the /data partition to the end of
# the medium (before the mount) and grows its file system (after the mount), both
# no-ops once done, both before the OTA agent starts so it never downloads an
# artifact into a /data that has not been grown yet. Native images size /data
# themselves (otapulse-partition-setup) and never contain this package.
#
# It is pulled in by soc-ota-agent's bootloader-mode RDEPENDS (customers write
# nothing); opt out with OTAPULSE_GROW_DATA_PART = "0" (see otapulse-full.bbclass).
SUMMARY = "OTA-Pulse: grow the /data partition and file system to the medium"
DESCRIPTION = "Extends the /data partition to the end of the storage medium before the mount and grows its file system after it. Bootloader compatibility mode only."
HOMEPAGE = "https://github.com/binaryblack/OTA-Pulse"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

SRC_URI = " \
    file://otapulse-resize-data-part \
    file://otapulse-resize-data-part.service \
    file://otapulse-growfs-data.service \
"

S = "${WORKDIR}"

# Bootloader mode only. In native mode the recipe is skipped, so it cannot be
# pulled into a native image by accident ("Nothing PROVIDES otapulse-growfs-data"
# names this reason) and it adds nothing to a native build's task graph.
python () {
    if 'otapulse-bootloader' not in (d.getVar('OVERRIDES') or '').split(':'):
        raise bb.parse.SkipRecipe("otapulse-growfs-data is for OTAPULSE_BOOT_MODE = \"bootloader\" only; "
                                  "native images size /data with otapulse-partition-setup")
}

# parted (resizepart, partprobe); fdisk (moves the GPT backup table to the end of
# the medium before parted writes it); blkid (partition table type, tag lookup);
# systemd (systemd-growfs); udev is part of systemd.
RDEPENDS:${PN} = " \
    parted \
    util-linux-fdisk \
    util-linux-blkid \
    systemd \
"

inherit systemd

SYSTEMD_SERVICE:${PN} = "otapulse-resize-data-part.service otapulse-growfs-data.service"
SYSTEMD_AUTO_ENABLE = "enable"

do_install() {
    install -d ${D}${bindir}
    install -m 0755 ${WORKDIR}/otapulse-resize-data-part ${D}${bindir}/otapulse-resize-data-part

    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${WORKDIR}/otapulse-resize-data-part.service ${D}${systemd_system_unitdir}/
    sed -e "s|@SYSTEMD_UNITDIR@|${systemd_unitdir}|g" \
        ${WORKDIR}/otapulse-growfs-data.service \
        > ${D}${systemd_system_unitdir}/otapulse-growfs-data.service
    chmod 0644 ${D}${systemd_system_unitdir}/otapulse-growfs-data.service
    if grep -q '@' ${D}${systemd_system_unitdir}/otapulse-growfs-data.service; then
        bbfatal "otapulse-growfs-data: unsubstituted @...@ placeholder left in otapulse-growfs-data.service"
    fi
}

FILES:${PN} = " \
    ${bindir}/otapulse-resize-data-part \
    ${systemd_system_unitdir}/otapulse-resize-data-part.service \
    ${systemd_system_unitdir}/otapulse-growfs-data.service \
"
