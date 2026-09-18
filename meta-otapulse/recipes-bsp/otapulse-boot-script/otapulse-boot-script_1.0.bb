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
    #
    # NOTE on the "$" + "{recoveryargs}" concatenation below (RECOVERYARGS_REF
    # and elsewhere in this function): this is NOT required for correctness.
    # Fable's TASK-S55-002 review (2026-09-12) confirmed bitbake's own
    # variable-expansion pass never runs over a python task's raw source --
    # unlike shell tasks, python function bodies are read via getVar with
    # expansion disabled (proof already in this very file: the pre-existing
    # bb.fatal a few lines above has always contained a literal
    # boot-dollar-brace-MACHINE-dollar-brace reference with no issue). The
    # concatenation is kept purely so this string never gets misread by a
    # future skim of this file as an actual U-Boot variable reference this
    # function is supposed to act on -- a readability choice, not a
    # correctness requirement.
    MARKER = "@@OTAPULSE_BOOTCOUNT_NET@@"
    RECOVERYARGS_REF = "$" + "{recoveryargs}"
    src_path = os.path.join(workdir, selected)
    with open(src_path) as f:
        lines = f.readlines()

    def is_code(line):
        # Whole-line hush comments only (this project's boot scripts never
        # use trailing inline "#" comments after a real command) -- good
        # enough to tell "a real assignment" from "bootpart mentioned only
        # in a comment", which is exactly the false-pass Fable's review
        # found against the naive substring check this replaces.
        return not line.strip().startswith('#')

    # Marker detection must also ignore comment lines -- a board script is
    # allowed to explain in a comment that it deliberately does NOT carry
    # the marker (e.g. boot-rockchip-rk3588-evb.cmd), and that explanatory
    # mention must not itself be treated as an active marker.
    marker_idx = next((i for i, l in enumerate(lines) if MARKER in l and is_code(l)), None)
    expanded_path = os.path.join(workdir, 'boot.expanded.cmd')

    if marker_idx is None:
        with open(expanded_path, 'w') as f:
            f.writelines(lines)
        bb.note(f"OTAPulse: {selected} has no {MARKER} marker -- boot.expanded.cmd is a verbatim copy")
        return

    def assigns(line, varname):
        # Matches both "setenv VAR ..." and the "test -n ... || setenv VAR
        # ..." idiom boot-generic.cmd uses, plus setexpr.b VAR (used for
        # bootpart itself). Deliberately does NOT match a bare "${VAR}"
        # read-reference -- only an actual write.
        import re
        return re.search(r'\b(setenv|setexpr\.b)\s+' + re.escape(varname) + r'\b', line) is not None

    before_lines = [l for l in lines[:marker_idx] if is_code(l)]
    after_lines = [l for l in lines[marker_idx + 1:] if is_code(l)]

    # Contract check 1: bootpart, mmcdev, mmcpart and scriptaddr must each
    # be ACTUALLY ASSIGNED (not just mentioned in a comment) before the
    # marker -- the fragment reads scriptaddr's already-loaded byte and
    # reuses mmcdev/mmcpart for its own load/fatwrite calls; any of these
    # unset means those commands fail and the whole net silently no-ops
    # (Fable's qemuarm64/virtio counterexample: a board using a different
    # transport variable convention must not pass this check by accident).
    missing_before = [
        v for v in ("bootpart", "mmcdev", "mmcpart", "scriptaddr")
        if not any(assigns(l, v) for l in before_lines)
    ]
    if missing_before:
        bb.fatal(
            "OTAPulse: " + selected + " has " + MARKER + " but never "
            "assigns (only setenv/setexpr.b count, not a comment mention) "
            "the following required variable(s) before the marker: " +
            ", ".join(missing_before) + ". The board script must resolve "
            "bootpart AND set mmcdev/mmcpart/scriptaddr for its own real "
            "hardware before including the bootcount-net fragment."
        )

    # Contract check 2: nothing AFTER the marker may re-assign bootpart --
    # that would silently clobber the fragment's own revert decision on its
    # way out (Fable's review: this is possible today with a naive
    # substring check that only asked "does the word appear anywhere
    # after", regardless of whether it's a real write).
    reassigns_after = [l for l in after_lines if assigns(l, "bootpart")]
    if reassigns_after:
        bb.fatal(
            "OTAPulse: " + selected + " re-assigns bootpart AFTER " + MARKER +
            " (" + reassigns_after[0].strip() + ") -- this would silently "
            "overwrite the fragment's own revert decision. Move any "
            "post-marker bootpart logic before the marker instead."
        )

    # Contract check 3: the LAST bootargs assignment after the marker (not
    # just "any line anywhere after the marker", which a script with two
    # bootargs lines -- the second one clobbering the first -- could pass
    # while still dropping the safety net entirely, per Fable's review)
    # must actually consume RECOVERYARGS_REF, or panic=15/init= never
    # reaches the kernel command line.
    bootargs_lines = [l for l in after_lines if assigns(l, "bootargs")]
    if not bootargs_lines or RECOVERYARGS_REF not in bootargs_lines[-1]:
        bb.fatal(
            "OTAPulse: " + selected + " includes " + MARKER + " but its "
            "LAST bootargs assignment after the marker does not append \"" +
            RECOVERYARGS_REF + "\" -- the fragment's pending-boot "
            "panic=15/init= safety net would never reach the kernel "
            "command line."
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
