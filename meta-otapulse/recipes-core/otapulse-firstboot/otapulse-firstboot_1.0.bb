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
    file://otapulse-boot-health \
    file://otapulse-boot-health.service \
    file://otapulse-boot-deadline \
    file://otapulse-boot-deadline.service \
    file://otapulse-boot-deadline.timer \
    file://otapulse-auto-provision \
    file://otapulse-auto-provision.service \
    file://otapulse-machine-id \
    file://otapulse-machine-id.service \
    file://99-otapulse-bsp-aliases.rules \
"

S = "${WORKDIR}"

# Runtime dependencies
RDEPENDS:${PN} = " \
    bash \
    gptfdisk \
    e2fsprogs \
    util-linux \
    util-linux-sfdisk \
    util-linux-blkid \
    curl \
    python3-core \
"

inherit systemd

# otapulse-boot-deadline.timer (BUG-356) is the enabled unit; its .service is
# static, started only by the timer.
SYSTEMD_SERVICE:${PN} = "otapulse-partition-setup.service otapulse-boot-health.service otapulse-boot-deadline.timer otapulse-auto-provision.service otapulse-machine-id.service"
SYSTEMD_AUTO_ENABLE = "enable"

do_install() {
    # Install partition setup script
    install -d ${D}${bindir}
    install -m 0755 ${WORKDIR}/otapulse-partition-setup ${D}${bindir}/otapulse-partition-setup

    # Install pre-agent boot-health script (direct-boot brick prevention)
    install -m 0755 ${WORKDIR}/otapulse-boot-health ${D}${bindir}/otapulse-boot-health

    # BUG-356: pending-upgrade boot deadline (armed by otapulse-boot-health)
    install -m 0755 ${WORKDIR}/otapulse-boot-deadline ${D}${bindir}/otapulse-boot-deadline

    # Install auto-provisioning script
    install -m 0755 ${WORKDIR}/otapulse-auto-provision ${D}${bindir}/otapulse-auto-provision

    # Install persistent machine-id script
    install -m 0755 ${WORKDIR}/otapulse-machine-id ${D}${bindir}/otapulse-machine-id

    # Install systemd services
    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${WORKDIR}/otapulse-partition-setup.service ${D}${systemd_system_unitdir}/
    install -m 0644 ${WORKDIR}/otapulse-boot-health.service ${D}${systemd_system_unitdir}/
    install -m 0644 ${WORKDIR}/otapulse-boot-deadline.service ${D}${systemd_system_unitdir}/
    install -m 0644 ${WORKDIR}/otapulse-boot-deadline.timer ${D}${systemd_system_unitdir}/
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
    ${bindir}/otapulse-boot-health \
    ${bindir}/otapulse-boot-deadline \
    ${bindir}/otapulse-auto-provision \
    ${bindir}/otapulse-machine-id \
    ${systemd_system_unitdir}/otapulse-partition-setup.service \
    ${systemd_system_unitdir}/otapulse-boot-health.service \
    ${systemd_system_unitdir}/otapulse-boot-deadline.service \
    ${systemd_system_unitdir}/otapulse-boot-deadline.timer \
    ${systemd_system_unitdir}/otapulse-auto-provision.service \
    ${systemd_system_unitdir}/otapulse-machine-id.service \
    ${nonarch_base_libdir}/udev/rules.d/99-otapulse-bsp-aliases.rules \
    ${localstatedir}/lib/otapulse \
"

# ── Bootloader compat mode (TODO-157 / sprint S109, TASK-S109-009) ───────────
# In bootloader mode (override "otapulse-bootloader", set by otapulse-full.bbclass)
# the board support owns the partition layout, the /data mount and the rollback
# net, so this package is SPLIT: the native-only parts are not installed, the
# parts the device cannot do without stay, and one bootloader-mode unit is added.
#
#   NOT installed (native only):
#     otapulse-partition-setup(.service)  GPT/partition-label detection and
#                                         (re)partitioning; it would mis-detect a
#                                         Mender-style MBR/GPT layout, and its
#                                         file-based boot environment init
#                                         (/data/ota/*) is meaningless there.
#     otapulse-boot-health(.service)      native direct-boot brick prevention
#                                         (FAT boot_count / extlinux / boot.scr
#                                         revert detection); the bootloader
#                                         environment's own bootlimit/altbootcmd
#                                         owns that here.
#     otapulse-boot-deadline(.service/.timer)
#                                         armed by boot-health; the userspace
#                                         deadline of bootloader mode is a
#                                         separate unit (TASK-S109-011: ADD ITS
#                                         FILES TO THE LISTS BELOW, do not
#                                         re-enable the native ones).
#     99-otapulse-bsp-aliases.rules       aliases Tegra/MBR labels into the
#                                         native by-partlabel names.
#   KEPT unchanged (layout independent):
#     otapulse-auto-provision(.service)   credentials, tunnel credential
#     otapulse-machine-id(.service)       /data/etc/machine-id (+ NIC MAC) restore
#     /var/lib/otapulse
#   ADDED:
#     otapulse-data-seed(.service)        /data/otapulse + device_type seeding
#                                         and the device-type consistency check.
#
# Mechanism: files are installed by the unchanged native do_install above and
# removed again by do_install:append:otapulse-bootloader below. That keeps the
# native task bodies, and so every native task signature, exactly as before
# (native-unchanged gate); nothing here is evaluated in native mode.
OTAPULSE_BL_NATIVE_ONLY_BINS = "otapulse-partition-setup otapulse-boot-health otapulse-boot-deadline"
OTAPULSE_BL_NATIVE_ONLY_UNITS = "otapulse-partition-setup.service otapulse-boot-health.service otapulse-boot-deadline.service otapulse-boot-deadline.timer"

SRC_URI:append:otapulse-bootloader = " \
    file://otapulse-data-seed \
    file://otapulse-data-seed.service \
    file://10-otapulse-provision-data.conf \
"

# Where the provisioning token comes from (customer variable, see
# otapulse-full.bbclass): "baked" (default) writes /etc/otapulse/provision.env into
# the image (done by the image class, never by a package - a package would put the
# secret into the package feed and the shared package cache); "manual" ships no
# token and this package adds a drop-in that reads /data/otapulse/provision.env.
OTAPULSE_PROVISIONING_MODE ?= "baked"

# Partitioning tools only otapulse-partition-setup needs. (e2fsprogs stays: it
# provides the fsck systemd runs on /data and the root filesystem.)
RDEPENDS:${PN}:remove:otapulse-bootloader = "gptfdisk util-linux-sfdisk"

# otapulse-auto-provision parses the server's JSON replies and the JWT with python3
# "json" and "base64". RDEPENDS above lists only python3-core, which has NEITHER
# (found by booting a minimal bootloader-mode image: "No module named 'json'", so
# ServerURL read back empty and the device never registered). The native production
# image hides this by installing the full python3 package. Added here for bootloader
# mode only so native stays byte-identical; the same two lines belong in the native
# RDEPENDS (follow-up: it changes the native package metadata, not the rootfs).
RDEPENDS:${PN}:append:otapulse-bootloader = " python3-json python3-netclient"

SYSTEMD_SERVICE:${PN}:remove:otapulse-bootloader = "otapulse-partition-setup.service otapulse-boot-health.service otapulse-boot-deadline.timer"
SYSTEMD_SERVICE:${PN}:append:otapulse-bootloader = " otapulse-data-seed.service"

FILES:${PN}:remove:otapulse-bootloader = " \
    ${bindir}/otapulse-partition-setup \
    ${bindir}/otapulse-boot-health \
    ${bindir}/otapulse-boot-deadline \
    ${systemd_system_unitdir}/otapulse-partition-setup.service \
    ${systemd_system_unitdir}/otapulse-boot-health.service \
    ${systemd_system_unitdir}/otapulse-boot-deadline.service \
    ${systemd_system_unitdir}/otapulse-boot-deadline.timer \
    ${nonarch_base_libdir}/udev/rules.d/99-otapulse-bsp-aliases.rules \
"
FILES:${PN}:append:otapulse-bootloader = " \
    ${bindir}/otapulse-data-seed \
    ${systemd_system_unitdir}/otapulse-data-seed.service \
    ${systemd_system_unitdir}/otapulse-auto-provision.service.d \
"

do_install:append:otapulse-bootloader() {
    for f in ${OTAPULSE_BL_NATIVE_ONLY_BINS}; do
        rm -f ${D}${bindir}/$f
    done
    for f in ${OTAPULSE_BL_NATIVE_ONLY_UNITS}; do
        rm -f ${D}${systemd_system_unitdir}/$f
    done
    rm -f ${D}${nonarch_base_libdir}/udev/rules.d/99-otapulse-bsp-aliases.rules
    rmdir ${D}${nonarch_base_libdir}/udev/rules.d ${D}${nonarch_base_libdir}/udev 2>/dev/null || true

    install -m 0755 ${WORKDIR}/otapulse-data-seed ${D}${bindir}/otapulse-data-seed
    install -m 0644 ${WORKDIR}/otapulse-data-seed.service ${D}${systemd_system_unitdir}/otapulse-data-seed.service

    # OTAPULSE_PROVISIONING_MODE = "manual": the token is read from /data (see the drop-in).
    if [ "${OTAPULSE_PROVISIONING_MODE}" = "manual" ]; then
        install -d ${D}${systemd_system_unitdir}/otapulse-auto-provision.service.d
        install -m 0644 ${WORKDIR}/10-otapulse-provision-data.conf \
            ${D}${systemd_system_unitdir}/otapulse-auto-provision.service.d/10-otapulse-provision-data.conf
    fi
}
