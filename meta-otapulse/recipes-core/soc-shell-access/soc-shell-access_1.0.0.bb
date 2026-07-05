# Yocto recipe for soc-shell-access v1.0.0
#
# CHANGE LOG
# ----------
# S16-009 (2026-05-23): Corrects S16-008's locked-password design.
#   S16-008 created support with --password ! (locked).  A live test on RPi4
#   proved this breaks gateway pubkey login when the device sshd has no PAM:
#   OpenSSH's own allowed_user() rejects ALL auth methods (including publickey)
#   for accounts whose shadow field is a locked sentinel (!, !!, *).
#   Fix: give support a SHA-512 hash of a build-time random secret (non-locked,
#   unknowable) + ship an sshd drop-in that disables PasswordAuthentication for
#   support, so the unknown password can never be used over SSH.
#
# PURPOSE
# -------
# Ships the Standard SSH shell tier's non-root user (`support`) and a curated
# sudoers allowlist so the OTA-Pulse gateway can log in as `support` (not root)
# and run a small set of read-only diagnostics + approved service restarts via
# NOPASSWD sudo — nothing else.
# Also ships an sshd drop-in (10-soc-support.conf) enforcing pubkey-only auth
# for the support user (PasswordAuthentication no, AuthenticationMethods publickey).
#
# OPERATOR-NON-EDITABLE PROPERTY
# -------------------------------
# The sudoers file is installed as root:root mode 0440 (required by sudo for
# a sudoers.d drop-in).  The `support` user has NO sudo/wheel/root group
# membership; its only elevation is this exact file.  Because `support` is
# non-root it cannot overwrite a root-owned 0440 file, so the allowlist cannot
# be widened from a support session.  On every OTA update the package manager
# replaces the file with the version shipped in the update artifact, restoring
# any on-device tampering done by root.  CONFFILES ensures the packager treats
# it as a managed config replaced by OTA (not preserved across updates).
#
# ALLOWLIST SUMMARY (see files/soc-support.sudoers for full detail)
# -----------------------------------------------------------------
#  SOC_JOURNALCTL        journalctl                       (read-only)
#  SOC_DMESG             dmesg                            (read-only)
#  SOC_IP_ADDR           ip addr                          (read-only, ip addr only)
#  SOC_DIAG              /usr/libexec/soc-diag/*          (vendor scripts + soc-readlog)
#  SOC_SYSTEMCTL_STATUS  systemctl status <any>           (read-only)
#  SOC_SYSTEMCTL_RESTART systemctl restart <approved>     (constrained list)
#
# Log reading is done via the validated wrapper
# /usr/libexec/soc-diag/soc-readlog (covered by SOC_DIAG), NOT via a raw
# 'cat /var/log/*' sudo entry — see SECURITY note below.
#
# systemctl restart is limited to explicit service names:
#   soc-ota-agent.service, soc-ota-tunneld.service, NetworkManager.service,
#   systemd-networkd.service, systemd-resolved.service, chronyd.service
# Wildcard restart (systemctl restart *) is intentionally EXCLUDED to prevent
# a support session from restarting arbitrary daemons or masking a privilege
# escalation path.
#
# SECURITY — why log reading uses a wrapper, not 'sudo cat /var/log/*'
# -------------------------------------------------------------------
# In sudoers, a '*' in a command ARGUMENT is matched by fnmatch() WITHOUT
# FNM_PATHNAME, so '*' matches '/' AND '..'.  An entry like
# '/bin/cat /var/log/*' therefore matches 'sudo cat /var/log/../../etc/shadow'
# and would let the non-root support user read ANY root-owned file — a full
# privilege-escalation hole that defeats the non-root design.  We removed that
# entry entirely.  Log reading now goes through soc-readlog, which takes a
# single basename argument, rejects '/' / '..' / control chars / leading '-',
# canonicalises the path and re-verifies it is a regular file directly under
# /var/log/, then exec's 'cat -- /var/log/<name>'.  No traversal is possible.
#
# SHELL-ESCAPE AUDIT
# ------------------
# No shell interpreter (sh, bash, dash, zsh) appears in the allowlist.
# No editors (vi, nano, emacs, ed) appear.  No tools that can spawn a shell
# (less --exec, find -exec, tee, dd, awk, perl, python) appear.  'ip addr'
# is the only ip subcommand allowed (not ip link set, ip route add, etc.).
# journalctl, dmesg, ip addr are all read-only kernel/journal interfaces.
# 'systemctl status *' is read-only by design (queries state, never modifies).
# soc-readlog is the only file-reading path and is traversal-hardened.

SUMMARY = "OTA-Pulse Standard shell tier: support user + curated sudoers allowlist"
DESCRIPTION = "Creates the non-root 'support' SSH user used by the OTA-Pulse gateway \
for the Standard remote-shell tier, and ships a root-owned 0440 sudoers drop-in \
that grants a curated NOPASSWD allowlist (read-only diagnostics + constrained \
service restarts).  The file is replaced on every OTA update so the operator \
cannot widen the allowlist from a support session."
HOMEPAGE = "https://github.com/binaryblack/OTA-Pulse"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

# ---------------------------------------------------------------------------
# Runtime dependencies
# ---------------------------------------------------------------------------
# sudo must be present — without it the sudoers file is inert.
# shadow provides useradd/usermod/passwd tools used during image construction.
RDEPENDS:${PN} = " \
    sudo \
    shadow \
"

# ---------------------------------------------------------------------------
# Source files (all local, no network fetch)
# ---------------------------------------------------------------------------
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

SRC_URI = " \
    file://soc-support.sudoers \
    file://soc-readlog \
    file://.keep \
    file://sshd_config.d/10-soc-support.conf \
    file://gateway-authorized-key \
    file://08-gateway-authkeys.conf \
"

S = "${WORKDIR}"

# ---------------------------------------------------------------------------
# User creation via useradd class
# ---------------------------------------------------------------------------
# inherit useradd: BitBake calls useradd/groupadd at do_rootfs time using the
# parameters below.  The user is created inside the image rootfs (${D}),
# not on the build host.
inherit useradd

# USERADD_PACKAGES must list every package that carries a USERADD_PARAM or
# GROUPADD_PARAM so BitBake can schedule user creation before packaging.
USERADD_PACKAGES = "${PN}"

# USERADD_PARAM for the 'support' user:
#   --system          : allocates a UID in the system range (< 1000), but
#                       --shell /bin/sh overrides the nologin default so the
#                       SSH PTY gets a real interactive shell.  Note: on
#                       Yocto targets 'system user' simply means a reserved UID;
#                       it does NOT prevent interactive login — that is controlled
#                       by the shell field.
#   --shell /bin/sh   : real login shell required for the SSH PTY.
#   --home-dir /home/support : home directory (created with --create-home).
#   --create-home     : mkdir + set ownership at adduser time.
#   --no-user-group   : do not auto-create a 'support' primary group; use the
#                       explicit --gid below so the GID is stable.
#   --gid support     : primary group 'support' (created by GROUPADD_PARAM).
#   --groups ""       : no secondary groups — not wheel, not sudo, not root.
#   --password *      : INITIAL placeholder — do_install replaces this with a
#                       real SHA-512 hash at build time (see below).  We cannot
#                       pass the final hash here because it is generated at
#                       install time from openssl rand.  Quoted ('*') because
#                       useradd_base.bbclass's perform_useradd() re-parses
#                       USERADD_PARAM via a second `eval` pass; an unquoted `*`
#                       is glob-expanded there against whatever files happen to
#                       exist in bitbake's cwd at that moment, injecting extra
#                       positional args after the `support` LOGIN argument and
#                       making useradd fail non-deterministically (BUG-151).
#                       The literal quote characters survive both eval passes
#                       and suppress the expansion while the placeholder value
#                       useradd/pkg_postinst see is still the literal `*`.
#
# PASSWORD DESIGN (S16-009 fix):
#   The device sshd is compiled WITHOUT PAM (no /etc/pam.d/sshd → UsePAM no).
#   OpenSSH's allowed_user() inspects the shadow field directly and rejects ALL
#   auth methods — including publickey — when the field is a locked/disabled
#   sentinel (!, !!, *).  S16-008 used --password ! which triggered this check
#   and broke gateway pubkey login (proven on RPi4 live test).
#
#   The fix: generate a SHA-512crypt hash of a 32-byte random secret at
#   do_install time using `openssl passwd -6`.  The hash is present and not a
#   locked sentinel, so allowed_user() is satisfied.  The plaintext secret is
#   discarded immediately after hashing — it is never stored in the image or
#   logs, is different on every build, and is therefore effectively unknowable.
#   The sshd drop-in (10-soc-support.conf, see below) disables
#   PasswordAuthentication for support, so the unknown password cannot be used
#   over SSH regardless.
#   Result: passwd -S support shows status "P" (password set), NOT "L" (locked).
USERADD_PARAM:${PN} = "--system --shell /bin/sh --home-dir /home/support --create-home --no-user-group --gid support --password '*' support"

# Create the primary group for the support user with a stable GID.
# Using 'support' as the group name; GID is auto-assigned in system range.
GROUPADD_PARAM:${PN} = "--system support"

# ---------------------------------------------------------------------------
# Install
# ---------------------------------------------------------------------------
do_install() {
    FILESDIR="${THISDIR}/files"

    # ------------------------------------------------------------------
    # 1. Sudoers drop-in
    #    Mode 0440, owner root:root — required by sudo; any other mode
    #    or owner causes sudo to ignore the file entirely.
    # ------------------------------------------------------------------
    install -d ${D}${sysconfdir}/sudoers.d
    install -m 0440 ${FILESDIR}/soc-support.sudoers \
        ${D}${sysconfdir}/sudoers.d/10-soc-support
    # Explicitly set owner root:root (install -o/-g not portable across all
    # OE hosts; chown in do_install operates on ${D} as the build user and
    # is corrected by the fakeroot environment).
    chown root:root ${D}${sysconfdir}/sudoers.d/10-soc-support

    # ------------------------------------------------------------------
    # 2. Vendor diagnostic scripts directory
    #    Ships empty (with a .keep marker) so the SOC_DIAG sudoers alias
    #    has a valid target directory from day one.  Vendor scripts are
    #    dropped in here by subsequent OTA updates or other recipes.
    # ------------------------------------------------------------------
    install -d -m 0755 ${D}${libexecdir}/soc-diag
    install -m 0644 ${FILESDIR}/.keep ${D}${libexecdir}/soc-diag/.keep
    chown root:root ${D}${libexecdir}/soc-diag/.keep

    # ------------------------------------------------------------------
    # 2b. Validated log-reader wrapper
    #     Replaces the removed 'sudo cat /var/log/*' entry.  Root-owned
    #     0755 so support can execute it via sudo (SOC_DIAG alias) but
    #     cannot modify it.  Confines reads to /var/log/ with no traversal.
    # ------------------------------------------------------------------
    install -m 0755 ${FILESDIR}/soc-readlog ${D}${libexecdir}/soc-diag/soc-readlog
    chown root:root ${D}${libexecdir}/soc-diag/soc-readlog

    # ------------------------------------------------------------------
    # 3. Support user home directory
    #    useradd --create-home creates /home/support inside the rootfs at
    #    do_rootfs time.  We also seed it here so it appears in FILES and
    #    the package manager tracks it.  Mode 0750: support owns it, no
    #    world-read (SSH authorized_keys must not be world-readable).
    # ------------------------------------------------------------------
    install -d -m 0750 ${D}/home/support

    # ------------------------------------------------------------------
    # 4. sshd drop-in: pubkey-only auth for the support user
    #    Installed 0644 root:root into /etc/ssh/sshd_config.d/ which is
    #    Include-d by the OE-core/poky sshd_config at its top line:
    #      Include /etc/ssh/sshd_config.d/*.conf
    #    No modification to the main sshd_config is required.
    #
    #    The Match User support block sets:
    #      PasswordAuthentication no
    #      KbdInteractiveAuthentication no
    #      PubkeyAuthentication yes
    #      AuthenticationMethods publickey
    #    This ensures the (unknowable) password generated in step 5 below
    #    can NEVER be used over SSH — only the gateway pubkey works.
    # ------------------------------------------------------------------
    install -d -m 0755 ${D}${sysconfdir}/ssh/sshd_config.d
    install -m 0644 ${FILESDIR}/sshd_config.d/10-soc-support.conf \
        ${D}${sysconfdir}/ssh/sshd_config.d/10-soc-support.conf
    chown root:root ${D}${sysconfdir}/ssh/sshd_config.d/10-soc-support.conf

    # ------------------------------------------------------------------
    # 5. Gateway authorized key
    #    The OTA-Pulse gateway authenticates to sshd as the `support` user
    #    using this public key. Without it, sshd enforces publickey-only
    #    auth but has nothing to match against → WS 1011 SSH connection
    #    failed on every remote shell session (regression from _1.0.bb).
    # ------------------------------------------------------------------
    install -d -m 0755 ${D}${sysconfdir}/ssh/authorized_keys
    install -m 0644 ${FILESDIR}/gateway-authorized-key \
        ${D}${sysconfdir}/ssh/authorized_keys/support

    # ------------------------------------------------------------------
    # 6. sshd drop-in: AuthorizedKeysFile lookup path
    #    Tells sshd to search /etc/ssh/authorized_keys/%u in addition to
    #    ~/.ssh/authorized_keys so the gateway key above is found.
    # ------------------------------------------------------------------
    install -m 0644 ${FILESDIR}/08-gateway-authkeys.conf \
        ${D}${sysconfdir}/ssh/sshd_config.d/08-gateway-authkeys.conf

    # NOTE: sshd_config Include injection (step 7 / BUG-114 fix) is done in
    # pkg_postinst below, NOT here.  do_install runs in this package's own
    # staging directory (${D}), which does NOT contain openssh's sshd_config —
    # that file lives in openssh's separate staging area and is only merged
    # into a single rootfs at do_rootfs time.  Modifying it here is a no-op.
    # pkg_postinst runs after do_rootfs has assembled the full image, so
    # ${D} at that point is the complete rootfs and sshd_config is present.

    # NOTE: shadow password replacement (step 5 / S16-009 fix) is done in
    # pkg_postinst:${PN} below, not here.  The useradd class creates the
    # support user in the rootfs shadow at do_rootfs time, which runs AFTER
    # do_install.  pkg_postinst runs after useradd completes, so it can
    # update the shadow field with the build-time-generated random hash.
}

# ---------------------------------------------------------------------------
# pkg_postinst: replace the support user's placeholder shadow password with
# a build-time SHA-512crypt hash of an unknowable random secret.
#
# This runs in fakeroot at do_rootfs time, AFTER the useradd class has
# created the support user in ${IMAGE_ROOTFS}/etc/shadow.  It replaces
# the initial '*' placeholder with a real hash so allowed_user() in
# OpenSSH (no-PAM builds) does not see a locked sentinel.
#
# IMPLEMENTATION NOTES
# --------------------
# - We cannot set the final hash in USERADD_PARAM because the random secret
#   must be generated fresh at image-assembly time and cannot be known at
#   parse time.
# - We do NOT use `usermod -R "$D" -p ...` here: pkg_postinst does not run
#   under PSEUDO, and usermod may not be available natively for the target.
#   Instead, we use a direct sed on $D/etc/shadow — the same approach used
#   by OE-core's useradd_base.bbclass for in-image shadow edits.
# - openssl passwd -6 is available on all modern build hosts (openssl 1.1+).
# - The plaintext secret is a 32-byte hex string that exists only in the
#   shell variable `_secret` for the duration of this subshell; it is never
#   written to disk, the image, or any log.  A fresh secret is generated
#   on every build, so the hash is not reproducible.
# ---------------------------------------------------------------------------
pkg_postinst:${PN} () {
    # Only run at image assembly time ($D is the target rootfs root).
    # When $D is empty this is an on-device postinst run — we skip it
    # because the hash is baked in at build time; nothing to do on device.
    if [ -n "$D" ]; then
        _shadow="$D/etc/shadow"
        if [ -f "$_shadow" ] && grep -q "^support:" "$_shadow"; then
            # Generate a SHA-512crypt hash of a random 32-byte secret.
            # openssl passwd -6 uses SHA-512crypt ($6$...$...).
            # The secret never leaves this subshell.
            _secret=$(openssl rand -hex 32)
            _hash=$(printf '%s' "$_secret" | openssl passwd -6 -stdin)
            unset _secret
            # Replace only the password field (field 2) for the support line.
            # sed expression: match ^support:<any-field2>: and substitute field 2.
            sed --follow-symlinks -i \
                "s|^support:[^:]*:|support:${_hash}:|" \
                "$_shadow"
            unset _hash
        fi
    fi

    # -----------------------------------------------------------------------
    # Step 7 (BUG-114): Ensure main sshd_config loads the drop-ins via Include.
    #
    # Newer OE-core/poky openssh ships "Include /etc/ssh/sshd_config.d/*.conf"
    # at the top of sshd_config automatically.  Older images (e.g. L4T-based
    # Jetson running OpenSSH 7.x / 8.x, built from sshd_config,v 1.102 2018)
    # do not.  Without the Include directive, 08-gateway-authkeys.conf is
    # silently ignored and gateway pubkey auth fails with WS 1011.
    #
    # WHY pkg_postinst AND NOT do_install:
    #   do_install runs in soc-shell-access's own package staging dir (${D}).
    #   At that point openssh's sshd_config has NOT been merged in — it lives
    #   in openssh's separate ${D} (WORKDIR/image).  The merge happens at
    #   do_rootfs time, which runs AFTER all do_install tasks complete.
    #   pkg_postinst with a non-empty $D runs at do_rootfs time, after all
    #   packages are installed into the rootfs, so sshd_config IS present.
    #
    # The on-device guard (if [ -n "$D" ]) is correct for Mender full-image
    # OTA: new rootfs images are assembled from scratch on the build host
    # (pkg_postinst runs there with $D set); the postinst never executes on
    # the device, so we do not risk modifying a live root-owned system file.
    # -----------------------------------------------------------------------
    if [ -n "$D" ]; then
        _sshd_conf="$D/etc/ssh/sshd_config"
        if [ -f "$_sshd_conf" ] && ! grep -q "^Include /etc/ssh/sshd_config.d" "$_sshd_conf"; then
            # Try to insert after the "This sshd was compiled with" comment that
            # older OpenSSH configs always carry — keeps the file readable.
            sed --follow-symlinks -i \
                '/^\# This sshd was compiled with/a Include /etc/ssh/sshd_config.d/*.conf' \
                "$_sshd_conf"
            # Fallback: anchor comment absent → prepend to file.
            if ! grep -q "^Include /etc/ssh/sshd_config.d" "$_sshd_conf"; then
                sed --follow-symlinks -i \
                    '1s/^/Include \/etc\/ssh\/sshd_config.d\/*.conf\n/' \
                    "$_sshd_conf"
            fi
        fi
        unset _sshd_conf
    fi
}

# ---------------------------------------------------------------------------
# Package file lists
# ---------------------------------------------------------------------------
FILES:${PN} = " \
    ${sysconfdir}/sudoers.d/10-soc-support \
    ${sysconfdir}/ssh/sshd_config.d/10-soc-support.conf \
    ${sysconfdir}/ssh/sshd_config.d/08-gateway-authkeys.conf \
    ${sysconfdir}/ssh/authorized_keys/support \
    ${libexecdir}/soc-diag \
    ${libexecdir}/soc-diag/.keep \
    ${libexecdir}/soc-diag/soc-readlog \
    /home/support \
"

# CONFFILES: declare managed config drop-ins as OTA-replaced files so
# the package manager always overwrites them on update (operator edits do
# not persist across OTA updates).
CONFFILES:${PN} = " \
    ${sysconfdir}/sudoers.d/10-soc-support \
    ${sysconfdir}/ssh/sshd_config.d/10-soc-support.conf \
    ${sysconfdir}/ssh/sshd_config.d/08-gateway-authkeys.conf \
    ${sysconfdir}/ssh/authorized_keys/support \
"

# ---------------------------------------------------------------------------
# Skip SPDX — local files only, no external source tree.
# Same pattern as soc-ota-agent and soc-ota-tunneld recipes.
# ---------------------------------------------------------------------------
do_create_spdx[noexec] = "1"
do_create_runtime_spdx[noexec] = "1"
