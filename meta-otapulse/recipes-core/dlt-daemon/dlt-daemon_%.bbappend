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

# BUG-474: upstream's default PACKAGECONFIG compiles in udp-connection
# (-DWITH_UDP_CONNECTION=ON) and dlt-adaptor-udp (-DWITH_DLT_ADAPTOR_UDP=ON)
# unconditionally. udp-connection makes dlt-daemon bind 0.0.0.0:3490 and
# multicast the entire live log stream, in plaintext, to 225.0.0.37:3491 on
# the LAN -- proven live to leak real journald content from every board.
# dlt.conf's UDPConnectionSetup = 0 disables this at runtime; removing the
# PACKAGECONFIG here is belt-and-braces so a future dlt.conf regression
# can't silently re-enable it (the capability is never compiled in at all).
# dlt-adaptor-udp is a separate standalone relay binary/service upstream
# also enables by default; not observed active on any board in this fleet
# (dlt-adaptor-udp.service is not-found in the built package), but dropped
# for the same defense-in-depth reason -- no board should ever need it.
PACKAGECONFIG:remove = "udp-connection dlt-adaptor-udp"

FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

SRC_URI += " \
    file://dlt.conf \
    file://dlt-system.conf \
    file://dlt-ordering.conf \
    file://dlt-system-ordering.conf \
    "

do_install:append() {
    install -m 0644 ${WORKDIR}/dlt.conf ${D}${sysconfdir}/dlt.conf
    install -m 0644 ${WORKDIR}/dlt-system.conf ${D}${sysconfdir}/dlt-system.conf

    install -d -m 0755 ${D}${systemd_system_unitdir}/dlt.service.d
    install -m 0644 ${WORKDIR}/dlt-ordering.conf \
        ${D}${systemd_system_unitdir}/dlt.service.d/50-otapulse-ordering.conf

    # Upstream dlt-system.service only Wants=dlt.service, no After= — real
    # start-order race found live (TASK-S43-003), see
    # dlt-system-ordering.conf for the failure evidence.
    install -d -m 0755 ${D}${systemd_system_unitdir}/dlt-system.service.d
    install -m 0644 ${WORKDIR}/dlt-system-ordering.conf \
        ${D}${systemd_system_unitdir}/dlt-system.service.d/50-otapulse-ordering.conf
}

FILES:${PN} += " \
    ${systemd_system_unitdir}/dlt.service.d/50-otapulse-ordering.conf \
    ${systemd_system_unitdir}/dlt-system.service.d/50-otapulse-ordering.conf \
    "
