# Yocto recipe for soc-shell-access-privileged v1.0.0
#
# CHANGE LOG
# ----------
# BUG-263 (2026-08-17): Elevated + Break-Glass remote-SSH tiers had no real
#   credential provisioning anywhere in the fleet — the gateway's
#   ELEVATED_SSH_USER/BREAKGLASS_SSH_USER both defaulted to literal 'root',
#   and every board that appeared to support those tiers was actually
#   demonstrating Yocto's debug-tweaks blank root password, not a real
#   elevated-tier mechanism (see tracker/bugs/BUG-263.yaml). This recipe is
#   the real mechanism: two dedicated non-root system accounts
#   (otapulse-elevated, otapulse-breakglass) modeled directly on
#   soc-shell-access's proven support-tier design, each with its own
#   gateway SSH key and its own sudoers allowlist. It is a SEPARATE recipe
#   from soc-shell-access_1.0.0.bb — that proven support-tier package is
#   not touched by this change at all.
#
#   Lessons inherited from soc-shell-access (see that recipe's own header
#   for the full writeups):
#     - S16-009: a no-PAM sshd's allowed_user() rejects ALL auth methods,
#       including publickey, for a locked shadow sentinel (!, !!, *).  Both
#       new users get a SHA-512crypt hash of a fresh build-time random
#       secret in pkg_postinst instead — never a locked entry.
#     - BUG-151: USERADD_PARAM's '--password' value must be the LITERAL
#       quoted string '*' (not a bare, unquoted '*') — perform_useradd()
#       re-evals USERADD_PARAM a second time and an unquoted '*' would
#       glob-expand against bitbake's cwd, injecting extra positional args
#       and making useradd fail non-deterministically.
#     - BUG-114: sshd_config's "Include /etc/ssh/sshd_config.d/*.conf" must
#       be present for any *.conf drop-in (including this package's) to be
#       loaded at all on older (pre-Include-by-default) OpenSSH configs.
#       That injection is performed idempotently by soc-shell-access's own
#       pkg_postinst; this package RDEPENDS on soc-shell-access precisely
#       so it never has to duplicate that mechanism.
#
# THREAT MODEL (carried into both sudoers files — see soc-elevated.sudoers
# and soc-breakglass.sudoers for the per-tier detail)
# -----------------------------------------------------------------------
# The elevated allowlist is a guardrail against accidental/casual
# brick-risk operations and a compromise-containment + audit boundary —
# NOT a watertight sandbox against a determined malicious operator.  That
# residual risk is what the approval workflow, forced session recording,
# MFA gate, and the separate break-glass identity exist for.  Break-glass
# is deliberately unbounded (NOPASSWD: ALL) behind its own identity/key —
# its safety property is attribution + the approval gate on minting a
# session in the first place, not command-level restriction.
#
# PURPOSE
# -------
# Ships:
#   - otapulse-elevated: a constrained non-root user with a curated
#     NOPASSWD sudo allowlist (service control, read-only network/journal/
#     boot-env inspection, a handful of read-only soc-ota-agent verbs,
#     reboot) plus systemd-journal/adm group membership for sudo-free
#     journalctl/dmesg reads, and the soc-readetc wrapper for traversal-
#     hardened /etc reads (a SEPARATE libexec directory from
#     soc-shell-access's /usr/libexec/soc-diag/, so it is never reachable
#     through the support tier's own SOC_DIAG alias).
#   - otapulse-breakglass: a separate non-root user with unrestricted
#     NOPASSWD sudo (ALL). The gateway opens break-glass sessions via
#     'sudo -i' (BREAKGLASS_SHELL_COMMAND) so the operator lands in a root
#     shell while sshd auth, audit trail, and $SUDO_USER all still
#     attribute the session to otapulse-breakglass, not root.
#   - An sshd drop-in (12-soc-privileged.conf) enforcing pubkey-only auth
#     for both users, and each user's own authorized_keys file under
#     /etc/ssh/authorized_keys/<user> (found via soc-shell-access's
#     08-gateway-authkeys.conf %u lookup — no change needed there).
#
# Does NOT touch root's own shadow entry or PermitRootLogin. Does NOT
# modify soc-shell-access_1.0.0.bb or anything under its files/.

SUMMARY = "OTA-Pulse Elevated + Break-Glass shell tiers: otapulse-elevated / otapulse-breakglass users + curated sudoers"
DESCRIPTION = "Creates the non-root 'otapulse-elevated' and 'otapulse-breakglass' SSH \
users used by the OTA-Pulse gateway for the Elevated and Break-Glass remote-shell \
tiers (BUG-263), replacing the prior literal-root design. otapulse-elevated gets a \
curated NOPASSWD sudoers allowlist plus systemd-journal/adm group membership for \
sudo-free diagnostics; otapulse-breakglass gets unrestricted NOPASSWD sudo behind a \
separate identity/key, entered via 'sudo -i' at session open. Both sudoers drop-ins \
are root-owned 0440 and replaced on every OTA update so the operator cannot widen \
either allowlist from a session using it. This is a guardrail and audit boundary, \
not a sandbox against a determined malicious operator — see the approval workflow, \
forced recording, and MFA gate for that residual risk."
HOMEPAGE = "https://github.com/binaryblack/OTA-Pulse"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

# ---------------------------------------------------------------------------
# Runtime dependencies
# ---------------------------------------------------------------------------
# sudo must be present — without it both sudoers files are inert.
# shadow provides useradd/usermod/passwd tools used during image construction.
# soc-shell-access guarantees 08-gateway-authkeys.conf (the %u authorized_keys
# lookup) and the BUG-114 sshd_config Include injection are present on any
# image that also carries this package — this recipe does not duplicate
# either mechanism.
RDEPENDS:${PN} = " \
    sudo \
    shadow \
    soc-shell-access \
"

# ---------------------------------------------------------------------------
# Source files (all local, no network fetch)
# ---------------------------------------------------------------------------
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

SRC_URI = " \
    file://soc-elevated.sudoers \
    file://soc-breakglass.sudoers \
    file://soc-readetc \
    file://elevated-profile \
    file://gateway-elevated-authorized-key \
    file://gateway-breakglass-authorized-key \
    file://sshd_config.d/12-soc-privileged.conf \
"

S = "${WORKDIR}"

# ---------------------------------------------------------------------------
# User creation via useradd class
# ---------------------------------------------------------------------------
inherit useradd

USERADD_PACKAGES = "${PN}"

# --password '*' — INITIAL placeholder, quoted exactly as soc-shell-access's
# template (BUG-151): perform_useradd() re-parses USERADD_PARAM via a second
# `eval` pass, and an unquoted `*` would glob-expand there against whatever
# files happen to exist in bitbake's cwd at that moment, injecting extra
# positional args after the LOGIN argument and making useradd fail
# non-deterministically. do_install/pkg_postinst replace this placeholder
# with a real per-user SHA-512 hash at build time — see pkg_postinst below.
USERADD_PARAM:${PN} = "\
    --system --shell /bin/sh --home-dir /home/otapulse-elevated --create-home --no-user-group --gid otapulse-elevated --password '*' otapulse-elevated; \
    --system --shell /bin/sh --home-dir /home/otapulse-breakglass --create-home --no-user-group --gid otapulse-breakglass --password '*' otapulse-breakglass \
"
GROUPADD_PARAM:${PN} = "--system otapulse-elevated; --system otapulse-breakglass"

# ---------------------------------------------------------------------------
# Install
# ---------------------------------------------------------------------------
do_install() {
    FILESDIR="${THISDIR}/files"

    # ------------------------------------------------------------------
    # 1. Sudoers drop-ins
    #    Mode 0440, owner root:root — required by sudo; any other mode
    #    or owner causes sudo to ignore the file entirely.
    # ------------------------------------------------------------------
    install -d ${D}${sysconfdir}/sudoers.d
    install -m 0440 ${FILESDIR}/soc-elevated.sudoers \
        ${D}${sysconfdir}/sudoers.d/15-soc-elevated
    chown root:root ${D}${sysconfdir}/sudoers.d/15-soc-elevated

    install -m 0440 ${FILESDIR}/soc-breakglass.sudoers \
        ${D}${sysconfdir}/sudoers.d/16-soc-breakglass
    chown root:root ${D}${sysconfdir}/sudoers.d/16-soc-breakglass

    # ------------------------------------------------------------------
    # 2. Elevated-tier /etc reader wrapper
    #    Deliberately a SEPARATE directory from /usr/libexec/soc-diag —
    #    installing it there would silently grant it to the support user
    #    too, via soc-shell-access's SOC_DIAG = /usr/libexec/soc-diag/*
    #    alias.
    # ------------------------------------------------------------------
    install -d -m 0755 ${D}${libexecdir}/soc-diag-elevated
    install -m 0755 ${FILESDIR}/soc-readetc \
        ${D}${libexecdir}/soc-diag-elevated/soc-readetc
    chown root:root ${D}${libexecdir}/soc-diag-elevated/soc-readetc

    # ------------------------------------------------------------------
    # 3. Home directories (mirrors soc-shell-access template step 3
    #    exactly — no extra chowns beyond what useradd --create-home
    #    already does at do_rootfs time).
    # ------------------------------------------------------------------
    install -d -m 0750 ${D}/home/otapulse-elevated
    install -d -m 0750 ${D}/home/otapulse-breakglass

    # ------------------------------------------------------------------
    # 4. Elevated-tier shell profile (pager -> cat, for sudo-free
    #    journalctl/dmesg reads via the systemd-journal/adm group grant
    #    added in pkg_postinst below).
    # ------------------------------------------------------------------
    install -m 0644 ${FILESDIR}/elevated-profile \
        ${D}/home/otapulse-elevated/.profile

    # ------------------------------------------------------------------
    # 5. sshd drop-in: pubkey-only auth for both users.
    # ------------------------------------------------------------------
    install -d -m 0755 ${D}${sysconfdir}/ssh/sshd_config.d
    install -m 0644 ${FILESDIR}/sshd_config.d/12-soc-privileged.conf \
        ${D}${sysconfdir}/ssh/sshd_config.d/12-soc-privileged.conf
    chown root:root ${D}${sysconfdir}/ssh/sshd_config.d/12-soc-privileged.conf

    # ------------------------------------------------------------------
    # 6. Gateway authorized keys — one per tier, so the two identities are
    #    cryptographically distinguishable (never the same key as
    #    'support' or as each other). Found via soc-shell-access's
    #    08-gateway-authkeys.conf AuthorizedKeysFile %u lookup — that file
    #    is global-only and needs no change.
    # ------------------------------------------------------------------
    install -d -m 0755 ${D}${sysconfdir}/ssh/authorized_keys
    install -m 0644 ${FILESDIR}/gateway-elevated-authorized-key \
        ${D}${sysconfdir}/ssh/authorized_keys/otapulse-elevated
    install -m 0644 ${FILESDIR}/gateway-breakglass-authorized-key \
        ${D}${sysconfdir}/ssh/authorized_keys/otapulse-breakglass

    # NOTE: sshd_config Include injection and shadow password replacement
    # both run in pkg_postinst below, not here — see soc-shell-access's own
    # do_install for why (do_install runs before do_rootfs assembles the
    # full rootfs / before useradd populates ${D}/etc/shadow).
}

# ---------------------------------------------------------------------------
# pkg_postinst: replace both users' placeholder shadow passwords with a
# fresh per-user SHA-512crypt hash, and grant otapulse-elevated
# systemd-journal/adm group membership for sudo-free journalctl/dmesg.
#
# Runs in fakeroot at do_rootfs time, after the useradd class has created
# both users in ${IMAGE_ROOTFS}/etc/shadow — see soc-shell-access's own
# pkg_postinst for the full rationale (direct sed on $D/etc/shadow rather
# than usermod, since pkg_postinst does not run under PSEUDO).
# ---------------------------------------------------------------------------
pkg_postinst:${PN} () {
    if [ -n "$D" ]; then
        _shadow="$D/etc/shadow"
        for _u in otapulse-elevated otapulse-breakglass; do
            if [ -f "$_shadow" ] && grep -q "^${_u}:" "$_shadow"; then
                _secret=$(openssl rand -hex 32)
                _hash=$(printf '%s' "$_secret" | openssl passwd -6 -stdin)
                unset _secret
                sed --follow-symlinks -i "s|^${_u}:[^:]*:|${_u}:${_hash}:|" "$_shadow"
                unset _hash
            fi
        done
        # Sudo-free diagnostics for the elevated tier: journal + log group
        # membership (group may be created by systemd/base-files — append
        # only if the group exists; mirror into gshadow when present).
        for _g in systemd-journal adm; do
            if grep -q "^${_g}:" "$D/etc/group" 2>/dev/null; then
                if ! grep -q "^${_g}:.*\botapulse-elevated\b" "$D/etc/group"; then
                    sed --follow-symlinks -i \
                        "s|^\(${_g}:[^:]*:[^:]*:\)\(.*\)$|\1\2,otapulse-elevated|;s|:,otapulse-elevated$|:otapulse-elevated|" \
                        "$D/etc/group"
                fi
                if [ -f "$D/etc/gshadow" ] && grep -q "^${_g}:" "$D/etc/gshadow" \
                   && ! grep -q "^${_g}:.*\botapulse-elevated\b" "$D/etc/gshadow"; then
                    sed --follow-symlinks -i \
                        "s|^\(${_g}:[^:]*:[^:]*:\)\(.*\)$|\1\2,otapulse-elevated|;s|:,otapulse-elevated$|:otapulse-elevated|" \
                        "$D/etc/gshadow"
                fi
            fi
        done
    fi
    # No sshd Include injection here — soc-shell-access (RDEPENDS) already
    # performs the BUG-114 injection idempotently at do_rootfs time.
}

# ---------------------------------------------------------------------------
# Package file lists
# ---------------------------------------------------------------------------
FILES:${PN} = " \
    ${sysconfdir}/sudoers.d/15-soc-elevated \
    ${sysconfdir}/sudoers.d/16-soc-breakglass \
    ${sysconfdir}/ssh/sshd_config.d/12-soc-privileged.conf \
    ${sysconfdir}/ssh/authorized_keys/otapulse-elevated \
    ${sysconfdir}/ssh/authorized_keys/otapulse-breakglass \
    ${libexecdir}/soc-diag-elevated \
    ${libexecdir}/soc-diag-elevated/soc-readetc \
    /home/otapulse-elevated \
    /home/otapulse-elevated/.profile \
    /home/otapulse-breakglass \
"

# CONFFILES: declare managed config drop-ins as OTA-replaced files so the
# package manager always overwrites them on update (operator edits do not
# persist across OTA updates).
CONFFILES:${PN} = " \
    ${sysconfdir}/sudoers.d/15-soc-elevated \
    ${sysconfdir}/sudoers.d/16-soc-breakglass \
    ${sysconfdir}/ssh/sshd_config.d/12-soc-privileged.conf \
    ${sysconfdir}/ssh/authorized_keys/otapulse-elevated \
    ${sysconfdir}/ssh/authorized_keys/otapulse-breakglass \
"

# ---------------------------------------------------------------------------
# Skip SPDX — local files only, no external source tree. Same pattern as
# soc-shell-access, soc-ota-agent, and soc-ota-tunneld recipes.
# ---------------------------------------------------------------------------
do_create_spdx[noexec] = "1"
do_create_runtime_spdx[noexec] = "1"
