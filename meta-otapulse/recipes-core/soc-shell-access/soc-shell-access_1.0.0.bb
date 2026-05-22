# Yocto recipe for soc-shell-access v1.0.0
#
# PURPOSE
# -------
# Ships the Standard SSH shell tier's non-root user (`support`) and a curated
# sudoers allowlist so the OTA-Pulse gateway can log in as `support` (not root)
# and run a small set of read-only diagnostics + approved service restarts via
# NOPASSWD sudo — nothing else.
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
#   --password !      : locked password; login is via SSH key only.
USERADD_PARAM:${PN} = "--system --shell /bin/sh --home-dir /home/support --create-home --no-user-group --gid support --password ! support"

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
}

# ---------------------------------------------------------------------------
# Package file lists
# ---------------------------------------------------------------------------
FILES:${PN} = " \
    ${sysconfdir}/sudoers.d/10-soc-support \
    ${libexecdir}/soc-diag \
    ${libexecdir}/soc-diag/.keep \
    ${libexecdir}/soc-diag/soc-readlog \
    /home/support \
"

# CONFFILES: declare the sudoers drop-in as a managed config file that OTA
# replaces (rather than merging with local changes).  This is the mechanism
# by which the operator-non-editable property is enforced at the package
# manager level: an OTA update always overwrites the file.
CONFFILES:${PN} = " \
    ${sysconfdir}/sudoers.d/10-soc-support \
"

# ---------------------------------------------------------------------------
# Skip SPDX — local files only, no external source tree.
# Same pattern as soc-ota-agent and soc-ota-tunneld recipes.
# ---------------------------------------------------------------------------
do_create_spdx[noexec] = "1"
do_create_runtime_spdx[noexec] = "1"
