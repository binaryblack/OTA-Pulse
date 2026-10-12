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
#  6. adds a kernel panic timeout (panic=N) to the kernel command line through the
#     bootloader's setup-commands hook, and refuses to build an image without one:
#     a kernel that panics without a timeout hangs forever and the bootloader
#     never gets the reset it needs to roll the update back (SPIKE.md s7.7, Opus
#     binding adjustment). CONFIG_PANIC_TIMEOUT in a kernel config is deliberately
#     NOT relied on: vendor kernels do not carry our kernel fragments.
#
# Not here (other tasks): BOOTCOUNT_LIMIT / redundant-env sanity checks (TASK-S109-012),
# gating of native-only runtime pieces in the image (TASK-S109-009), GRUB panic delivery
# (TASK-S109-018).

inherit mender-setup

# ---- 1. the mode override ---------------------------------------------------
DISTROOVERRIDES:append = ":otapulse-bootloader"

# ---- 2a. customer variables ------------------------------------------------
OTAPULSE_PANIC_TIMEOUT_S ?= "10"
OTAPULSE_GENERATE_ARTIFACT ?= "1"

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

    # ---- OTAPULSE_* -> board support variables, "?=" semantics ----
    # A MENDER_* variable that is already set (explicit advanced override) wins.
    # noweakdefault: the board support's own "??=" defaults do not count as "set".
    for ovar, mvar in (('OTAPULSE_STORAGE_DEVICE', 'MENDER_STORAGE_DEVICE'),
                       ('OTAPULSE_STORAGE_TOTAL_SIZE_MB', 'MENDER_STORAGE_TOTAL_SIZE_MB'),
                       ('OTAPULSE_BOOT_PART_SIZE_MB', 'MENDER_BOOT_PART_SIZE_MB'),
                       ('OTAPULSE_DATA_PART_SIZE_MB', 'MENDER_DATA_PART_SIZE_MB'),
                       ('OTAPULSE_SWAP_PART_SIZE_MB', 'MENDER_SWAP_PART_SIZE_MB')):
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
MENDER_UBOOT_POST_SETUP_COMMANDS:prepend:mender-uboot = "setenv bootargs \${bootargs} panic=${OTAPULSE_PANIC_TIMEOUT_S}; "

# The kernel command line must end up with a positive panic= value. Returns the LAST
# panic= value found in TEXT (the kernel honours the last one), or None.
def otapulse_full_last_panic(text):
    import re
    vals = re.findall(r'(?<![\w-])panic=(-?\d+)', text or '')
    return int(vals[-1]) if vals else None

def otapulse_full_check_panic(d):
    """Fatal unless the bootloader setup commands leave a positive panic= on the
    kernel command line."""
    if d.getVar('OTAPULSE_BOOTLOADER') == 'grub':
        bb.fatal("OTAPULSE_BOOTLOADER = \"grub\": delivering the kernel panic timeout through the "
                 "GRUB environment is not supported yet, and bootloader mode refuses to build an "
                 "image whose hung kernel would never be rolled back.")
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
    # Bootloader (U-Boot) recipes built with the board support: verify the generated
    # defines header really carries the panic= setting that goes into the compiled-in env.
    if d.getVar('do_provide_mender_defines', False):
        d.appendVarFlag('do_provide_mender_defines', 'postfuncs', ' otapulse_full_check_uboot_defines')
}

otapulse_full_check_uboot_defines() {
    hdr="${S}/include/config_mender_defines.h"
    [ -f "$hdr" ] || bbfatal "otapulse-full: ${hdr} was not generated"
    if ! grep -Eq '^#define MENDER_UBOOT_(PRE|POST)_SETUP_COMMANDS "(.*[ ;])?panic=[1-9][0-9]*' "$hdr"; then
        bbfatal "otapulse-full: the bootloader environment built here does not set a kernel panic timeout (no panic=N in the setup commands of $hdr)"
    fi
    bbnote "otapulse-full: bootloader setup commands carry: $(grep -E '^#define MENDER_UBOOT_POST_SETUP_COMMANDS' "$hdr")"
}
