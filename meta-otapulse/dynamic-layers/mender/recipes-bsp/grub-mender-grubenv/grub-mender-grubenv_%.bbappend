# OTA-Pulse bootloader mode, GRUB boot configuration: kernel panic timeout.
# (INTERNAL file, TASK-S109-030.)
#
# Why: in bootloader mode the GRUB environment owns slot switching and rollback. A
# kernel that panics without a timeout hangs forever, GRUB never boots again, the
# boot counter never gets its second look and a bad update is never rolled back.
# Every bootloader-mode image therefore needs "panic=N" on the kernel command line.
# For U-Boot boards that is injected through the U-Boot setup commands (see
# otapulse-full-bootloader.bbclass); for GRUB it is this bbappend.
#
# How: the board support builds grub.cfg by concatenating every
# [0-9][0-9]_*_grub.cfg fragment in its source directory in name order; 10_ builds
# the "bootargs" variable and 90_ passes it to the kernel. A fragment named 11_
# therefore runs after 10_ and before 90_, and appends the panic timeout last (the
# kernel honours the last panic= it sees).
#
# This file lives under dynamic-layers/ and is only parsed when the board support
# layer is part of the build (BBFILES_DYNAMIC in conf/layer.conf), i.e. never in a
# native build. The board support layers are only meant for bootloader mode
# (OTAPULSE_BOOT_MODE = "bootloader"): OTAPULSE_PANIC_TIMEOUT_S is defined by the
# bootloader-mode class only, so with the board support layers but without that mode the
# variable is undefined and do_configure fails the build (fail-closed).

OTAPULSE_PANIC_FRAGMENT_NAME = "11_otapulse_panic_bootargs_grub.cfg"

# The "${bootargs}" below is GRUB syntax, not a BitBake variable (no such BitBake
# variable exists, so BitBake leaves it as written; the check at the end of
# do_compile proves the generated grub.cfg is right).
do_configure:prepend() {
    if ! echo "${OTAPULSE_PANIC_TIMEOUT_S}" | grep -Eq '^[1-9][0-9]*$'; then
        bbfatal "OTAPULSE_PANIC_TIMEOUT_S must be a positive number of seconds (got \"${OTAPULSE_PANIC_TIMEOUT_S}\"). Bootloader mode requires a kernel panic timeout: a kernel that panics without one hangs forever and a failed update is never rolled back."
    fi
    cat > "${S}/${OTAPULSE_PANIC_FRAGMENT_NAME}" <<'OTAPULSE_PANIC_EOF'
# OTA-Pulse: kernel panic -> reboot after N seconds, so the boot counter can roll a failed update back
set bootargs="${bootargs} panic=${OTAPULSE_PANIC_TIMEOUT_S}"
OTAPULSE_PANIC_EOF
}
do_configure[vardeps] += "OTAPULSE_PANIC_TIMEOUT_S OTAPULSE_PANIC_FRAGMENT_NAME"

# Build gate: the generated grub.cfg must leave a positive panic= on the kernel
# command line, set AFTER the board support builds "bootargs" and BEFORE the kernel
# is started with it. This is the GRUB counterpart of otapulse_full_check_uboot_defines.
python otapulse_full_check_grub_panic() {
    import re
    path = os.path.join(d.getVar('B'), 'mender_grub.cfg')
    if not os.path.isfile(path):
        bb.fatal("OTAPULSE_BOOTLOADER = \"grub\": the GRUB boot configuration %s was not generated." % path)
    text = open(path, errors='replace').read()
    # sections are delimited by "# Start of ---------- <name> ----------" markers
    parts = re.split(r'^# Start of -+ (\S+) -+$', text, flags=re.M)
    sections = [(parts[i], parts[i + 1]) for i in range(1, len(parts) - 1, 2)]
    names = [n for n, _ in sections]
    fragment = d.getVar('OTAPULSE_PANIC_FRAGMENT_NAME')
    if fragment not in names:
        bb.fatal("OTAPULSE_PANIC_TIMEOUT_S: the OTA-Pulse kernel panic timeout fragment (%s) is missing from the "
                 "generated GRUB boot configuration. Bootloader mode refuses to build an image whose hung kernel "
                 "would never be rolled back." % fragment)
    base = [n for n in names if re.match(r'10_.*bootargs', n)]
    if not base:
        bb.fatal("OTAPULSE_BOOTLOADER = \"grub\": the generated GRUB boot configuration does not build the kernel "
                 "command line (no bootargs section); the kernel panic timeout cannot be verified.")
    start = names.index(base[0])
    # Effective panic= of the kernel command line variable, evaluated in file order. An
    # assignment ("set bootargs=..." or plain "bootargs=...") that does not build on the
    # old value ("${bootargs}" / "$bootargs") replaces the whole command line, and so does
    # "unset bootargs": both drop the panic timeout. The value that counts is the one in
    # force when the kernel is started (the linux line), not what happens after it.
    last = None          # (panic value, fragment) or None
    effective = None     # value of `last` at the moment the kernel is started
    boot_uses_bootargs = False
    panic_re = r'(?<![\w-])panic=(-?\d+)'
    for name, body in sections[start:]:
        for line in body.splitlines():
            line = line.strip()
            if not line or line.startswith('#'):
                continue
            if re.match(r'unset\s+(?:.*\s)?bootargs(?:\s|$)', line):
                last = None
                continue
            m = re.match(r'(?:set\s+)?bootargs=(.*)$', line)
            if m:
                value = m.group(1)
                found = re.findall(panic_re, value)
                if found:
                    last = (int(found[-1]), name)
                elif not re.search(r'\$(?:\{bootargs\}|bootargs(?!\w))', value):
                    last = None
                continue
            if re.match(r'(?:if\s+)?linux\s', line) and re.search(r'\$(?:\{bootargs\}|bootargs(?!\w))', line):
                boot_uses_bootargs = True
                found = re.findall(panic_re, line)
                if found:
                    last = (int(found[-1]), name)
                effective = last
    last = effective
    if not boot_uses_bootargs:
        bb.fatal("OTAPULSE_BOOTLOADER = \"grub\": the generated GRUB boot configuration does not pass the kernel "
                 "command line variable to the kernel; the kernel panic timeout would have no effect.")
    if last is None or last[0] < 1:
        bb.fatal("OTAPULSE_PANIC_TIMEOUT_S: the generated GRUB boot configuration %s. Bootloader mode requires a "
                 "positive kernel panic timeout on the kernel command line (do not override it with panic=0)."
                 % ('sets panic=%d (in %s)' % last if last else 'does not set panic='))
    expected = d.getVar('OTAPULSE_PANIC_TIMEOUT_S')
    if str(last[0]) != str(expected) or last[1] != fragment:
        bb.warn("OTAPULSE_PANIC_TIMEOUT_S = %s but the effective kernel panic timeout in GRUB is %d (set in %s)"
                % (expected, last[0], last[1]))
    bb.note("otapulse-full: GRUB kernel command line carries panic=%d (from %s)" % last)
}
do_compile[postfuncs] += "otapulse_full_check_grub_panic"
