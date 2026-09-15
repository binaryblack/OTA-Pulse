# OTAPulse Time-Sync Gating (BUG-441 / BUG-432 / BUG-435)
#
# Every OTA-Pulse board ships systemd-time-wait-sync.service (the stock unit
# that blocks dependents until the clock is verifiably NTP-synced) but it is
# DISABLED by default, and none of soc-ota-agent/socmond/otapulse-auto-
# provision order themselves after time-sync.target — so on any board that
# boots with a wrong/floor clock (no RTC, or a dead RTC battery), the very
# first TLS call these units make can race a cold clock and fail cert
# validation (X509_V_ERR_CERT_NOT_YET_VALID), which soc-ota-agent's own
# retry-then-rollback logic treats as grounds to discard an otherwise
# successful update (BUG-432 on Jetson, BUG-435 on Dragon Q6A, BUG-441 on
# RPi4 — the same fleet-wide gap, generalised here instead of fixed per
# board).
#
# This recipe enables systemd-timesyncd.service and
# systemd-time-wait-sync.service fleet-wide (generic — no per-board/per-
# machine conditionals) and bounds the wait so a board with no reachable NTP
# path still boots (time-wait-sync has no timeout upstream):
#   - systemd-time-wait-sync.service.d/10-otapulse.conf: TimeoutStartSec=120
#   - timesyncd.conf.d/10-otapulse.conf: FallbackNTP as a backstop if the
#     board's own timesyncd.conf NTP= is unset/unreachable
# The sysinit.target.wants symlinks are created directly in do_install
# (matching the existing set_dragon_q6a_ntp() ROOTFS_POSTPROCESS_COMMAND
# pattern in custom-base-image.bb) rather than via the systemd bbclass's
# postinst-enable mechanism, since SYSTEMD_SERVICE only auto-enables units
# THIS recipe installs — these two units are shipped by the systemd recipe
# itself.

SUMMARY = "OTAPulse fleet-wide time-sync gating (bounded NTP wait before TLS)"
DESCRIPTION = "Enables systemd-timesyncd and systemd-time-wait-sync with a \
bounded (120s) timeout and a FallbackNTP backstop, so every board's clock is \
verifiably synced (or the bound is hit) before soc-ota-agent/socmond/ \
otapulse-auto-provision make their first TLS call. Closes the BUG-432/435/441 \
failure class fleet-wide instead of per board."
HOMEPAGE = "https://github.com/binaryblack/OTA-Pulse"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

SRC_URI = " \
    file://10-otapulse-time-wait-sync.conf \
    file://10-otapulse-timesyncd.conf \
"

S = "${WORKDIR}"

do_install() {
    # Bound systemd-time-wait-sync's wait so a board with no reachable NTP
    # path still boots instead of hanging forever (no upstream timeout).
    install -d ${D}${sysconfdir}/systemd/system/systemd-time-wait-sync.service.d
    install -m 0644 ${WORKDIR}/10-otapulse-time-wait-sync.conf \
        ${D}${sysconfdir}/systemd/system/systemd-time-wait-sync.service.d/10-otapulse.conf

    # Fallback NTP backstop if the board's own timesyncd.conf NTP= is unset
    # or unreachable.
    install -d ${D}${sysconfdir}/systemd/timesyncd.conf.d
    install -m 0644 ${WORKDIR}/10-otapulse-timesyncd.conf \
        ${D}${sysconfdir}/systemd/timesyncd.conf.d/10-otapulse.conf

    # Enable both units fleet-wide via sysinit.target.wants symlinks, same
    # pattern as custom-base-image.bb's set_dragon_q6a_ntp().
    install -d ${D}${sysconfdir}/systemd/system/sysinit.target.wants
    ln -sf ${systemd_system_unitdir}/systemd-timesyncd.service \
        ${D}${sysconfdir}/systemd/system/sysinit.target.wants/systemd-timesyncd.service
    ln -sf ${systemd_system_unitdir}/systemd-time-wait-sync.service \
        ${D}${sysconfdir}/systemd/system/sysinit.target.wants/systemd-time-wait-sync.service
}

FILES:${PN} = " \
    ${sysconfdir}/systemd/system/systemd-time-wait-sync.service.d/10-otapulse.conf \
    ${sysconfdir}/systemd/timesyncd.conf.d/10-otapulse.conf \
    ${sysconfdir}/systemd/system/sysinit.target.wants/systemd-timesyncd.service \
    ${sysconfdir}/systemd/system/sysinit.target.wants/systemd-time-wait-sync.service \
"
