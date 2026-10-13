# OTA-Pulse "full" entry point: bootloader compatibility mode switch.
#
# Usage (build-wide, in local.conf; it MUST be a configuration-level INHERIT,
# a recipe-level "inherit" is too late to select the build flavour):
#
#   INHERIT += "otapulse-full"
#
# In "native" mode (the default) this class does NOTHING: it adds no variable
# that any recipe reads, no override, no BBMASK, no task, no event handler.
# Inheriting it in a native build changes nothing.
#
# In "bootloader" mode (OTAPULSE_BOOT_MODE = "bootloader") the device's
# bootloader environment (U-Boot env or GRUB grubenv) owns the boot-slot flag and
# its rollback fallback, and the OTA-Pulse agent drives it. The mode is
# implemented in otapulse-full-bootloader.bbclass, which this class pulls in.
#
# ---------------------------------------------------------------------------
# Customer-facing variables (set them in local.conf; nothing else is needed)
# ---------------------------------------------------------------------------
#   OTAPULSE_BOOT_MODE              "native" (default) | "bootloader"
#   -- bootloader mode only --
#   OTAPULSE_BOOTLOADER             "uboot" | "grub"          (required)
#   OTAPULSE_IMAGE_TYPE             "sd" | "uefi" | "gpt"      (default: sd for uboot, uefi for grub)
#   OTAPULSE_STORAGE_DEVICE         block device holding the partitions, e.g. "/dev/mmcblk0"
#   OTAPULSE_STORAGE_TOTAL_SIZE_MB  size of the medium the image is written to; the root
#                                   filesystem partitions are what remains after the boot
#                                   and data partitions and a small overhead
#   OTAPULSE_BOOT_PART_SIZE_MB      boot (FAT) partition size
#   OTAPULSE_DATA_PART_SIZE_MB      persistent /data partition size
#   OTAPULSE_SWAP_PART_SIZE_MB      swap partition size (0 = none)
#   OTAPULSE_DEVICE_TYPE            device type artifacts are checked against
#                                   (default: derived from MACHINE by the board support)
#   OTAPULSE_PANIC_TIMEOUT_S        kernel panic -> reboot timeout in seconds, added to the
#                                   kernel command line by the bootloader, U-Boot or GRUB
#                                   (default 10;
#                                   0 or negative is refused: a kernel that panics without
#                                   a timeout hangs forever and the bootloader never gets
#                                   the reset it needs to roll the update back)
#   OTAPULSE_UBOOT_ENV_OFFSET       (uboot only, optional) byte offset on the storage device of the
#                                   first copy of the U-Boot environment; default: the board
#                                   integration's layout
#   OTAPULSE_UBOOT_ENV_OFFSET_REDUND (uboot only, optional) byte offset of the second (redundant)
#                                   copy of the U-Boot environment; at least OTAPULSE_UBOOT_ENV_SIZE
#                                   bytes away from the first
#   OTAPULSE_UBOOT_ENV_SIZE         (uboot only, optional) size in bytes of one environment copy
#                                   (default 0x20000)
#   OTAPULSE_UBOOT_AUTO_CONFIGURE   (uboot only, optional) "1" (default): OTA-Pulse adds the boot
#                                   counter and the redundant environment to U-Boot's configuration;
#                                   "0": your U-Boot already has them (the build checks it)
#   OTAPULSE_ALLOW_GRUB_DEBUG       "0" (default): a GRUB image built with the board support's debug
#                                   options (PACKAGECONFIG force-grub-prompt / debug-pause) is
#                                   refused; "1" is a developer-only opt-out, never for a shipped image
#   OTAPULSE_GENERATE_ARTIFACT      "1" (default): every image build also writes the
#                                   .otapulse rootfs artifact (otapulse-artifact.bbclass);
#                                   "0" to build the disk image only
#   OTA_SERVER_URL                  your OTA-Pulse server (as in native mode)
#   OTAPULSE_PROVISIONING_TOKEN     fleet provisioning token (as in native mode): the device
#                                   registers itself with it on first boot
#   OTAPULSE_PROVISIONING_MODE      "baked" (default): the token is written into the image
#                                   (/etc/otapulse/provision.env, mode 0600, never into a
#                                   package); "manual": the image carries no token and the
#                                   device reads /data/otapulse/provision.env, which you
#                                   place yourself
#   OTAPULSE_GROW_DATA_PART         "1" (default): on every boot the persistent data
#                                   partition is extended to the end of the storage medium and
#                                   its file system grown to match, before the agent starts;
#                                   "0" to keep the size the image was built with
# Build checks: a misconfiguration fails the build with a message that names the OTAPULSE_*
# variable to fix, as early as the answer exists (configuration, recipe parse, U-Boot configuration,
# compiled-in U-Boot environment, finished image). See otapulse-full-bootloader.bbclass section 8.
# See conf/samples/local.conf.bootloader.sample for a complete example.
#
# Required layers in bootloader mode (the class cannot add layers, it checks and
# stops with a clear message): meta-otapulse-bsp/core, plus meta-otapulse-bsp/qemu
# for QEMU machines, meta-otapulse-bsp/boards/<vendor> for those boards. A native
# bblayers.conf must NOT contain them.
#
# INTERNAL notes (not customer facing):
#  * The mode switch is the OVERRIDE "otapulse-bootloader" (consumed by
#    soc-ota-agent_1.0.0.bb). It has to be in OVERRIDES before any recipe is
#    parsed, hence the config-level inherit. The override and the BBMASK for the
#    native-only recipes come from this class so the customer writes neither.
#  * The vendored board-integration layer is only ever inherited from here, via
#    "inherit ${@...}" below. The class therefore cannot be inherited at all when
#    the layer is missing, so the layer check runs BEFORE that inherit.

OTAPULSE_BOOT_MODE ?= "native"

def otapulse_full_bsp_requirements(d):
    """Returns [(layer-collection, customer-facing layer dir)] needed in bootloader mode.
    INTERNAL: the collection names are the vendored layers' own."""
    machine = d.getVar('MACHINE') or ''
    req = [('mender', 'meta-otapulse-bsp/core')]
    if machine.startswith('qemu') or machine.startswith('vexpress-qemu'):
        req.append(('mender-qemu', 'meta-otapulse-bsp/qemu'))
    if machine.startswith('raspberrypi'):
        req.append(('mender-raspberrypi', 'meta-otapulse-bsp/boards/raspberrypi'))
    if machine.startswith('imx'):
        req.append(('mender-nxp', 'meta-otapulse-bsp/boards/nxp'))
    return req

def otapulse_full_check_token(d):
    """The provisioning token is written into a shell-readable environment file at image
    build time; refuse anything that is not a plain token, with a message that names the
    variable (and never prints the value)."""
    import re
    tok = d.getVar('OTAPULSE_PROVISIONING_TOKEN') or ''
    if tok and not re.fullmatch(r'[A-Za-z0-9_.-]+', tok):
        bb.fatal("OTAPULSE_PROVISIONING_TOKEN contains characters that are not valid in a "
                 "provisioning token (allowed: letters, digits, underscore, dot, dash). "
                 "Copy the token again from your dashboard.")

def otapulse_full_image_type_conflict(bl, it):
    """Message when OTAPULSE_IMAGE_TYPE cannot be combined with OTAPULSE_BOOTLOADER (TASK-S109-012),
    else None. "gpt" is accepted with both bootloaders."""
    if bl == 'grub' and it == 'sd':
        return ("OTAPULSE_IMAGE_TYPE = \"sd\" (an MBR disk image for U-Boot boards) cannot be combined with "
                "OTAPULSE_BOOTLOADER = \"grub\". Set OTAPULSE_IMAGE_TYPE = \"uefi\" (or \"gpt\"), or set "
                "OTAPULSE_BOOTLOADER = \"uboot\" if your board boots through U-Boot.")
    if bl == 'uboot' and it == 'uefi':
        return ("OTAPULSE_IMAGE_TYPE = \"uefi\" (a UEFI disk image that boots through GRUB) cannot be combined "
                "with OTAPULSE_BOOTLOADER = \"uboot\". Set OTAPULSE_IMAGE_TYPE = \"sd\" (or \"gpt\"), or set "
                "OTAPULSE_BOOTLOADER = \"grub\" if your board boots through GRUB and UEFI.")
    return None

def otapulse_full_validate(d):
    """Bootloader-mode customer variables. Runs BEFORE the board support is
    inherited (it expands them), so the messages are the customer's first contact."""
    bl = (d.getVar('OTAPULSE_BOOTLOADER') or '').strip()
    if bl not in ('uboot', 'grub'):
        bb.fatal("OTAPULSE_BOOT_MODE = \"bootloader\" requires OTAPULSE_BOOTLOADER to be \"uboot\" "
                 "or \"grub\" (got \"%s\"). Set it in local.conf." % bl)
    it = (d.getVar('OTAPULSE_IMAGE_TYPE') or '').strip()
    if not it:
        it = 'uefi' if bl == 'grub' else 'sd'
        d.setVar('OTAPULSE_IMAGE_TYPE', it)
    if it not in ('sd', 'uefi', 'gpt'):
        bb.fatal("OTAPULSE_IMAGE_TYPE must be \"sd\", \"uefi\" or \"gpt\" (got \"%s\")." % it)
    conflict = otapulse_full_image_type_conflict(bl, it)
    if conflict:
        bb.fatal(conflict)
    pt = (d.getVar('OTAPULSE_PANIC_TIMEOUT_S') or '10').strip()
    if not pt.isdigit() or int(pt) < 1:
        bb.fatal("OTAPULSE_PANIC_TIMEOUT_S must be a positive number of seconds (got \"%s\"). "
                 "Bootloader mode requires a kernel panic timeout: a kernel that panics without "
                 "one hangs forever and a failed update is never rolled back." % pt)

    pm = (d.getVar('OTAPULSE_PROVISIONING_MODE') or 'baked').strip()
    if pm not in ('baked', 'manual'):
        bb.fatal("OTAPULSE_PROVISIONING_MODE must be \"baked\" or \"manual\" (got \"%s\")." % pm)

    otapulse_full_check_token(d)

    gd = (d.getVar('OTAPULSE_GROW_DATA_PART') or '1').strip()
    if gd not in ('0', '1'):
        bb.fatal("OTAPULSE_GROW_DATA_PART must be \"1\" or \"0\" (got \"%s\")." % gd)

def otapulse_full_select(d):
    """Evaluated once, at configuration level, by the inherit below. Returns the
    class to pull in ('' = nothing: native mode)."""
    mode = (d.getVar('OTAPULSE_BOOT_MODE') or '').strip() or 'native'
    present = (d.getVar('BBFILE_COLLECTIONS') or '').split()

    if mode == 'native':
        # Inert. The only thing done is a hint for an easy misconfiguration.
        if 'mender' in present:
            bb.warn("OTAPULSE_BOOT_MODE is \"native\" but the OTA-Pulse bootloader support "
                    "layers (meta-otapulse-bsp/*) are listed in BBLAYERS. Remove them from "
                    "bblayers.conf, or set OTAPULSE_BOOT_MODE = \"bootloader\".")
        return ''

    if mode != 'bootloader':
        bb.fatal("OTAPULSE_BOOT_MODE must be \"native\" or \"bootloader\" (got \"%s\")." % mode)

    missing = [path for coll, path in otapulse_full_bsp_requirements(d) if coll not in present]
    if missing:
        bb.fatal("OTAPULSE_BOOT_MODE = \"bootloader\" requires the OTA-Pulse bootloader support "
                 "layers, but your BBLAYERS is missing: %s. Add %s to bblayers.conf "
                 "(bitbake-layers add-layer <path>; they ship next to meta-otapulse), or set "
                 "OTAPULSE_BOOT_MODE = \"native\"."
                 % (', '.join(missing), ' and '.join(missing)))
    otapulse_full_validate(d)
    return 'otapulse-full-bootloader'

inherit ${@otapulse_full_select(d)}
