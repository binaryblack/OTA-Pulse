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
    file://otapulse-bootcount-net.cmd.inc \
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

    # TODO-049/S55: splice the shared generic pre-kernel boot-count
    # self-revert fragment (otapulse-bootcount-net.cmd.inc) into any
    # boot-${MACHINE}.cmd that contains the literal marker line
    # "@@OTAPULSE_BOOTCOUNT_NET@@". Boards with no marker pass through
    # byte-identical (just copied to boot.expanded.cmd) -- do_compile always
    # mkimages boot.expanded.cmd, never the raw selected file, so this is the
    # single place either path is decided.
    MARKER = "@@OTAPULSE_BOOTCOUNT_NET@@"
    src_path = os.path.join(workdir, selected)
    with open(src_path) as f:
        lines = f.readlines()

    marker_idx = next((i for i, l in enumerate(lines) if MARKER in l), None)
    expanded_path = os.path.join(workdir, 'boot.expanded.cmd')

    if marker_idx is None:
        with open(expanded_path, 'w') as f:
            f.writelines(lines)
        bb.note(f"OTAPulse: {selected} has no {MARKER} marker -- boot.expanded.cmd is a verbatim copy")
        return

    # Contract check 1: the marker must come after bootpart has already
    # been resolved by the including script -- otherwise the fragment's
    # revert logic has nothing valid to overwrite.
    before_marker = "".join(lines[:marker_idx])
    if "bootpart" not in before_marker:
        bb.fatal(
            f"OTAPulse: {selected} has {MARKER} before any 'bootpart' "
            f"reference -- the board script must resolve bootpart (2/3) "
            f"BEFORE including the bootcount-net fragment."
        )

    # Contract check 2: something after the marker (the bootargs line) must
    # consume the fragment's recoveryargs variable reference, or the
    # panic=15/init= safety net is silently dropped from the kernel command
    # line.
    #
    # RECOVERYARGS_REF is built via string concatenation rather than typed
    # directly as a dollar-brace reference in this python source: bitbake's
    # own metadata-expansion pass scans the ENTIRE raw text of a task
    # function -- comments included, since expansion is plain text
    # substitution with no notion of a Python comment -- looking for that
    # exact two-character opener followed by a name and a closing brace, and
    # would try to treat "recoveryargs" as a bitbake datastore variable
    # (which it is not; it is only ever a U-Boot environment variable
    # inside the compiled .cmd/.scr file). Concatenation keeps the dollar
    # sign and the opening brace in separate string literals so that
    # two-character opener never appears contiguously anywhere in this
    # function's raw source text, sidestepping the ambiguity entirely
    # rather than relying on how any particular bitbake version happens to
    # handle an unresolvable reference.
    after_marker = "".join(lines[marker_idx + 1:])
    RECOVERYARGS_REF = "$" + "{recoveryargs}"
    if RECOVERYARGS_REF not in after_marker:
        bb.fatal(
            "OTAPulse: " + selected + " includes " + MARKER + " but no later "
            "line appends \"" + RECOVERYARGS_REF + "\" to bootargs -- the "
            "fragment's pending-boot panic=15/init= safety net would never "
            "reach the kernel command line."
        )

    inc_path = os.path.join(workdir, 'otapulse-bootcount-net.cmd.inc')
    with open(inc_path) as f:
        fragment_lines = f.readlines()

    expanded = lines[:marker_idx] + fragment_lines + lines[marker_idx + 1:]
    with open(expanded_path, 'w') as f:
        f.writelines(expanded)
    bb.note(f"OTAPulse: spliced {MARKER} in {selected} -- boot.expanded.cmd written ({len(fragment_lines)} fragment lines)")
}

addtask configure before do_compile after do_unpack

# Compile boot script
#
# TODO-049/S55: always mkimage boot.expanded.cmd (written by do_configure's
# Python step, either a verbatim copy of the selected boot-*.cmd or that
# file with the bootcount-net fragment spliced in at its
# @@OTAPULSE_BOOTCOUNT_NET@@ marker) -- never the raw selected file
# directly, so there is exactly one path regardless of whether a given
# board opted into the marker.
do_compile() {
    if [ ! -f "${WORKDIR}/boot.expanded.cmd" ]; then
        bbfatal "OTAPulse: boot.expanded.cmd missing -- do_configure did not run or failed silently"
    fi

    bbnote "OTAPulse: Compiling boot.expanded.cmd for ${UBOOT_MKIMAGE_ARCH}"

    mkimage -A ${UBOOT_MKIMAGE_ARCH} -O linux -T script -C none \
        -n "OTAPulse Boot Script" \
        -d ${WORKDIR}/boot.expanded.cmd ${WORKDIR}/boot.scr
}

# No runtime installation needed - boot.scr goes to boot partition via deploy
# Boot slot syncing is handled by soc-ota-agent Go code, not shell script
do_install() {
    :
}

# Deploy boot.scr to image directory for WKS.
# Also deploy the expanded plaintext source (boot.cmd) alongside it -- not
# consumed by anything at boot time, purely so a `bitbake -c deploy` diff
# against a prior build shows the EXACT text a marker-splice produced,
# without needing to disassemble boot.scr's mkimage wrapper to check.
do_deploy() {
    install -d ${DEPLOYDIR}
    install -m 0644 ${WORKDIR}/boot.scr ${DEPLOYDIR}/boot.scr
    install -m 0644 ${WORKDIR}/boot.expanded.cmd ${DEPLOYDIR}/boot.cmd
}

addtask deploy after do_compile before do_build

# No runtime files - this is a deploy-only recipe
FILES:${PN} = ""
ALLOW_EMPTY:${PN} = "1"

# Machine-specific package
PACKAGE_ARCH = "${MACHINE_ARCH}"

# Compatibility note
COMPATIBLE_HOST = "(arm|aarch64).*-linux"
