# Monitoring daemon for the SoC Monitoring platform
# This is the binary distribution version (shell scripts) for systems
# without Rust toolchain support

SUMMARY = "SoC Monitoring embedded systems monitoring daemon (shell version)"
DESCRIPTION = "A monitoring daemon for embedded Linux devices that provides \
telemetry collection, crash capture, coredump handling, and OTA update \
client functionality for the SoC Monitoring platform. This version uses \
shell scripts and is suitable for systems without Rust support."
HOMEPAGE = "https://github.com/example/soc-monitoring"
SECTION = "system"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

# =============================================================================
# PACKAGECONFIG - Optional Features
# =============================================================================
# Enable/disable features at build time in local.conf or image recipe:
#
#   To ENABLE dashboard:
#     PACKAGECONFIG:append:pn-socmond-bin = " dashboard"
#
# PACKAGECONFIG options:
#   dashboard - Include local web dashboard (requires python3, adds ~5MB)
PACKAGECONFIG ??= ""
PACKAGECONFIG[dashboard] = ",,,python3-core"

# Source files (shell script implementations)
SRC_URI = " \
    file://socmond \
    file://socmond-watchdog \
    file://socmond.service \
    file://socmond-watchdog.service \
    file://config.json \
    file://custom_metrics.conf.example \
    file://core-handler.sh \
    file://soc-ctl \
    file://soc-deregister \
    "

# Add dashboard files only if dashboard feature is enabled
SRC_URI += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', 'file://soc-dashboard file://soc-dashboard.service', '', d)}"

S = "${WORKDIR}"

# Dependencies for shell scripts
DEPENDS = "curl openssl systemd"
RDEPENDS:${PN} = "curl ca-certificates systemd gzip coreutils util-linux jq"

# Runtime recommendations
RRECOMMENDS:${PN} = "kernel-module-watchdog"

inherit systemd

# Systemd service configuration
SYSTEMD_SERVICE:${PN} = "socmond.service socmond-watchdog.service"
SYSTEMD_AUTO_ENABLE = "enable"

# Add dashboard service if enabled
SYSTEMD_SERVICE:${PN} += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', 'soc-dashboard.service', '', d)}"

do_install() {
    # Install main daemon binary
    install -d ${D}${bindir}
    install -m 0755 ${WORKDIR}/socmond ${D}${bindir}/socmond
    install -m 0755 ${WORKDIR}/socmond-watchdog ${D}${bindir}/socmond-watchdog
    install -m 0755 ${WORKDIR}/core-handler.sh ${D}${bindir}/socmond-core-handler

    # Install CLI tools
    install -m 0755 ${WORKDIR}/soc-ctl ${D}${bindir}/soc-ctl
    install -m 0755 ${WORKDIR}/soc-deregister ${D}${bindir}/soc-deregister

    # Install configuration directory and default config
    install -d ${D}${sysconfdir}/socmond
    install -m 0644 ${WORKDIR}/config.json ${D}${sysconfdir}/socmond/config.json
    install -m 0644 ${WORKDIR}/custom_metrics.conf.example ${D}${sysconfdir}/socmond/custom_metrics.conf.example

    # Install systemd service files
    install -d ${D}${systemd_unitdir}/system
    install -m 0644 ${WORKDIR}/socmond.service ${D}${systemd_unitdir}/system/socmond.service
    install -m 0644 ${WORKDIR}/socmond-watchdog.service ${D}${systemd_unitdir}/system/socmond-watchdog.service

    # Create directories for persistent data storage
    install -d ${D}${localstatedir}/lib/socmond
    install -d ${D}${localstatedir}/lib/socmond/ota
    install -d ${D}${localstatedir}/lib/socmond/coredumps
    install -d ${D}${localstatedir}/lib/socmond/metrics

    # Install sysctl configuration for coredump handling
    # Set suid_dumpable=0 first to avoid kernel warning about unsafe core_pattern
    install -d ${D}${sysconfdir}/sysctl.d
    cat > ${D}${sysconfdir}/sysctl.d/90-socmond-coredump.conf << 'SYSCTL_EOF'
# Socmond coredump handler configuration
# Set suid_dumpable=0 (safe) before setting pipe handler
fs.suid_dumpable=0
kernel.core_pattern=|/usr/bin/socmond-core-handler %p %u %g %s %t %c %h %e
kernel.core_pipe_limit=4
SYSCTL_EOF
    chmod 0644 ${D}${sysconfdir}/sysctl.d/90-socmond-coredump.conf

    # Create tmpfiles.d configuration for runtime directories
    if ${@bb.utils.contains('DISTRO_FEATURES', 'systemd', 'true', 'false', d)}; then
        install -d ${D}${nonarch_libdir}/tmpfiles.d
        cat > ${D}${nonarch_libdir}/tmpfiles.d/socmond.conf << 'EOF'
# Runtime directory for socmond daemon IPC socket
d /run/socmond 0755 root root -
# Log directory for socmond (if not using journald exclusively)
d /var/log/socmond 0755 root root -
EOF
    fi
}

do_install:append() {
    # Install dashboard if enabled (using BitBake conditional)
    if [ "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', '1', '0', d)}" = "1" ]; then
        install -m 0755 ${WORKDIR}/soc-dashboard ${D}${bindir}/soc-dashboard
        install -m 0644 ${WORKDIR}/soc-dashboard.service ${D}${systemd_unitdir}/system/soc-dashboard.service
    fi
}

# Files to include in the package
FILES:${PN} = " \
    ${bindir}/socmond \
    ${bindir}/socmond-watchdog \
    ${bindir}/socmond-core-handler \
    ${bindir}/soc-ctl \
    ${bindir}/soc-deregister \
    ${sysconfdir}/socmond \
    ${sysconfdir}/socmond/config.json \
    ${sysconfdir}/sysctl.d/90-socmond-coredump.conf \
    ${systemd_unitdir}/system/socmond.service \
    ${systemd_unitdir}/system/socmond-watchdog.service \
    ${localstatedir}/lib/socmond \
    "

# Add dashboard files if enabled
FILES:${PN} += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', '${bindir}/soc-dashboard', '', d)}"
FILES:${PN} += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', '${systemd_unitdir}/system/soc-dashboard.service', '', d)}"

# Add tmpfiles.d configuration for systemd-based systems
FILES:${PN} += "${@bb.utils.contains('DISTRO_FEATURES', 'systemd', '${nonarch_libdir}/tmpfiles.d/socmond.conf', '', d)}"

# Mark config file as configuration (preserve during upgrades)
CONFFILES:${PN} = "${sysconfdir}/socmond/config.json ${sysconfdir}/socmond/custom_metrics.conf"

# This package conflicts with the Rust version
RCONFLICTS:${PN} = "socmond"
RPROVIDES:${PN} = "socmond"

# Post-install script to create dynamic linker symlink if needed
pkg_postinst_${PN}() {
    #!/bin/sh
    # Ensure dynamic linker is accessible in /usr/lib for compatibility
    if [ ! -e $D/usr/lib/ld-linux-aarch64.so.1 ] && [ -e $D/lib/ld-linux-aarch64.so.1 ]; then
        mkdir -p $D/usr/lib
        ln -sf /lib/ld-linux-aarch64.so.1 $D/usr/lib/ld-linux-aarch64.so.1
    fi
}
