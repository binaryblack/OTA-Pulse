SUMMARY = "OTA-Pulse Remote SSH — support user + sudoers allowlist + sshd drop-in"
DESCRIPTION = "Provisions the 'support' Linux user that operators land in when \
they open a standard-tier Remote SSH session via the OTA-Pulse gateway. Ships \
the sudoers allowlist (only the few commands the support tier may run with \
elevated privileges) and the sshd_config drop-in that pins the support \
session to no-forwarding, no-X11, PTY-only. RDEPENDS on sudo and openssh-sshd \
so the drop-ins land in functional locations. (TODO-002 sprint S16.)"
HOMEPAGE = "https://github.com/binaryblack/OTA-Pulse"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

SRC_URI = " \
    file://10-soc-support.sudoers \
    file://10-soc-support.sshd_config \
"

S = "${WORKDIR}"

# Runtime dependencies: drop-ins need their consumers installed.
RDEPENDS:${PN} = "sudo openssh-sshd"

# Create the 'support' user at image build time.
inherit useradd

USERADD_PACKAGES = "${PN}"
# Use the default UID range (>= 1000) so F12's `id` assertion sees a real user.
# No password (locked by default). Shell is /bin/sh which always exists in
# core-image-minimal; F1 attaches a PTY so the gateway gets a working shell.
# (systemd-journal group membership for read-only `journalctl` access is added
# at first boot by a postinst — see pkg_postinst below — because the group
# does not exist in the recipe-sysroot at useradd-time.)
GROUPADD_PARAM:${PN} = "support"
USERADD_PARAM:${PN} = "-m -d /home/support -s /bin/sh -g support support"

pkg_postinst:${PN}() {
    # Best-effort: add 'support' to systemd-journal once both exist on the
    # target rootfs. Failure (e.g. group not present in a minimal image) is
    # non-fatal — journalctl for the user's own messages still works.
    if [ -z "$D" ]; then
        getent group systemd-journal >/dev/null 2>&1 && \
            usermod -a -G systemd-journal support 2>/dev/null || true
    fi
}

do_install() {
    install -d -m 0750 ${D}${sysconfdir}/sudoers.d
    install -m 0440 ${WORKDIR}/10-soc-support.sudoers \
        ${D}${sysconfdir}/sudoers.d/10-soc-support

    install -d -m 0755 ${D}${sysconfdir}/ssh/sshd_config.d
    install -m 0644 ${WORKDIR}/10-soc-support.sshd_config \
        ${D}${sysconfdir}/ssh/sshd_config.d/10-soc-support.conf
}

FILES:${PN} = " \
    ${sysconfdir}/sudoers.d/10-soc-support \
    ${sysconfdir}/ssh/sshd_config.d/10-soc-support.conf \
"

# sudoers files have a strict ownership/mode contract; tell QA they're intentional.
INSANE_SKIP:${PN} = "already-stripped"
