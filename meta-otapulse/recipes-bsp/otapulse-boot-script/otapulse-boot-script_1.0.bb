# OTAPulse A/B Boot Script
# Compiles U-Boot boot script for A/B partition switching
#
# Machine-specific boot scripts:
#   - boot-${MACHINE}.cmd   (highest priority)
#   - boot-imx8mp.cmd       (for i.MX8MP boards)
#   - boot-generic.cmd      (fallback)
#
# To add support for your platform:
#   1. Create files/boot-${MACHINE}.cmd with platform-specific settings
#   2. Set OTAPULSE_BOOT_SCRIPT = "1" in local.conf

SUMMARY = "OTAPulse A/B boot script"
DESCRIPTION = "U-Boot boot script that reads boot slot preference and boots appropriate rootfs partition"
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/MIT;md5=0835ade698e0bcf8506ecda2f7b4f302"

DEPENDS = "u-boot-mkimage-native"

# Allow machine-specific boot scripts with fallback to generic
# Priority: boot-${MACHINE}.cmd > boot-imx8mp.cmd (for imx8 machines) > boot-generic.cmd
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

SRC_URI = " \
    file://boot-generic.cmd \
    file://boot-imx8mp.cmd \
"

S = "${WORKDIR}"

inherit deploy

# Determine architecture for mkimage
# arm64/aarch64 -> arm64, arm -> arm
UBOOT_MKIMAGE_ARCH = "${@'arm64' if d.getVar('TARGET_ARCH') in ['aarch64', 'arm64'] else 'arm'}"

# Select boot script based on machine
# Users can override by setting OTAPULSE_BOOT_CMD in local.conf
OTAPULSE_BOOT_CMD ?= ""

python do_configure() {
    import os
    workdir = d.getVar('WORKDIR')
    machine = d.getVar('MACHINE')

    # Priority order for boot script selection
    boot_cmd_override = d.getVar('OTAPULSE_BOOT_CMD')
    candidates = []

    if boot_cmd_override:
        candidates.append(boot_cmd_override)

    candidates.extend([
        f'boot-{machine}.cmd',
        'boot-imx8mp.cmd' if 'imx8' in machine.lower() else None,
        'boot-generic.cmd'
    ])

    # Find first existing boot script
    selected = None
    for candidate in candidates:
        if candidate and os.path.exists(os.path.join(workdir, candidate)):
            selected = candidate
            break

    if not selected:
        bb.fatal("No boot script found! Create files/boot-${MACHINE}.cmd or use boot-generic.cmd")

    # Write selection to file for shell task to read
    # This ensures reliable variable passing between Python and shell tasks
    marker_file = os.path.join(workdir, '.boot_cmd_selected')
    with open(marker_file, 'w') as f:
        f.write(selected)

    bb.note(f"OTAPulse: Using boot script: {selected}")
}

addtask configure before do_compile after do_unpack

# Compile boot script
do_compile() {
    # Read boot script selection from marker file (written by do_configure)
    if [ -f "${WORKDIR}/.boot_cmd_selected" ]; then
        BOOT_CMD=$(cat "${WORKDIR}/.boot_cmd_selected")
    else
        # Fallback detection in shell if marker file missing
        if [ -f "${WORKDIR}/boot-${MACHINE}.cmd" ]; then
            BOOT_CMD="boot-${MACHINE}.cmd"
        elif echo "${MACHINE}" | grep -qi "imx8" && [ -f "${WORKDIR}/boot-imx8mp.cmd" ]; then
            BOOT_CMD="boot-imx8mp.cmd"
        else
            BOOT_CMD="boot-generic.cmd"
        fi
    fi

    bbnote "OTAPulse: Compiling $BOOT_CMD for ${UBOOT_MKIMAGE_ARCH}"

    mkimage -A ${UBOOT_MKIMAGE_ARCH} -O linux -T script -C none \
        -n "OTAPulse Boot Script" \
        -d ${WORKDIR}/${BOOT_CMD} ${WORKDIR}/boot.scr
}

# No runtime installation needed - boot.scr goes to boot partition via deploy
# Boot slot syncing is handled by soc-ota-agent Go code, not shell script
do_install() {
    :
}

# Deploy boot.scr to image directory for WKS
do_deploy() {
    install -d ${DEPLOYDIR}
    install -m 0644 ${WORKDIR}/boot.scr ${DEPLOYDIR}/boot.scr
}

addtask deploy after do_compile before do_build

# No runtime files - this is a deploy-only recipe
FILES:${PN} = ""
ALLOW_EMPTY:${PN} = "1"

# Machine-specific package
PACKAGE_ARCH = "${MACHINE_ARCH}"

# Compatibility note
COMPATIBLE_HOST = "(arm|aarch64).*-linux"
