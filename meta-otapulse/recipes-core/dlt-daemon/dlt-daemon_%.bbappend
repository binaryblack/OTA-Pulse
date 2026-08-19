# OTA-Pulse dlt-daemon configuration — sprint S43 (TODO-032).
#
# Overrides upstream meta-oe's default /etc/dlt.conf and
# /etc/dlt-system.conf with our own (localhost-only TCP binding,
# offline/persistent trace storage under /data/dlt, journald ingestion
# enabled) and adds a systemd ordering drop-in.
#
# Real bug found + fixed during TASK-S43-003 live verification: upstream's
# CMakeLists.txt defaults WITH_DLT_USE_IPv6 to ON, unconditionally (not
# gated by any PACKAGECONFIG) — dlt-daemon binds via AF_INET6, so an IPv4
# BindAddress like 127.0.0.1 fails inet_pton(AF_INET6, ...) and the daemon
# exits at startup ("Could not open main socket, for binding 127.0.0.1").
# This project's addressing is IPv4-only throughout (DB target_ip, every
# board's static IP) — disable IPv6 at build time instead of switching to
# an IPv6 loopback literal, matching that convention.
EXTRA_OECMAKE += "-DWITH_DLT_USE_IPv6=OFF"

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
