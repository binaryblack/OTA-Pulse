# OTA-Pulse dlt-daemon configuration — sprint S43 (TODO-032).
#
# Overrides upstream meta-oe's default /etc/dlt.conf and
# /etc/dlt-system.conf with our own (localhost-only TCP binding,
# offline/persistent trace storage under /data/dlt, journald ingestion
# enabled) and adds a systemd ordering drop-in. No PACKAGECONFIG changes —
# meta-oe's default build already compiles in everything needed (confirmed
# in docs/dlt-daemon-recipe-spike.md, TASK-S43-002).

FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

SRC_URI += " \
    file://dlt.conf \
    file://dlt-system.conf \
    file://dlt-ordering.conf \
    "

do_install:append() {
    install -m 0644 ${WORKDIR}/dlt.conf ${D}${sysconfdir}/dlt.conf
    install -m 0644 ${WORKDIR}/dlt-system.conf ${D}${sysconfdir}/dlt-system.conf

    install -d -m 0755 ${D}${systemd_system_unitdir}/dlt.service.d
    install -m 0644 ${WORKDIR}/dlt-ordering.conf \
        ${D}${systemd_system_unitdir}/dlt.service.d/50-otapulse-ordering.conf
}

FILES:${PN} += " \
    ${systemd_system_unitdir}/dlt.service.d/50-otapulse-ordering.conf \
    "
