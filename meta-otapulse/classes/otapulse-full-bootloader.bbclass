# OTA-Pulse bootloader compatibility mode (INTERNAL class).
#
# Pulled in by otapulse-full.bbclass ONLY when OTAPULSE_BOOT_MODE = "bootloader".
# Customers never inherit this directly; they write "INHERIT += "otapulse-full""
# and OTAPULSE_* variables (see otapulse-full.bbclass and
# conf/samples/local.conf.bootloader.sample for the customer-facing surface).
#
# What it does (INTERNAL view; "board support" = the vendored board-integration
# layers in meta-otapulse-bsp/, derived from upstream meta-mender):
#  1. adds the OVERRIDE "otapulse-bootloader" (read by soc-ota-agent_1.0.0.bb);
#  2. turns the OTAPULSE_* variables into the board support's feature switches
#     (MENDER_FEATURES_ENABLE) and sizing variables (MENDER_*), with "?=" semantics;
#  3. NEVER enables the device-side update client features ("mender-auth-install",
#     "mender-update-install"): the OTA-Pulse agent is the only client. Refuses to
#     build when they are enabled or when MENDER_FEATURES is assigned directly;
#  4. masks the native-only recipes that would collide with the board support
#     (native U-Boot bbappend, u-boot-env-config, base-files bbappend);
#  5. replaces the board support's own ".mender" artifact image type and bootstrap
#     artifact with our otapulse-artifact.bbclass (.otapulse);
#  6. adds a kernel panic timeout (panic=N) to the kernel command line and refuses to
#     build an image without one (U-Boot: through the bootloader's setup-commands hook;
#     GRUB: through a boot-configuration fragment added by a bbappend that lives in
#     dynamic-layers/ and is only parsed when the board support is part of the build,
#     see TASK-S109-030).
#     A kernel that panics without a timeout hangs forever and the bootloader
#     never gets the reset it needs to roll the update back (SPIKE.md s7.7, Opus
#     binding adjustment). CONFIG_PANIC_TIMEOUT in a kernel config is deliberately
#     NOT relied on: vendor kernels do not carry our kernel fragments.
#
#  7. provisioning token (TASK-S109-013, Opus decision): the customer's existing
#     OTAPULSE_PROVISIONING_TOKEN is written into the IMAGE (never into a package) as
#     /etc/otapulse/provision.env (mode 0600, that one variable only), which the
#     unchanged otapulse-auto-provision.service already reads. OTAPULSE_PROVISIONING_MODE =
#     "manual" ships no token at all; the otapulse-firstboot package then adds a drop-in
#     that reads /data/otapulse/provision.env instead.
#
# How the native-only pieces are kept out of a bootloader image (TASK-S109-009), ONE
# mechanism per item:
#   * BBMASK (this class, section 4), the recipe is not parsed at all: the native U-Boot
#     bbappend, u-boot-env-config, the base-files bbappend (custom fstab).
#   * the "otapulse-bootloader" override inside the recipe that is kept in part:
#       - otapulse-firstboot keeps otapulse-auto-provision and otapulse-machine-id, drops
#         otapulse-partition-setup / otapulse-boot-health / otapulse-boot-deadline and the
#         native udev aliases, and adds otapulse-data-seed;
#       - soc-ota-agent drops the /usr/bin/mender symlink and gptfdisk, adds
#         otapulse-growfs-data (recipe skipped outside bootloader mode).
#
#  8. sanity checks (TASK-S109-012): see section 8 below. Everything a customer can get wrong
#     in bootloader mode fails the build with a message that names the OTAPULSE_* variable to
#     fix: at configuration/recipe parse time where the value is known then (variable values,
#     debug options, the state-script gate), and at the earliest build step where the answer only
#     exists in a build product (the U-Boot configuration, the compiled-in U-Boot environment,
#     the finished root file system).
#
# Not here (other tasks): the bootloader-mode boot deadline (TASK-S109-011), the real
# panic->rollback run on GRUB (TASK-S109-018; panic=N DELIVERY for GRUB is section 6 above,
# TASK-S109-030).

inherit mender-setup

# ---- 1. the mode override ---------------------------------------------------
DISTROOVERRIDES:append = ":otapulse-bootloader"

# ---- 2a. customer variables ------------------------------------------------
OTAPULSE_PANIC_TIMEOUT_S ?= "10"
OTAPULSE_GENERATE_ARTIFACT ?= "1"
OTAPULSE_PROVISIONING_MODE ?= "baked"
OTAPULSE_PROVISIONING_TOKEN ?= ""

# Feature switches derived from OTAPULSE_BOOTLOADER / OTAPULSE_IMAGE_TYPE. The
# handler below validates both before anything here is expanded.
def otapulse_full_features(d):
    bl = d.getVar('OTAPULSE_BOOTLOADER') or ''
    it = d.getVar('OTAPULSE_IMAGE_TYPE') or ''
    bl_feat = {'uboot': 'mender-uboot', 'grub': 'mender-grub'}.get(bl)
    it_feat = {'sd': 'mender-image-sd', 'uefi': 'mender-image-uefi', 'gpt': 'mender-image-gpt'}.get(it)
    if not bl_feat or not it_feat:
        bb.fatal("OTAPULSE_BOOTLOADER must be \"uboot\" or \"grub\" and OTAPULSE_IMAGE_TYPE must be "
                 "\"sd\", \"uefi\" or \"gpt\" (got \"%s\" and \"%s\")." % (bl, it))
    return ' '.join(['mender-image', bl_feat, it_feat])

MENDER_FEATURES_ENABLE:append = " ${@otapulse_full_features(d)}"

# ---- 3 (helpers). guards ------------------------------------------------------
OTAPULSE_FULL_FORBIDDEN_FEATURES = "mender-auth-install mender-update-install"

def otapulse_full_enabled_features(d):
    enabled = (d.getVar('MENDER_FEATURES_ENABLE') or '').split()
    disabled = (d.getVar('MENDER_FEATURES_DISABLE') or '').split()
    return [f for f in enabled if f not in disabled]

def otapulse_full_check_forbidden_features(d, where):
    bad = [f for f in otapulse_full_enabled_features(d)
           if f in (d.getVar('OTAPULSE_FULL_FORBIDDEN_FEATURES') or '').split()]
    if bad:
        bb.fatal("OTAPULSE_BOOT_MODE = \"bootloader\" never installs a device-side update client, "
                 "but %s enable(s) %s via MENDER_FEATURES_ENABLE. OTA-Pulse updates are always "
                 "performed by the OTA-Pulse agent. Remove %s from your configuration and use the "
                 "OTAPULSE_* variables instead (see conf/samples/local.conf.bootloader.sample)."
                 % (where, ' and '.join(bad), 'it' if len(bad) == 1 else 'them'))

def otapulse_full_conf_assignments(d, varname):
    """Lines in the parsed configuration files (outside the vendored board support
    layers) that assign VARNAME directly, as [(file, lineno, text)]. Variable
    history is only recorded under 'bitbake -e', so this reads the files that the
    configuration parser reported."""
    import re
    op = r'(?:\?\?=|\?=|:=|\+=|=\+|\.=|=\.|=(?!=))'
    pat = re.compile(r'^\s*(?:export\s+)?' + re.escape(varname) + r'(?::[\w.+\-]+)*\s*' + op)
    hits = []
    seen = set()
    # (the parser calls the list '__depends' until the configuration is complete)
    for ent in (d.getVar('__base_depends', False) or []) + (d.getVar('__depends', False) or []):
        path = ent[0] if isinstance(ent, (list, tuple)) else ent
        if path in seen or not path.endswith('.conf') or '/meta-otapulse-bsp/' in path:
            continue
        seen.add(path)
        try:
            with open(path, 'r', errors='replace') as f:
                for n, line in enumerate(f, 1):
                    if pat.match(line):
                        hits.append((path, n, line.strip()))
        except OSError:
            pass
    return hits

# ---- 2b/3. one-time configuration handler -------------------------------------
addhandler otapulse_full_config_handler
otapulse_full_config_handler[eventmask] = "bb.event.ConfigParsed"
python otapulse_full_config_handler() {
    # ---- the client-install features are never allowed ----
    otapulse_full_check_forbidden_features(d, 'your configuration')

    # ---- MENDER_FEATURES must not be assigned directly ----
    hits = otapulse_full_conf_assignments(d, 'MENDER_FEATURES')
    if hits:
        bb.fatal("MENDER_FEATURES is assigned directly at %s:%d (\"%s\"). Do not set it. In "
                 "bootloader mode the feature set is derived from OTAPULSE_BOOTLOADER and "
                 "OTAPULSE_IMAGE_TYPE; remove the assignment."
                 % (hits[0][0], hits[0][1], hits[0][2]))
    # (an ':append'/':remove' on the result is caught by the board support's own
    # consistency check, which compares MENDER_FEATURES with the enabled list)

    # ---- the U-Boot environment variables (TASK-S109-012): syntax, applicability ----
    otapulse_full_check_uboot_vars(d)

    # ---- OTAPULSE_* -> board support variables, "?=" semantics ----
    # A MENDER_* variable that is already set (explicit advanced override) wins.
    # noweakdefault: the board support's own "??=" defaults do not count as "set".
    for ovar, mvar in (('OTAPULSE_STORAGE_DEVICE', 'MENDER_STORAGE_DEVICE'),
                       ('OTAPULSE_STORAGE_TOTAL_SIZE_MB', 'MENDER_STORAGE_TOTAL_SIZE_MB'),
                       ('OTAPULSE_BOOT_PART_SIZE_MB', 'MENDER_BOOT_PART_SIZE_MB'),
                       ('OTAPULSE_DATA_PART_SIZE_MB', 'MENDER_DATA_PART_SIZE_MB'),
                       ('OTAPULSE_SWAP_PART_SIZE_MB', 'MENDER_SWAP_PART_SIZE_MB'),
                       # U-Boot only (validated above); unset unless the customer sets them
                       ('OTAPULSE_UBOOT_ENV_OFFSET', 'MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET'),
                       ('OTAPULSE_UBOOT_ENV_OFFSET_REDUND', 'MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2'),
                       ('OTAPULSE_UBOOT_ENV_SIZE', 'BOOTENV_SIZE'),
                       ('OTAPULSE_UBOOT_AUTO_CONFIGURE', 'MENDER_UBOOT_AUTO_CONFIGURE')):
        val = d.getVar(ovar, False, noweakdefault=True)
        if val is not None and val.strip() != '' and d.getVar(mvar, False, noweakdefault=True) is None:
            d.setVar(mvar, val.strip())

    # device type: ONE value must reach the agent (device_type file), the image name
    # and the artifact. Customer value wins; otherwise the board support's default
    # (which may carry a board-specific suffix) is pinned here so a later
    # "MENDER_DEVICE_TYPE ?= ${MACHINE}" in an image class cannot shadow it.
    cur = d.getVar('MENDER_DEVICE_TYPE', False, noweakdefault=True)
    cust = d.getVar('OTAPULSE_DEVICE_TYPE', False, noweakdefault=True)
    if cust is not None and cust.strip() != '':
        cust = cust.strip()
        if cur not in (None, cust, '${MACHINE}', '${MENDER_DEVICE_TYPE_DEFAULT}') \
                and d.expand(cur) != d.expand(cust):
            bb.fatal("OTAPULSE_DEVICE_TYPE (\"%s\") conflicts with MENDER_DEVICE_TYPE (\"%s\"). "
                     "Set OTAPULSE_DEVICE_TYPE only." % (cust, cur))
        d.setVar('MENDER_DEVICE_TYPE', cust)
    elif cur in (None, '${MACHINE}'):
        d.setVar('MENDER_DEVICE_TYPE', '${MENDER_DEVICE_TYPE_DEFAULT}')

    # Fail ONCE, early, for the common case (the panic setting is visible at
    # configuration level). The per-image check stays as the backstop for
    # recipe-level changes.
    otapulse_full_check_panic(d)

    # Proof, for the per-recipe checks, that this class was inherited at
    # configuration level (the only place this handler can be registered from).
    d.setVar('OTAPULSE_FULL_CONFIGURED', '1')
}

# ---- 4. native-only recipes that collide with the board support ---------------
# (native U-Boot bbappend: second env definition; u-boot-env-config: switch-boot-slot.sh +
# fw_env.config; base-files bbappend: custom fstab with /data.) Anchored on THIS
# layer's directory (LAYERDIR_OTAPULSE, set in conf/layer.conf) so the same paths in
# meta-otapulse-bsp/ are never touched.
# BBMASK is the single gate for these three (TASK-S109-009): each is a whole recipe or
# bbappend that exists only for the native layout, so a mask is exact, and anything that
# still asks for a masked recipe (an image listing u-boot-env-config, say) fails the
# build with "Nothing PROVIDES" instead of silently getting a second boot environment.
# Pieces that are only PART native (otapulse-firstboot, soc-ota-agent) are gated by the
# override inside their own recipe instead; see the header of this class.
def otapulse_full_bbmask(d):
    import re
    base = re.escape((d.getVar('LAYERDIR_OTAPULSE') or '').rstrip('/'))
    if not base:
        bb.fatal("LAYERDIR_OTAPULSE is not defined: is meta-otapulse's conf/layer.conf up to date?")
    return ' '.join('^%s/recipes-%s/' % (base, sub)
                    for sub in ('bsp/u-boot', 'bsp/u-boot-env-config', 'core/base-files'))
BBMASK += "${@otapulse_full_bbmask(d)}"

# ---- 5. artifacts: ours, not the board support's -------------------------------
# No board-support "mender" image type and no bootstrap artifact (the latter pulls
# the artifact tool into the build; our agent keeps its own state, see SPIKE.md s7.3).
IMAGE_FSTYPES:remove = "mender"
IMAGE_TYPEDEP:dataimg:remove = "bootstrap-artifact"
# Every image also gets the signed .otapulse rootfs artifact from our class.
IMAGE_CLASSES += "${@'otapulse-artifact' if d.getVar('OTAPULSE_GENERATE_ARTIFACT') == '1' else ''}"

# ---- 6. kernel panic timeout through the bootloader command hook ---------------
# Runs after the board support's setup. Escaped dollar: the shell that writes the
# U-Boot header must not expand it (see TASK-S109-001: \${fdtcontroladdr}).
# The command line is ONE argument ("..."): an unquoted ${bootargs} is split into words by U-Boot's shell, and a command
# with more than CONFIG_SYS_MAXARGS (16) arguments is refused, silently dropping panic=N. The Raspberry Pi firmware hands
# U-Boot a 26-word command line, so the unquoted form lost the timeout there (TASK-S109-019, found on the bench board).
# \\\" below becomes \" in the generated C header and " in the U-Boot environment.
MENDER_UBOOT_POST_SETUP_COMMANDS:prepend:mender-uboot = "setenv bootargs \\\"\${bootargs} panic=${OTAPULSE_PANIC_TIMEOUT_S}\\\"; "

# The kernel command line must end up with a positive panic= value. Returns the LAST
# panic= value found in TEXT (the kernel honours the last one), or None.
def otapulse_full_last_panic(text):
    import re
    vals = re.findall(r'(?<![\w-])panic=(-?\d+)', text or '')
    return int(vals[-1]) if vals else None

def otapulse_full_check_grub_panic_support(d):
    """GRUB: the panic timeout is delivered by a boot-configuration fragment that a
    bbappend in dynamic-layers/ adds to the board support's GRUB recipe. At
    configuration level the most that can be proven is that the bbappend exists and
    that BBFILES_DYNAMIC will really load it; the recipe-level check (python () below)
    and the build gate in the bbappend (do_compile) prove the rest."""
    import glob, os
    base = (d.getVar('LAYERDIR_OTAPULSE') or '').rstrip('/')
    support = os.path.join(base, 'dynamic-layers', 'mender', 'recipes-bsp',
                           'grub-mender-grubenv', 'grub-mender-grubenv_%.bbappend')
    if not base or not os.path.isfile(support):
        bb.fatal("OTAPULSE_BOOTLOADER = \"grub\": the OTA-Pulse kernel panic timeout support for GRUB is "
                 "missing from your meta-otapulse layer, so the kernel panic timeout cannot be delivered. "
                 "Bootloader mode refuses to build an image whose hung kernel would never be rolled back. "
                 "Update the layer.")
    loaded = False
    for ent in (d.getVar('BBFILES_DYNAMIC') or '').split():
        coll, _, pattern = ent.partition(':')
        if coll == 'mender' and support in glob.glob(pattern):
            loaded = True
    if not loaded:
        bb.fatal("OTAPULSE_BOOTLOADER = \"grub\": the OTA-Pulse kernel panic timeout support for GRUB is not "
                 "registered in BBFILES_DYNAMIC (conf/layer.conf of meta-otapulse), so it would not be loaded. "
                 "Bootloader mode refuses to build an image whose hung kernel would never be rolled back.")

def otapulse_full_check_panic(d):
    """Fatal unless the bootloader leaves a positive panic= on the kernel command
    line. U-Boot: checked from the setup commands. GRUB: the timeout value is checked
    here and the delivery is checked by otapulse_full_check_grub_panic_support (this
    function), the recipe check and the grub.cfg build gate."""
    if d.getVar('OTAPULSE_BOOTLOADER') == 'grub':
        pt = (d.getVar('OTAPULSE_PANIC_TIMEOUT_S') or '').strip()
        if not pt.isdigit() or int(pt) < 1:
            bb.fatal("OTAPULSE_PANIC_TIMEOUT_S must be a positive number of seconds (got \"%s\"). "
                     "Bootloader mode requires a kernel panic timeout: a kernel that panics without one "
                     "hangs forever and a failed update is never rolled back." % pt)
        otapulse_full_check_grub_panic_support(d)
        return
    text = (d.getVar('MENDER_UBOOT_PRE_SETUP_COMMANDS') or '') + ' ' + \
           (d.getVar('MENDER_UBOOT_POST_SETUP_COMMANDS') or '')
    panic = otapulse_full_last_panic(text)
    if panic is None or panic < 1:
        bb.fatal("Bootloader mode requires a kernel panic timeout on the kernel command line, but "
                 "the bootloader setup commands %s. Set OTAPULSE_PANIC_TIMEOUT_S (default 10) and do "
                 "not override it with panic=0 in your own bootloader commands."
                 % ('contain panic=%d' % panic if panic is not None else 'do not set panic='))

def otapulse_full_check_image(d):
    otapulse_full_check_forbidden_features(d, 'an image recipe')
    otapulse_full_check_panic(d)

python () {
    if d.getVar('OTAPULSE_FULL_CONFIGURED') != '1':
        bb.fatal("otapulse-full is enabled in bootloader mode from a recipe (inherit), which is too "
                 "late to select the build flavour. Enable it build-wide: INHERIT += \"otapulse-full\" "
                 "in local.conf, and remove it from your recipes.")
    if bb.data.inherits_class('image', d):
        otapulse_full_check_image(d)
    # GRUB: the recipe that builds the boot configuration must really carry the panic
    # timeout support from the dynamic-layers bbappend (fragment writer + build gate).
    if d.getVar('PN') == 'grub-mender-grubenv' and d.getVar('OTAPULSE_BOOTLOADER') == 'grub':
        gate = (d.getVarFlag('do_compile', 'postfuncs') or '').split()
        # (the fragment writer is do_configure:prepend in the bbappend; it names the file
        # through OTAPULSE_PANIC_FRAGMENT_NAME, which is how it is recognised here)
        if 'otapulse_full_check_grub_panic' not in gate or \
                '${OTAPULSE_PANIC_FRAGMENT_NAME}' not in (d.getVar('do_configure', False) or ''):
            bb.fatal("OTAPULSE_BOOTLOADER = \"grub\": the GRUB boot configuration recipe does not carry the "
                     "OTA-Pulse kernel panic timeout support (the bbappend from meta-otapulse was not applied). "
                     "Bootloader mode refuses to build an image whose hung kernel would never be rolled back.")
    # Bootloader (U-Boot) recipes built with the board support: verify the generated
    # defines header really carries the panic= setting that goes into the compiled-in env.
    if d.getVar('do_provide_mender_defines', False):
        d.appendVarFlag('do_provide_mender_defines', 'postfuncs', ' otapulse_full_check_uboot_defines')
        # TASK-S109-012 (section 8): the environment layout is checked now (parse time), the
        # U-Boot configuration right after do_configure and the compiled-in environment
        # right after do_compile (the earliest points where those exist).
        otapulse_full_check_uboot_layout(d)
        d.appendVarFlag('do_configure', 'prefuncs', ' otapulse_full_uboot_defconfig otapulse_full_check_uboot_config_early')
        d.appendVarFlag('do_configure', 'postfuncs', ' otapulse_full_uboot_cfg otapulse_full_check_uboot_config')
        d.appendVarFlag('do_compile', 'postfuncs', ' otapulse_full_check_uboot_env')
    # TASK-S109-019: boards that boot through a boot script (Raspberry Pi): the boot script and
    # cmdline.txt that the disk image really carries.
    if bb.data.inherits_class('image', d) and d.getVar('OTAPULSE_BOOTLOADER') == 'uboot':
        d.appendVarFlag('do_image_sdimg', 'postfuncs', ' otapulse_bl_bootfs_checks')
    # TASK-S109-012: GRUB debug options, and the state-script gate of the agent recipe.
    if d.getVar('PN') == 'grub-mender-grubenv' and d.getVar('OTAPULSE_BOOTLOADER') == 'grub':
        otapulse_full_check_grub_debug(d)
    if d.getVar('PN') == 'soc-ota-agent':
        otapulse_full_check_agent_recipe(d)
}

otapulse_full_check_uboot_defines() {
    hdr="${S}/include/config_mender_defines.h"
    [ -f "$hdr" ] || bbfatal "otapulse-full: ${hdr} was not generated"
    if ! grep -Eq '^#define MENDER_UBOOT_(PRE|POST)_SETUP_COMMANDS "(.*[ ;])?panic=[1-9][0-9]*' "$hdr"; then
        bbfatal "otapulse-full: the bootloader environment built here does not set a kernel panic timeout (no panic=N in the setup commands of $hdr)"
    fi
    bbnote "otapulse-full: bootloader setup commands carry: $(grep -E '^#define MENDER_UBOOT_POST_SETUP_COMMANDS' "$hdr")"
}

# ---- 7. provisioning token: image-level file, never a package -------------------
# The token is a credential. A package that carried it would put it in the package feed
# and in the shared package/sstate caches, so it is written here, into the root file
# system of THIS image only, at the end of the image build. Mode 0600, root, and the file
# holds that one variable (otapulse-auto-provision.service reads it as an EnvironmentFile).
#   OTAPULSE_PROVISIONING_MODE = "baked" (default) and a token set: file written.
#   OTAPULSE_PROVISIONING_MODE = "manual": no file; the operator places
#       /data/otapulse/provision.env (see the otapulse-firstboot drop-in). A stale file is removed.
ROOTFS_POSTPROCESS_COMMAND += "otapulse_bl_provision_env;"
otapulse_bl_provision_env() {
    f="${IMAGE_ROOTFS}${sysconfdir}/otapulse/provision.env"
    if [ "${OTAPULSE_PROVISIONING_MODE}" != "baked" ]; then
        rm -f "$f"
        return 0
    fi
    token='${OTAPULSE_PROVISIONING_TOKEN}'
    [ -n "$token" ] || return 0
    case "$token" in
        *[!A-Za-z0-9_.-]*) bbfatal "OTAPULSE_PROVISIONING_TOKEN contains characters that are not valid in a token (allowed: letters, digits, underscore, dot, dash)" ;;
    esac
    install -d -m 0755 ${IMAGE_ROOTFS}${sysconfdir}/otapulse
    ( umask 077; printf 'OTAPULSE_PROVISIONING_TOKEN=%s\n' "$token" > "$f" )
    chmod 0600 "$f"
    chown 0:0 "$f"
}
otapulse_bl_provision_env[vardeps] += "OTAPULSE_PROVISIONING_TOKEN OTAPULSE_PROVISIONING_MODE"

# ---- 8. sanity checks (TASK-S109-012) -------------------------------------------
# Each check fails the build with a message that names the OTAPULSE_* variable to fix.
# Where each one runs, and why there:
#   configuration parse   OTAPULSE_UBOOT_* syntax / applicability (handler above); the image type
#                         against the bootloader (otapulse-full.bbclass, before the board support
#                         is even inherited)
#   recipe parse          U-Boot environment layout (redundancy, overlap, size), the GRUB debug
#                         options, the state-script gate of the agent recipe
#   around do_configure   the U-Boot configuration itself: environment offset/size against what the
#                         agent (fw_env.config) uses (before the board support compares, and again
#                         after the configuration fragments are merged); boot counter and redundant
#                         environment (after the merge)
#   after do_compile      the compiled-in U-Boot environment: the boot-limit/rollback commands
#                         and the kernel command line that U-Boot really passes (panic=N)
#   rootfs post-process   /etc/otapulse/boot_mode, the state scripts, fw_env.config in the image
# The pure helpers below are unit-tested (test_bl_sanity.py next to this file).

# Developer opt-out: "1" lets a GRUB image carry the board support's debug options
# (PACKAGECONFIG force-grub-prompt / debug-pause), which stop an unattended device at a
# GRUB prompt or pause. Never set it for an image that ships.
OTAPULSE_ALLOW_GRUB_DEBUG ?= "0"

def otapulse_full_parse_number(text):
    """Decimal or 0x-hexadecimal text as an int; None when it is neither."""
    import re
    t = (text or '').strip()
    if re.fullmatch(r'0[xX][0-9a-fA-F]+', t):
        return int(t, 16)
    if re.fullmatch(r'[0-9]+', t):
        return int(t, 10)
    return None

def otapulse_full_env_layout_problem(off1, off2, size):
    """None when the two copies of the U-Boot environment are a valid redundant pair,
    else (OTAPULSE variable to fix, message). Arguments are ints (None = unusable)."""
    if size is None or size < 1:
        return ('OTAPULSE_UBOOT_ENV_SIZE',
                "The U-Boot environment size is not a positive number. Set OTAPULSE_UBOOT_ENV_SIZE to the "
                "size in bytes of your U-Boot environment (for example 0x20000).")
    if off1 is None or off1 < 1:
        return ('OTAPULSE_UBOOT_ENV_OFFSET',
                "The offset of the first U-Boot environment copy is not a positive number. Set "
                "OTAPULSE_UBOOT_ENV_OFFSET to the byte offset on the storage device where your U-Boot "
                "keeps its environment.")
    if off2 is None or off2 < 1:
        return ('OTAPULSE_UBOOT_ENV_OFFSET_REDUND',
                "The U-Boot environment is not redundant: there is no offset for a second copy. Set "
                "OTAPULSE_UBOOT_ENV_OFFSET_REDUND to the byte offset of the second environment copy. Without "
                "a second copy a power cut while the environment is written leaves no valid environment, and "
                "the device can neither boot nor roll a failed update back.")
    if off1 == off2:
        return ('OTAPULSE_UBOOT_ENV_OFFSET_REDUND',
                "The U-Boot environment is not redundant: both copies are at offset 0x%x. Set "
                "OTAPULSE_UBOOT_ENV_OFFSET_REDUND to a second, different offset (at least "
                "OTAPULSE_UBOOT_ENV_SIZE bytes away from OTAPULSE_UBOOT_ENV_OFFSET). Without a second "
                "copy a power cut while the environment is written leaves no valid environment."
                % off1)
    if abs(off2 - off1) < size:
        return ('OTAPULSE_UBOOT_ENV_OFFSET_REDUND',
                "The two U-Boot environment copies (offsets 0x%x and 0x%x, size 0x%x each) overlap. Set "
                "OTAPULSE_UBOOT_ENV_OFFSET_REDUND at least OTAPULSE_UBOOT_ENV_SIZE bytes away from "
                "OTAPULSE_UBOOT_ENV_OFFSET, or reduce OTAPULSE_UBOOT_ENV_SIZE." % (off1, off2, size))
    return None

def otapulse_full_check_uboot_vars(d):
    """Configuration parse: the customer-facing U-Boot environment variables. They only
    make sense for OTAPULSE_BOOTLOADER = "uboot" and must be plain byte counts."""
    bl = (d.getVar('OTAPULSE_BOOTLOADER') or '').strip()
    names = ('OTAPULSE_UBOOT_ENV_OFFSET', 'OTAPULSE_UBOOT_ENV_OFFSET_REDUND',
             'OTAPULSE_UBOOT_ENV_SIZE', 'OTAPULSE_UBOOT_AUTO_CONFIGURE')
    given = [v for v in names if (d.getVar(v) or '').strip() != '']
    if given and bl != 'uboot':
        bb.fatal("%s only applies to OTAPULSE_BOOTLOADER = \"uboot\" (yours is \"%s\"). Remove %s from your "
                 "configuration, or set OTAPULSE_BOOTLOADER = \"uboot\"."
                 % (' and '.join(given), bl, 'it' if len(given) == 1 else 'them'))
    for v in names[:3]:
        val = (d.getVar(v) or '').strip()
        if val == '':
            continue
        n = otapulse_full_parse_number(val)
        if n is None or n < 1 or n % 512:
            bb.fatal("%s must be a positive number of bytes that is a multiple of 512, in decimal or "
                     "0x hexadecimal (got \"%s\")." % (v, val))
    ac = (d.getVar('OTAPULSE_UBOOT_AUTO_CONFIGURE') or '').strip()
    if ac not in ('', '0', '1'):
        bb.fatal("OTAPULSE_UBOOT_AUTO_CONFIGURE must be \"1\" (OTA-Pulse adds the boot counter and the "
                 "redundant environment to your U-Boot) or \"0\" (your U-Boot already has them) "
                 "(got \"%s\")." % ac)
    o1 = otapulse_full_parse_number(d.getVar('OTAPULSE_UBOOT_ENV_OFFSET'))
    o2 = otapulse_full_parse_number(d.getVar('OTAPULSE_UBOOT_ENV_OFFSET_REDUND'))
    if o1 is not None and o2 is not None:
        sz = otapulse_full_parse_number(d.getVar('OTAPULSE_UBOOT_ENV_SIZE')) or 1
        prob = otapulse_full_env_layout_problem(o1, o2, sz)
        if prob:
            bb.fatal(prob[1])

def otapulse_full_env_region_problem(base, off1, off2, size, total_aligned, reserved, alignment):
    """None when both environment copies lie inside the area that the image layout reserves for them
    (BASE .. BASE + TOTAL_ALIGNED, directly followed by the first partition) and that area fits the space
    reserved ahead of the partitions; else (OTAPULSE variable to fix, message). All arguments are ints."""
    if alignment and base is not None and base % alignment:
        return ('OTAPULSE_UBOOT_ENV_OFFSET',
                "OTAPULSE_UBOOT_ENV_OFFSET (0x%x) must be a multiple of the partition alignment (0x%x bytes): "
                "the environment area is laid out on that grid. Set OTAPULSE_UBOOT_ENV_OFFSET to a multiple of "
                "0x%x." % (base, alignment, alignment))
    if reserved is not None and total_aligned > reserved:
        return ('OTAPULSE_UBOOT_ENV_SIZE',
                "Two copies of the U-Boot environment of OTAPULSE_UBOOT_ENV_SIZE = 0x%x bytes need 0x%x bytes "
                "(rounded up to the partition alignment), but only 0x%x bytes are reserved for the bootloader "
                "data ahead of the first partition. Reduce OTAPULSE_UBOOT_ENV_SIZE."
                % (size, total_aligned, reserved))
    lo, hi = base, base + total_aligned
    for name, off, var in (('first', off1, 'OTAPULSE_UBOOT_ENV_OFFSET'),
                           ('second', off2, 'OTAPULSE_UBOOT_ENV_OFFSET_REDUND')):
        if off < lo or off + size > hi:
            return (var,
                    "The %s U-Boot environment copy (offset 0x%x, size 0x%x) lies outside the area reserved for "
                    "the environment (0x%x to 0x%x); the first partition of the image would overwrite it. Set "
                    "%s so that the copy lies inside that area, or leave it unset."
                    % (name, off, size, lo, hi, var))
    return None

def otapulse_full_check_uboot_layout(d):
    """Recipe parse of the U-Boot recipe: the two environment copies really are a valid
    redundant pair inside the area the image layout reserves for them (this is what both U-Boot's
    configuration and the agent's fw_env.config are generated from). Runs at parse time so that the
    board support's own build-time checks of the same facts can never be the first to report."""
    if bb.utils.contains('MENDER_FEATURES', 'mender-ubi', True, False, d):
        return
    try:
        off1 = otapulse_full_parse_number(d.getVar('MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1'))
        off2 = otapulse_full_parse_number(d.getVar('MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2'))
        size = otapulse_full_parse_number(d.getVar('BOOTENV_SIZE'))
        base = otapulse_full_parse_number(d.getVar('MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET'))
        total = otapulse_full_parse_number(d.getVar('MENDER_BOOTENV_TOTAL_ALIGNED_SIZE'))
        reserved = otapulse_full_parse_number(d.getVar('MENDER_RESERVED_SPACE_BOOTLOADER_DATA'))
        align = otapulse_full_parse_number(d.getVar('MENDER_PARTITION_ALIGNMENT'))
        env_part = (d.getVar('MENDER_UBOOT_CONFIG_SYS_MMC_ENV_PART') or '0').strip()
    except Exception as e:
        bb.fatal("The U-Boot environment layout could not be evaluated (%s). Check "
                 "OTAPULSE_UBOOT_ENV_OFFSET, OTAPULSE_UBOOT_ENV_OFFSET_REDUND and OTAPULSE_UBOOT_ENV_SIZE." % e)
    prob = otapulse_full_env_layout_problem(off1, off2, size)
    if prob:
        bb.fatal(prob[1])
    # An environment in a hardware boot partition (MMC_ENV_PART != 0) is outside the user area: the
    # reserved-space arithmetic only applies to the user area.
    if env_part == '0' and None not in (base, total):
        prob = otapulse_full_env_region_problem(base, off1, off2, size, total, reserved, align)
        if prob:
            bb.fatal(prob[1])

def otapulse_full_boot_counter_defined(cfg, header_text):
    """True when CONFIG_BOOTCOUNT_LIMIT and CONFIG_BOOTCOUNT_ENV are both on: as Kconfig options in the
    parsed .config (CFG) or as "#define"s in a board header, which only the preprocessed configuration
    (u-boot.cfg, HEADER_TEXT) shows. Returns the list of options that are NOT on."""
    import re
    missing = []
    for opt in ('CONFIG_BOOTCOUNT_LIMIT', 'CONFIG_BOOTCOUNT_ENV'):
        if cfg.get(opt) == 'y':
            continue
        if re.search(r'^\s*#\s*define\s+%s\b(?!\s+0\s*$)' % opt, header_text or '', re.M):
            continue
        missing.append(opt)
    return missing

def otapulse_full_dotconfig_problems(text, off1, off2, size, ubi=False, final=True, header_text='', mmc=(None, None)):
    """Problems in a U-Boot .config (text), as [(OTAPULSE variable, message)]. OFF1/OFF2/SIZE are
    the environment layout the agent is configured with (ints). FINAL=False is the early look at
    the board configuration before configuration fragments are merged: only values that
    contradict the agent's layout are reported (a fragment may still add what is missing).
    HEADER_TEXT is the preprocessed configuration (u-boot.cfg), which also shows options that a
    board turns on in its C header; None = not available, then a boot counter that is not in the
    .config cannot be judged and is not reported (the bootloader build itself refuses to compile
    without one). MMC is (device, hardware partition) of the storage the agent is configured for
    (None = unknown): the environment device and partition of U-Boot's configuration must match."""
    import re
    cfg = {}
    for line in text.splitlines():
        m = re.match(r'^(CONFIG_[A-Za-z0-9_]+)=(.*)$', line)
        if m:
            cfg[m.group(1)] = m.group(2).strip().strip('"')
    probs = []
    missing = otapulse_full_boot_counter_defined(cfg, header_text)
    if missing and final and header_text is not None:
        probs.append(('OTAPULSE_UBOOT_AUTO_CONFIGURE',
                      "The U-Boot configuration built for this image does not enable %s. The boot counter "
                      "in the U-Boot environment is what makes the bootloader roll a failed update back; "
                      "without it a bad update can leave the device unbootable. Enable it in your U-Boot "
                      "configuration (a Kconfig option or a define in the board header), or, if the board "
                      "allows it, set OTAPULSE_UBOOT_AUTO_CONFIGURE = \"1\" so OTA-Pulse adds it."
                      % ' and '.join(missing)))
    if ubi:
        return probs
    if final and cfg.get('CONFIG_ENV_IS_IN_MMC') != 'y':
        probs.append(('OTAPULSE_UBOOT_AUTO_CONFIGURE',
                      "The U-Boot configuration built for this image does not keep its environment on the "
                      "MMC/SD storage (CONFIG_ENV_IS_IN_MMC is not set), but the agent reads and writes the "
                      "environment on the storage device. Move your U-Boot environment to the MMC/SD "
                      "storage or, if the board allows it, set OTAPULSE_UBOOT_AUTO_CONFIGURE = \"1\" so "
                      "OTA-Pulse configures U-Boot for it."))
        return probs
    if final and (cfg.get('CONFIG_SYS_REDUNDAND_ENVIRONMENT') != 'y' or 'CONFIG_ENV_OFFSET_REDUND' not in cfg):
        probs.append(('OTAPULSE_UBOOT_ENV_OFFSET_REDUND',
                      "The U-Boot environment in the U-Boot configuration built for this image is not "
                      "redundant (CONFIG_SYS_REDUNDAND_ENVIRONMENT / CONFIG_ENV_OFFSET_REDUND). A power cut "
                      "while the environment is written would leave no valid copy. Make your U-Boot "
                      "environment redundant (OTAPULSE_UBOOT_ENV_OFFSET_REDUND is the offset of the second "
                      "copy) or, if the board allows it, set OTAPULSE_UBOOT_AUTO_CONFIGURE = \"1\"."))
    mmc_dev, mmc_part = mmc
    for opt, want, what in (('CONFIG_SYS_MMC_ENV_DEV', mmc_dev, 'MMC device'),
                            ('CONFIG_SYS_MMC_ENV_PART', mmc_part, 'MMC hardware partition (0 = the user area)')):
        got = otapulse_full_parse_number(cfg.get(opt))
        if want is not None and got is not None and got != want:
            probs.append(('OTAPULSE_STORAGE_DEVICE',
                          "U-Boot's configuration keeps its environment on %s %d (%s), but the agent is configured "
                          "for %s %d. A mismatch makes the agent read and write a different place than the one "
                          "U-Boot uses and breaks the update rollback. Set OTAPULSE_STORAGE_DEVICE to the storage "
                          "device your U-Boot uses (for example /dev/mmcblk%d), or change your U-Boot "
                          "configuration." % (what, got, opt, what, want, got if opt.endswith('DEV') else want)))
    for opt, want, var in (('CONFIG_ENV_OFFSET', off1, 'OTAPULSE_UBOOT_ENV_OFFSET'),
                           ('CONFIG_ENV_OFFSET_REDUND', off2, 'OTAPULSE_UBOOT_ENV_OFFSET_REDUND'),
                           ('CONFIG_ENV_SIZE', size, 'OTAPULSE_UBOOT_ENV_SIZE')):
        if opt not in cfg:
            continue
        got = otapulse_full_parse_number(cfg[opt])
        if got is not None and want is not None and got != want:
            probs.append((var,
                          "U-Boot's configuration has %s = 0x%x, but the agent is configured to use 0x%x "
                          "(its fw_env.config). A mismatch makes the agent read and write the wrong place "
                          "in the storage and breaks the update rollback. Set %s = \"0x%x\" to the value "
                          "your U-Boot uses, or remove the setting from your U-Boot configuration."
                          % (opt, got, want, var, got)))
    return probs

def otapulse_full_expected_mmc(d):
    """(MMC device number, hardware partition) the agent is configured for, from the same variables
    the board support derives U-Boot's expectations from; None where unknown."""
    import re
    dev = otapulse_full_parse_number(d.getVar('MENDER_UBOOT_STORAGE_DEVICE'))
    if dev is None:
        m = re.match(r'^/dev/mmcblk(\d+)', (d.getVar('MENDER_STORAGE_DEVICE') or '').strip())
        dev = int(m.group(1)) if m else None
    part = otapulse_full_parse_number(d.getVar('MENDER_UBOOT_CONFIG_SYS_MMC_ENV_PART') or '0')
    return (dev, part)

def otapulse_full_uboot_config_dirs(b, uboot_machine, uboot_config):
    """The directories that hold U-Boot configurations (.config) in build directory B. A multi-configuration
    board (UBOOT_CONFIG set, one build per UBOOT_MACHINE entry) keeps each in B/<entry>; B/.config does not
    exist for it, or is not a configuration that is built."""
    import os
    if (uboot_config or '').strip():
        return [os.path.join(b, m) for m in (uboot_machine or '').split()]
    return [b]

# The board configuration as .config (the step the board support itself takes before it compares
# anything) so it can be looked at ahead of the board support's own English-language comparison.
# A multi-configuration board (UBOOT_CONFIG) has no single configuration at this point: skipped.
otapulse_full_uboot_defconfig() {
    if [ -z "${UBOOT_CONFIG}" ] && [ ! -e ${B}/.config ]; then
        oe_runmake -C ${S} O=${B} ${UBOOT_MACHINE}
    fi
}

# The preprocessed configuration: shows the options a board enables in its C header (the Kconfig
# .config does not). Best effort: the output is kept in otapulse-u-boot-cfg.log; when no u-boot.cfg
# comes out, the log tells why (the board support's header refuses to preprocess without a boot
# counter, which is itself the answer). Anything else leaves the boot counter to the bootloader build.
otapulse_full_uboot_cfg() {
    if [ -n "${UBOOT_CONFIG}" ]; then
        for c in ${UBOOT_MACHINE}; do
            if [ -e ${B}/$c/.config ]; then
                oe_runmake_call -C ${S} O=${B}/$c u-boot.cfg > ${B}/$c/otapulse-u-boot-cfg.log 2>&1 || true
            fi
        done
    else
        oe_runmake_call -C ${S} O=${B} u-boot.cfg > ${B}/otapulse-u-boot-cfg.log 2>&1 || true
    fi
    return 0
}

def otapulse_full_header_text(cfg_text, log_text):
    """The preprocessed U-Boot configuration to judge the boot counter by: u-boot.cfg when it was
    generated; '' when it could not be generated BECAUSE the board support's header found no boot
    counter (its "#error CONFIG_BOOTCOUNT_... is required" - then nothing is defined); None when it is
    not available for any other reason (the boot counter is then not judged here)."""
    import re
    if cfg_text is not None:
        return cfg_text
    if re.search(r'#error\s+CONFIG_BOOTCOUNT_(LIMIT|ENV)\s+is required', log_text or ''):
        return ''
    return None

python otapulse_full_check_uboot_config_early() {
    """Before do_configure: the board configuration against the environment layout the agent is
    configured with. A value that contradicts it is reported in OTA-Pulse terms here, ahead of the
    board support's own comparison."""
    import os
    if (d.getVar('UBOOT_CONFIG') or '').strip():
        return
    path = os.path.join(d.getVar('B'), '.config')
    if not os.path.isfile(path):
        return
    off1 = otapulse_full_parse_number(d.getVar('MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1'))
    off2 = otapulse_full_parse_number(d.getVar('MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2'))
    size = otapulse_full_parse_number(d.getVar('BOOTENV_SIZE'))
    ubi = bb.utils.contains('MENDER_FEATURES', 'mender-ubi', True, False, d)
    with open(path, errors='replace') as f:
        probs = otapulse_full_dotconfig_problems(f.read(), off1, off2, size, ubi, final=False,
                                                 mmc=otapulse_full_expected_mmc(d))
    if probs:
        bb.fatal("%s: %s" % (probs[0][0], probs[0][1]))
}

python otapulse_full_check_uboot_config() {
    """After do_configure: the final U-Boot configuration (board configuration, auto-configuration and
    configuration fragments merged, plus the options the board header turns on) against the
    environment layout the agent is configured with."""
    import os
    dirs = otapulse_full_uboot_config_dirs(d.getVar('B'), d.getVar('UBOOT_MACHINE'), d.getVar('UBOOT_CONFIG'))
    found = [x for x in dirs if os.path.isfile(os.path.join(x, '.config'))]
    if not found:
        bb.fatal("OTAPULSE_UBOOT_AUTO_CONFIGURE: no U-Boot configuration (.config) was generated under %s, so "
                 "the boot counter and the redundant environment cannot be verified." % d.getVar('B'))
    off1 = otapulse_full_parse_number(d.getVar('MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1'))
    off2 = otapulse_full_parse_number(d.getVar('MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2'))
    size = otapulse_full_parse_number(d.getVar('BOOTENV_SIZE'))
    ubi = bb.utils.contains('MENDER_FEATURES', 'mender-ubi', True, False, d)
    for x in found:
        cfg_text = log_text = None
        if os.path.isfile(os.path.join(x, 'u-boot.cfg')):
            with open(os.path.join(x, 'u-boot.cfg'), errors='replace') as f:
                cfg_text = f.read()
        if os.path.isfile(os.path.join(x, 'otapulse-u-boot-cfg.log')):
            with open(os.path.join(x, 'otapulse-u-boot-cfg.log'), errors='replace') as f:
                log_text = f.read()
        header = otapulse_full_header_text(cfg_text, log_text)
        if header is None:
            bb.warn("otapulse-full: no preprocessed U-Boot configuration (u-boot.cfg) in %s; a boot counter "
                    "enabled in a board header cannot be seen, the bootloader build checks it itself" % x)
        with open(os.path.join(x, '.config'), errors='replace') as f:
            probs = otapulse_full_dotconfig_problems(f.read(), off1, off2, size, ubi, header_text=header,
                                                     mmc=otapulse_full_expected_mmc(d))
        if probs:
            bb.fatal("%s: %s" % (probs[0][0], probs[0][1]))
    bb.note("otapulse-full: U-Boot configuration carries the boot counter and a redundant environment "
            "(offsets 0x%x / 0x%x, size 0x%x)" % (off1 or 0, off2 or 0, size or 0))
}

# ---- U-Boot compiled-in environment: the kernel command line that U-Boot really passes ----
def otapulse_full_uboot_env_dict(text):
    """name=value lines of a u-boot-initial-env file."""
    env = {}
    for line in (text or '').splitlines():
        if '=' not in line or line.startswith('#'):
            continue
        k, _, v = line.partition('=')
        env[k.strip()] = v
    return env

def otapulse_full_maxargs(config_text):
    """CONFIG_SYS_MAXARGS of the U-Boot build, from the text of its .config or u-boot.cfg ("CONFIG_SYS_MAXARGS=16" or
    "#define CONFIG_SYS_MAXARGS 16"); 16 (U-Boot's default) when the text does not say, or says something unusable."""
    import re
    m = re.search(r'^(?:CONFIG_SYS_MAXARGS=|#define\s+CONFIG_SYS_MAXARGS\s+)(\d+)\s*$', config_text or '', re.M)
    if m and int(m.group(1)) >= 2:
        return int(m.group(1))
    return 16

def otapulse_full_uboot_statements(text, lookup, maxargs=16):
    """Generator over the statements of a U-Boot shell script, each a list of words. Quotes and
    backslash escapes are honoured; ';' outside quotes ends a statement; $name / ${name} are
    expanded through lookup() when the statement is read (so earlier statements have run).
    An UNQUOTED expansion is split into words like U-Boot's shell does, and a statement with more
    than CONFIG_SYS_MAXARGS (MAXARGS, default 16, the command name counts) words is refused by U-Boot: it is returned as ['__too_many_args__']
    (a no-op for the callers), so that for example 'setenv bootargs ${bootargs} panic=7' with a long
    bootargs is seen to do nothing, exactly as it does on the board."""
    import re
    words, cur, have, quote = [], '', False, None
    def emit(w):
        return ['__too_many_args__'] if len(w) > maxargs else w
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        if quote == "'":
            if c == "'":
                quote = None
            else:
                cur += c
            i += 1
            continue
        if quote == '"' and text.startswith('\\\\"', i):
            # the board integration's "expand_bootargs" idiom: inside a double-quoted word the
            # three characters  \\"  (as stored in the environment) are a literal quote
            cur += '"'
            have = True
            i += 3
            continue
        if c == '\\' and i + 1 < n:
            cur += text[i + 1]
            have = True
            i += 2
            continue
        if quote == '"':
            if c == '"':
                quote = None
                i += 1
                continue
        else:
            if c in '"\'':
                quote = c
                have = True
                i += 1
                continue
            if c in ' \t\r\n':
                if have:
                    words.append(cur)
                    cur, have = '', False
                i += 1
                continue
            if c == ';' or (c in '&|' and text.startswith(c + c, i)):
                # ';' and the && / || list operators all end a statement: both sides are run, in order
                # (the pessimistic reading: a replacement of bootargs on either side counts)
                if have:
                    words.append(cur)
                    cur, have = '', False
                if words:
                    yield emit(words)
                    words = []
                i += 1 if c == ';' else 2
                continue
        if c == '$':
            m = re.match(r'\$(?:\{([A-Za-z0-9_]+)\}|([A-Za-z_][A-Za-z0-9_]*))', text[i:])
            if m:
                val = lookup(m.group(1) or m.group(2))
                if quote == '"':
                    cur += val
                    have = True
                elif val:
                    for k, fld in enumerate(re.split(r"\s+", val)):
                        if k > 0 and have:
                            words.append(cur)
                            cur, have = '', False
                        if fld:
                            cur += fld
                            have = True
                i += m.end()
                continue
        cur += c
        have = True
        i += 1
    if have:
        words.append(cur)
    if words:
        yield emit(words)

def otapulse_full_uboot_boot_trace(env, entry, fdt_bootargs=None, maxargs=16):
    """Runs the environment command ENTRY (for example 'bootcmd') the way U-Boot would and returns
    {'boots': [...], 'external': bool}: the kernel command line (bootargs) at EVERY boot command that
    the run reaches, and whether the flow leaves the environment (a boot script, a distro boot
    sequence, extlinux/PXE) so that the command line is not decided here. Conditions are not
    evaluated: every branch of an if runs, in order, so a branch that replaces the command line
    shows up as its own entry and the result is the pessimistic one.
    FDT_BOOTARGS (optional): the text the firmware puts into /chosen/bootargs (the Raspberry Pi
    firmware builds it from cmdline.txt); "fdt get value bootargs /chosen bootargs" in a boot script
    then loads it into bootargs, like U-Boot does."""
    boot_cmds = ('boot', 'bootd', 'bootm', 'bootz', 'booti', 'bootelf', 'bootvx', 'go')
    # 'bootflow' is U-Boot's standard boot (bootflow scan: boot.scr / extlinux found on the boot media);
    # the Raspberry Pi U-Boot's bootcmd is exactly 'bootflow scan'.
    external_cmds = ('source', 'sysboot', 'pxe', 'bootefi', 'bootflow')
    local = dict(env)
    state = {'bootargs': env.get('bootargs', ''), 'boots': [], 'external': False, 'steps': 0}

    def lookup(name):
        if name == 'bootargs':
            return state['bootargs']
        return local.get(name, '')

    def run(script, depth):
        if depth > 24 or state['steps'] > 5000:
            return
        for words in otapulse_full_uboot_statements(script, lookup, maxargs):
            state['steps'] += 1
            while words and words[0] in ('then', 'else', 'do'):
                words = words[1:]
            if not words or words[0] in ('fi', 'if', 'elif', 'while', 'for', 'done'):
                continue
            cmd = words[0]
            if cmd == 'run':
                for name in words[1:]:
                    if name.startswith('distro_bootcmd') or name.startswith('bootcmd_') or name.startswith('boot_a_script'):
                        state['external'] = True
                    run(local.get(name, ''), depth + 1)
            elif (cmd == 'setenv' or (cmd == 'env' and words[1:2] == ['set'])) and len(words) >= (2 if cmd == 'setenv' else 3):
                if cmd == 'env':
                    words = words[1:]  # "env set NAME VALUE" is "setenv NAME VALUE"
                if words[1] == '-f' and len(words) > 2:
                    words = words[:1] + words[2:]  # force flag
                value = ' '.join(words[2:])
                if words[1] == 'bootargs':
                    state['bootargs'] = value
                elif len(words) == 2:
                    local.pop(words[1], None)
                else:
                    local[words[1]] = value
            elif cmd == 'fdt' and words[1:5] == ['get', 'value', 'bootargs', '/chosen'] and fdt_bootargs is not None:
                state['bootargs'] = fdt_bootargs
            elif cmd in boot_cmds:
                state['boots'].append(state['bootargs'])
            elif cmd in external_cmds:
                state['external'] = True

    run(env.get(entry, ''), 0)
    return {'boots': state['boots'], 'external': state['external']}

def otapulse_full_uboot_env_problems(env, maxargs=16):
    """Problems in the compiled-in U-Boot environment (name -> value), as [(OTAPULSE variable,
    message)]. In bootloader mode the board support always injects its boot-counter / rollback
    environment, so bootlimit, upgrade_available and altbootcmd are required for every board; an
    environment without them means the integration is missing. A board whose boot flow leaves the
    environment (a boot script / distro boot sequence) decides its kernel command line there: only
    the panic= trace is skipped for it (the setup-commands check at parse time still applies)."""
    probs = []
    traces = {}
    for entry in ('bootcmd', 'altbootcmd'):
        if env.get(entry, '').strip():
            traces[entry] = otapulse_full_uboot_boot_trace(env, entry, maxargs=maxargs)
    limit = otapulse_full_parse_number(env.get('bootlimit'))
    if limit is None or limit < 1:
        probs.append(('OTAPULSE_UBOOT_AUTO_CONFIGURE',
                      "The compiled-in U-Boot environment has no positive bootlimit, so the bootloader never "
                      "decides that an update failed. Enable the boot counter in your U-Boot (or, if the "
                      "board allows it, set OTAPULSE_UBOOT_AUTO_CONFIGURE = \"1\")."))
    if 'upgrade_available' not in env or not env.get('altbootcmd', '').strip():
        probs.append(('OTAPULSE_UBOOT_AUTO_CONFIGURE',
                      "The compiled-in U-Boot environment has no upgrade_available flag or no altbootcmd "
                      "(the rollback command). Add them to your U-Boot environment (or, if the board "
                      "allows it, set OTAPULSE_UBOOT_AUTO_CONFIGURE = \"1\")."))
    if not env.get('bootcmd', '').strip():
        probs.append(('OTAPULSE_PANIC_TIMEOUT_S',
                      "The compiled-in U-Boot environment has no bootcmd, so the kernel command line it "
                      "passes (and its panic timeout) cannot be verified. Keep OTAPULSE_PANIC_TIMEOUT_S set "
                      "and give your U-Boot a boot command."))
    for entry, tr in traces.items():
        if tr['external']:
            continue
        if not tr['boots']:
            probs.append(('OTAPULSE_PANIC_TIMEOUT_S',
                          "The compiled-in U-Boot command %s never reaches a boot command, so the kernel "
                          "command line it passes (and its panic timeout) cannot be verified. Keep "
                          "OTAPULSE_PANIC_TIMEOUT_S set and boot the kernel from this command." % entry))
            continue
        for n, args in enumerate(tr['boots'], 1):
            panic = otapulse_full_last_panic(args)
            if panic is None or panic < 1:
                probs.append(('OTAPULSE_PANIC_TIMEOUT_S',
                              "The kernel command line that U-Boot passes when it runs %s (boot command %d) %s. "
                              "Bootloader mode requires a positive kernel panic timeout on the kernel command "
                              "line: a hung kernel would never reset and a failed update would never be rolled "
                              "back. Set OTAPULSE_PANIC_TIMEOUT_S to a positive number of seconds and do not "
                              "replace or reset the kernel command line (bootargs) in your own U-Boot commands."
                              % (entry, n, ('has panic=%d' % panic) if panic is not None else 'does not contain panic=')))
                break
    return probs

# ---- boot script (distro/standard boot boards, for example the Raspberry Pi) ----------------
# A board whose U-Boot boots through a boot script (bootcmd = "bootflow scan" / "run distro_bootcmd")
# decides its kernel command line there, not in the environment: the checks above can only trust
# the setup commands. The finished boot partition holds the boot script (boot.scr) and the firmware's
# command line (cmdline.txt); this reads them and runs the script the way U-Boot would.
def otapulse_full_boot_script_text(data):
    """Text of a U-Boot boot script. BYTES are either a legacy uImage of type "script" (64 byte header,
    then two 32 bit words, then the script) or plain text (a boot.cmd). Comment lines are dropped
    and every line ends a statement (';'), like the shell of U-Boot reads a script."""
    import struct
    if isinstance(data, str):
        raw = data.encode('utf-8', 'replace')
    else:
        raw = data
    if len(raw) >= 72 and struct.unpack('>I', raw[:4])[0] == 0x27051956:
        size = struct.unpack('>I', raw[12:16])[0]
        body = raw[64:64 + size]
        # a script image starts with the (length, 0) table of its sub-images
        raw = body[8:] if len(body) > 8 else b''
    text = raw.decode('utf-8', 'replace').replace('\x00', '')
    lines = []
    for ln in text.splitlines():
        t = ln.strip()
        if not t or t.startswith('#'):
            continue
        lines.append(t)
    return '; '.join(lines)

def otapulse_full_boot_script_problems(script_data, env, cmdline_txt, maxargs=16):
    """Problems of a board that boots through a boot script, as [(OTAPULSE variable, message)]:
    the script must run the OTA-Pulse boot-slot setup, reach a boot command, and the kernel command
    line it really hands over (the firmware's cmdline.txt, then the setup commands) must carry a
    positive panic= value and a root device that the boot-slot setup chooses. ENV is the compiled-in
    U-Boot environment (name -> value), CMDLINE_TXT the text of cmdline.txt."""
    probs = []
    script = otapulse_full_boot_script_text(script_data)
    cmdline = ' '.join((cmdline_txt or '').split())
    if not script:
        return [('OTAPULSE_BOOT_MODE',
                 "The boot script of the image (boot.scr) is empty or unreadable, so the kernel command line "
                 "and the boot slot cannot be verified.")]
    env2 = dict(env)
    env2['__boot_script'] = script
    tr = otapulse_full_uboot_boot_trace(env2, '__boot_script', fdt_bootargs=cmdline, maxargs=maxargs)
    if 'mender_setup' not in script or 'run mender_setup' not in script.replace(';', ' ; '):
        probs.append(('OTAPULSE_BOOT_MODE',
                      "The image's boot script (boot.scr) does not run the OTA-Pulse boot-slot setup, so the "
                      "bootloader never chooses the root file system from its boot-slot flag and a failed update "
                      "is never rolled back. The boot script of the board support is replaced by one without it "
                      "(the stock board script?). Keep the board support's boot script: "
                      "OTAPULSE_BOOT_MODE = \"bootloader\" together with the layers of meta-otapulse-bsp."))
        return probs
    if not tr['boots']:
        probs.append(('OTAPULSE_PANIC_TIMEOUT_S',
                      "The image's boot script never reaches a boot command, so the kernel command line it "
                      "passes (and its panic timeout) cannot be verified."))
        return probs
    for n, args in enumerate(tr['boots'], 1):
        panic = otapulse_full_last_panic(args)
        if panic is None or panic < 1:
            probs.append(('OTAPULSE_PANIC_TIMEOUT_S',
                          "The kernel command line that the image's boot script hands to the kernel (boot "
                          "command %d) %s: %s. Bootloader mode requires a positive kernel panic timeout: a hung "
                          "kernel would never reset and a failed update would never be rolled back. Keep "
                          "OTAPULSE_PANIC_TIMEOUT_S set and do not replace the kernel command line in your "
                          "boot script or in cmdline.txt."
                          % (n, ('has panic=%d' % panic) if panic is not None else 'does not contain panic=', args.strip())))
            break
    # The root device must come from the boot-slot setup (a reference that is resolved while the
    # script runs), never from a fixed root= in cmdline.txt: a fixed one ignores the A/B switch.
    import re
    fixed = re.findall(r'(?<![\w-])root=(/dev/\S+|PARTUUID=\S+|UUID=\S+|PARTLABEL=\S+)', cmdline)
    if fixed and not re.search(r'(?<![\w-])root=\$\{?mender_kernel_root\}?', cmdline):
        probs.append(('OTAPULSE_BOOT_MODE',
                      "The firmware command line (cmdline.txt) has a fixed root=%s, so the bootloader cannot "
                      "switch the root file system between the two update slots: an update would never take "
                      "effect and a failed one would never be rolled back. In bootloader mode root= must come "
                      "from the boot-slot setup (the board support's cmdline.txt does that)." % fixed[0]))
    return probs

def otapulse_full_rpi_firmware_bootargs():
    """What the Raspberry Pi firmware (VideoCore) puts into /chosen/bootargs IN FRONT OF cmdline.txt before U-Boot sees
    it (12 words, as observed on a Raspberry Pi 4; the MAC is a placeholder, only the word count matters). The boot script reads this with 'fdt get value bootargs', so a command
    that appends to it is judged against the real, long command line, not against the short cmdline.txt alone."""
    return ("coherent_pool=1M 8250.nr_uarts=1 snd_bcm2835.enable_headphones=0 cgroup_disable=memory "
            "numa_policy=interleave snd_bcm2835.enable_hdmi=0 video=HDMI-A-1:640x480M@60D numa=fake=2 "
            "system_heap.max_order=0 smsc95xx.macaddr=00:00:00:00:00:00 vc_mem.mem_base=0x3ec00000 "
            "vc_mem.mem_size=0x40000000")

def otapulse_full_find_deployed_uboot_env(deploy_dir):
    """The compiled-in U-Boot environment in the image deploy directory: (path, None), or (None, reason).
    A single-configuration U-Boot deploys "u-boot-initial-env". A multi-configuration one (UBOOT_CONFIG, for
    example the NXP i.MX8M Plus FRDM) deploys "u-boot-<recipe>-initial-env-<config>" (plus machine/version-stamped
    copies of it) instead; those are all copies of ONE environment, and are only accepted when they agree."""
    import glob, os
    exact = os.path.join(deploy_dir, 'u-boot-initial-env')
    if os.path.isfile(exact):
        return exact, None
    found = {}
    for path in sorted(glob.glob(os.path.join(deploy_dir, 'u-boot-*initial-env*'))):
        if os.path.isfile(path):
            with open(path, errors='replace') as f:
                found.setdefault(f.read(), path)
    if not found:
        return None, "was not found"
    if len(found) > 1:
        return None, "exists in %d different versions (%s)" % (len(found), ', '.join(os.path.basename(p) for p in sorted(found.values())))
    return list(found.values())[0], None

python otapulse_bl_bootfs_checks() {
    """After the disk image is built: the boot script and cmdline.txt that went into the boot partition
    of a board that boots through a boot script (the Raspberry Pi), run the way U-Boot would."""
    import os
    w = os.path.join(d.getVar('WORKDIR'), 'bootfs')
    script = os.path.join(w, 'boot.scr')
    if not os.path.isfile(script):
        bb.note("otapulse-full: no boot script in the boot partition (the boot command is compiled into U-Boot): "
                "boot script checks skipped")
        return
    envf, why = otapulse_full_find_deployed_uboot_env(d.getVar('DEPLOY_DIR_IMAGE'))
    if not envf:
        bb.fatal("OTAPULSE_PANIC_TIMEOUT_S: the compiled-in U-Boot environment (u-boot-initial-env) %s "
                 "in %s, so the boot script of the image cannot be verified." % (why, d.getVar('DEPLOY_DIR_IMAGE')))
    with open(envf, errors='replace') as f:
        env = otapulse_full_uboot_env_dict(f.read())
    cmdline = ''
    cf = os.path.join(w, 'cmdline.txt')
    if os.path.isfile(cf):
        with open(cf, errors='replace') as f:
            cmdline = f.read()
    if os.path.isfile(os.path.join(w, 'config.txt')):
        # a Raspberry Pi boot partition: the firmware prepends its own parameters to cmdline.txt
        cmdline = otapulse_full_rpi_firmware_bootargs() + ' ' + cmdline
    with open(script, 'rb') as f:
        data = f.read()
    # (CONFIG_SYS_MAXARGS is read from the U-Boot configuration where the build dir exists, see otapulse_full_check_uboot_env;
    #  at image time only the default 16 is available)
    probs = otapulse_full_boot_script_problems(data, env, cmdline)
    if probs:
        bb.fatal("%s: %s" % (probs[0][0], probs[0][1]))
    tr = otapulse_full_uboot_boot_trace(dict(env, __boot_script=otapulse_full_boot_script_text(data)),
                                        '__boot_script', fdt_bootargs=' '.join(cmdline.split()))
    bb.note("otapulse-full: the image's boot script runs the boot-slot setup and hands the kernel panic=%s"
            % otapulse_full_last_panic(tr['boots'][0]))
}

python otapulse_full_check_uboot_env() {
    """After do_compile: the compiled-in U-Boot environment (u-boot-initial-env) is the effective
    default environment of the finished bootloader."""
    import glob, os
    b = d.getVar('B')
    paths = sorted(glob.glob(os.path.join(b, 'u-boot-initial-env')) +
                   glob.glob(os.path.join(b, '*', 'u-boot-initial-env')))
    if not paths:
        bb.fatal("OTAPULSE_PANIC_TIMEOUT_S: the compiled-in U-Boot environment (u-boot-initial-env) was not "
                 "generated under %s, so the kernel panic timeout U-Boot passes cannot be verified." % b)
    expected = (d.getVar('OTAPULSE_PANIC_TIMEOUT_S') or '').strip()
    cfg_text = ''
    for cand in sorted(glob.glob(os.path.join(b, '.config')) + glob.glob(os.path.join(b, '*', '.config')) +
                       glob.glob(os.path.join(b, 'u-boot.cfg')) + glob.glob(os.path.join(b, '*', 'u-boot.cfg'))):
        with open(cand, errors='replace') as f:
            cfg_text += f.read() + '\n'
    maxargs = otapulse_full_maxargs(cfg_text)
    for path in paths:
        with open(path, errors='replace') as f:
            env = otapulse_full_uboot_env_dict(f.read())
        probs = otapulse_full_uboot_env_problems(env, maxargs)
        if probs:
            bb.fatal("%s: %s" % (probs[0][0], probs[0][1]))
        tr = otapulse_full_uboot_boot_trace(env, 'bootcmd', maxargs=maxargs)
        if tr['external']:
            bb.note("otapulse-full: this board boots through a boot script; the kernel command line is decided "
                    "there, only the setup-commands panic timeout is verified (%s)" % path)
            continue
        panic = otapulse_full_last_panic(tr['boots'][0])
        if expected.isdigit() and panic != int(expected):
            bb.warn("OTAPULSE_PANIC_TIMEOUT_S = %s but the kernel panic timeout U-Boot passes is %s" % (expected, panic))
        bb.note("otapulse-full: compiled-in U-Boot environment hands the kernel panic=%s (bootlimit %s)"
                % (panic, env.get('bootlimit')))
}

# ---- GRUB debug options ------------------------------------------------------------
def otapulse_full_check_grub_debug(d):
    """Recipe parse of the GRUB boot configuration: the board support's debug options stop an
    unattended device at a GRUB prompt or pause, so a device that fails an update never
    reaches the rollback. Refused unless OTAPULSE_ALLOW_GRUB_DEBUG = "1" (then a warning)."""
    pc = (d.getVar('PACKAGECONFIG') or '').split()
    bad = [f for f in ('force-grub-prompt', 'debug-pause') if f in pc]
    if not bad:
        return
    if (d.getVar('OTAPULSE_ALLOW_GRUB_DEBUG') or '0').strip() == '1':
        bb.warn("OTAPULSE_ALLOW_GRUB_DEBUG = \"1\": the GRUB boot configuration is built with %s. "
                "This image stops at a GRUB prompt or pause and must NEVER be shipped." % ' and '.join(bad))
        return
    bb.fatal("The GRUB boot configuration is built with the debug option %s (PACKAGECONFIG of "
             "grub-mender-grubenv). A device with it stops at a GRUB prompt or pause instead of booting "
             "unattended, so a failed update is never rolled back. Remove it from PACKAGECONFIG. For a "
             "developer-only debug image set OTAPULSE_ALLOW_GRUB_DEBUG = \"1\"; never ship such an image."
             % ' and '.join(bad))

# ---- the state-script gate and /etc/otapulse/boot_mode ------------------------------
def otapulse_full_script_gate_problem(path):
    """None when the state script carries the boot-mode gate block (TASK-S109-010), else why not."""
    try:
        with open(path, errors='replace') as f:
            text = f.read()
    except OSError as e:
        return "cannot be read (%s)" % e
    b = text.find('OTAPULSE-BOOT-MODE-GATE BEGIN')
    e = text.find('OTAPULSE-BOOT-MODE-GATE END')
    if b < 0 or e < 0 or e < b:
        return "does not carry the OTAPULSE-BOOT-MODE-GATE block"
    if '/etc/otapulse/boot_mode' not in text[b:e]:
        return "has an OTAPULSE-BOOT-MODE-GATE block that does not read /etc/otapulse/boot_mode"
    return None

def otapulse_full_check_agent_recipe(d):
    """Recipe parse of the agent recipe: every Artifact*_01 state script it ships (the files that
    are really found on FILESPATH, so a bbappend that replaces one is checked too) carries the
    gate. Without it the script would switch the boot slot behind the bootloader's back."""
    import re
    names = re.findall(r'file://(Artifact\w*_[0-9]+)\b', d.getVar('SRC_URI') or '')
    if not names:
        bb.fatal("OTAPULSE_BOOT_MODE = \"bootloader\": the agent recipe (soc-ota-agent) ships no "
                 "Artifact*_01 state scripts. Update your meta-otapulse layer.")
    bad = []
    for name in names:
        path = bb.utils.which(d.getVar('FILESPATH') or '', name)
        if not path:
            bad.append("%s was not found" % name)
            continue
        why = otapulse_full_script_gate_problem(path)
        if why:
            bad.append("%s (%s) %s" % (name, path, why))
    if bad:
        bb.fatal("OTAPULSE_BOOT_MODE = \"bootloader\": the state scripts of the agent recipe are not safe "
                 "for bootloader mode: %s. These scripts must stop on /etc/otapulse/boot_mode, otherwise "
                 "they switch the boot slot behind the bootloader's back. Your meta-otapulse layer is out of "
                 "date or overridden: update it." % '; '.join(bad))

def otapulse_full_fw_env_problem(text):
    """None when fw_env.config text describes a valid redundant environment, else
    (OTAPULSE variable, message)."""
    rows = [l.split() for l in (text or '').splitlines() if l.strip() and not l.lstrip().startswith('#')]
    if len(rows) < 2:
        return ('OTAPULSE_UBOOT_ENV_OFFSET_REDUND',
                "The image's U-Boot environment description (/data/u-boot/fw_env.config, linked from /etc/fw_env.config) "
                "describes %d environment copy, not two. Set "
                "OTAPULSE_UBOOT_ENV_OFFSET_REDUND so the agent can use a redundant environment." % len(rows))
    vals = []
    for r in rows[:2]:
        vals.append((otapulse_full_parse_number(r[1]) if len(r) > 1 else None,
                     otapulse_full_parse_number(r[2]) if len(r) > 2 else None))
    (o1, s1), (o2, s2) = vals
    if s1 != s2:
        return ('OTAPULSE_UBOOT_ENV_SIZE',
                "The two environment copies in the image's /data/u-boot/fw_env.config have different sizes. Set "
                "OTAPULSE_UBOOT_ENV_SIZE.")
    return otapulse_full_env_layout_problem(o1, o2, s1)

def otapulse_full_rootfs_problems(rootfs, bootloader):
    """Checks of the finished root file system, as [(OTAPULSE variable, message)]."""
    import glob, os
    probs = []
    bm = os.path.join(rootfs, 'etc/otapulse/boot_mode')
    try:
        with open(bm) as f:
            mode = f.read().strip()
    except OSError:
        mode = None
    if mode != 'bootloader':
        probs.append(('OTAPULSE_BOOT_MODE',
                      "The image does not contain /etc/otapulse/boot_mode = bootloader (%s). Without it the "
                      "agent and the state scripts behave as on a native image and switch the boot slot "
                      "themselves instead of through the bootloader. Install the OTA-Pulse agent "
                      "(soc-ota-agent) in the image, built with OTAPULSE_BOOT_MODE = \"bootloader\" in "
                      "local.conf." % ('missing' if mode is None else 'found "%s"' % mode)))
    sdir = os.path.join(rootfs, 'usr/share/otapulse/state-scripts')
    found = sorted(glob.glob(os.path.join(sdir, 'Artifact*_01')))
    have = [os.path.basename(p) for p in found]
    for need in ('ArtifactReboot_Enter_01', 'ArtifactRollbackReboot_Enter_01', 'ArtifactCommit_Enter_01'):
        if need not in have:
            probs.append(('OTAPULSE_BOOT_MODE',
                          "The image does not contain the state script %s under /usr/share/otapulse/"
                          "state-scripts, which every OTA-Pulse update artifact needs. Install the OTA-Pulse "
                          "agent (soc-ota-agent) in the image." % need))
    for p in found:
        why = otapulse_full_script_gate_problem(p)
        if why:
            probs.append(('OTAPULSE_BOOT_MODE',
                          "The image's state script %s %s. In bootloader mode it would switch the boot slot "
                          "behind the bootloader's back. Rebuild the agent with a meta-otapulse layer that "
                          "has the bootloader-mode state scripts (update the layer)." % (os.path.basename(p), why)))
    if bootloader == 'uboot':
        fw = os.path.join(rootfs, 'data/u-boot/fw_env.config')
        try:
            with open(fw) as f:
                prob = otapulse_full_fw_env_problem(f.read())
        except OSError:
            prob = ('OTAPULSE_UBOOT_ENV_OFFSET',
                    "The image does not contain /data/u-boot/fw_env.config, so the agent cannot find the "
                    "U-Boot environment. Check OTAPULSE_BOOTLOADER = \"uboot\" and OTAPULSE_UBOOT_ENV_OFFSET.")
        if prob:
            probs.append(prob)
    return probs

def otapulse_full_image_wants_agent(rootfs, install_vars):
    """True when the image is one that carries the OTA-Pulse agent: it lists soc-ota-agent among the
    packages it installs, or the agent binary is in the root file system. Other images of a
    bootloader-mode build (an initramfs, a minimal test image) are not checked."""
    import os
    for v in install_vars:
        if 'soc-ota-agent' in (v or '').split():
            return True
    return os.path.exists(os.path.join(rootfs, 'usr/bin/soc-ota-agent'))

python otapulse_bl_rootfs_checks() {
    """End of the image build: what the finished root file system really contains."""
    rootfs = d.getVar('IMAGE_ROOTFS')
    if not otapulse_full_image_wants_agent(rootfs, (d.getVar('IMAGE_INSTALL'), d.getVar('PACKAGE_INSTALL'))):
        bb.note("otapulse-full: this image does not install the OTA-Pulse agent: boot mode checks skipped")
        return
    probs = otapulse_full_rootfs_problems(rootfs, d.getVar('OTAPULSE_BOOTLOADER'))
    if probs:
        bb.fatal("%s: %s" % (probs[0][0], probs[0][1]))
    bb.note("otapulse-full: root file system carries boot_mode, the gated state scripts and fw_env.config")
}
ROOTFS_POSTPROCESS_COMMAND += "otapulse_bl_rootfs_checks;"
