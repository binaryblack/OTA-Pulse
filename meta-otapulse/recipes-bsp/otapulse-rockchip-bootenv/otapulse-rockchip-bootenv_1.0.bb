SUMMARY = "OTA-Pulse Rockchip Boot Environment Integration (DEPRECATED — inert no-op)"
DESCRIPTION = "Formerly installed an ArtifactCommit_* PARTUUID-swap script for A/B \
boot switching on Rockchip platforms. BUG-232: that script was installed into \
${sysconfdir}/otapulse/scripts (RootfsScriptsPath), a location the agent never \
executes Artifact* transition scripts from (those run from ArtScriptsPath, which \
is populated FROM THE ARTIFACT at install time — see soc-ota-agent's \
installer/installer.go). Worse, having ANY file in RootfsScriptsPath without a \
'version' file trips CheckRootfsScriptsVersion (statescript/executor.go) and fails \
EVERY OTA commit on the board — so this package broke every Rockchip OTA and its \
own slot-switch script never ran. The real, working, hardware-independent \
mechanism is u-boot-env-config's switch-boot-slot.sh, which the agent already \
invokes natively on install (installer/dual_rootfs_device.go switchBootSlot()) and \
on rollback (via soc-ota-agent's ArtifactRollbackReboot_Enter_01 state script). \
This package is kept only so nothing breaks if another recipe still references it \
by name (RRECOMMENDS/RDEPENDS is silently satisfiable); it installs nothing."
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/MIT;md5=0835ade698e0bcf8506ecda2f7b4f302"

# No SRC_URI, no do_install, no FILES — intentionally inert. The former
# ArtifactCommit_Enter_00_switch-boot-slot script is left in files/ for
# historical reference only; it is not referenced by SRC_URI and is never
# installed or packaged.

do_install() {
    :
}

FILES:${PN} = ""
ALLOW_EMPTY:${PN} = "1"
