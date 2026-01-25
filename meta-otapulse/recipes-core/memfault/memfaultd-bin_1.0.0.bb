# Memfault-like monitoring daemon for SoC Monitoring platform
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
#     PACKAGECONFIG:append:pn-memfaultd-bin = " dashboard"
#
# PACKAGECONFIG options:
#   dashboard - Include local web dashboard (requires python3, adds ~5MB)
PACKAGECONFIG ??= ""
PACKAGECONFIG[dashboard] = ",,,python3-core"

# Source files (shell script implementations)
SRC_URI = " \
    file://memfaultd \
    file://memfault-watchdog \
    file://memfaultd.service \
    file://memfault-watchdog.service \
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
RDEPENDS:${PN} = "curl ca-certificates systemd bash gzip coreutils util-linux jq"

# Runtime recommendations
RRECOMMENDS:${PN} = "kernel-module-watchdog"

inherit systemd

# Systemd service configuration
SYSTEMD_SERVICE:${PN} = "memfaultd.service memfault-watchdog.service"
SYSTEMD_AUTO_ENABLE = "enable"

# Add dashboard service if enabled
SYSTEMD_SERVICE:${PN} += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', 'soc-dashboard.service', '', d)}"

do_install() {
    # Install main daemon binary
    install -d ${D}${bindir}
    install -m 0755 ${WORKDIR}/memfaultd ${D}${bindir}/memfaultd
    install -m 0755 ${WORKDIR}/memfault-watchdog ${D}${bindir}/memfault-watchdog
    install -m 0755 ${WORKDIR}/core-handler.sh ${D}${bindir}/memfault-core-handler

    # Install CLI tools
    install -m 0755 ${WORKDIR}/soc-ctl ${D}${bindir}/soc-ctl
    install -m 0755 ${WORKDIR}/soc-deregister ${D}${bindir}/soc-deregister

    # Install configuration directory and default config
    install -d ${D}${sysconfdir}/memfault
    install -m 0644 ${WORKDIR}/config.json ${D}${sysconfdir}/memfault/config.json
    install -m 0644 ${WORKDIR}/custom_metrics.conf.example ${D}${sysconfdir}/memfault/custom_metrics.conf.example

    # Install systemd service files
    install -d ${D}${systemd_unitdir}/system
    install -m 0644 ${WORKDIR}/memfaultd.service ${D}${systemd_unitdir}/system/memfaultd.service
    install -m 0644 ${WORKDIR}/memfault-watchdog.service ${D}${systemd_unitdir}/system/memfault-watchdog.service

    # Create directories for persistent data storage
    install -d ${D}${localstatedir}/lib/memfault
    install -d ${D}${localstatedir}/lib/memfault/ota
    install -d ${D}${localstatedir}/lib/memfault/coredumps
    install -d ${D}${localstatedir}/lib/memfault/metrics

    # Install sysctl configuration for coredump handling
    # Set suid_dumpable=0 first to avoid kernel warning about unsafe core_pattern
    install -d ${D}${sysconfdir}/sysctl.d
    cat > ${D}${sysconfdir}/sysctl.d/90-memfault-coredump.conf << 'SYSCTL_EOF'
# Memfault coredump handler configuration
# Set suid_dumpable=0 (safe) before setting pipe handler
fs.suid_dumpable=0
kernel.core_pattern=|/usr/bin/memfault-core-handler %p %u %g %s %t %c %h %e
kernel.core_pipe_limit=4
SYSCTL_EOF
    chmod 0644 ${D}${sysconfdir}/sysctl.d/90-memfault-coredump.conf

    # Create tmpfiles.d configuration for runtime directories
    if ${@bb.utils.contains('DISTRO_FEATURES', 'systemd', 'true', 'false', d)}; then
        install -d ${D}${nonarch_libdir}/tmpfiles.d
        cat > ${D}${nonarch_libdir}/tmpfiles.d/memfault.conf << 'EOF'
# Runtime directory for memfault daemon IPC socket
d /run/memfault 0755 root root -
# Log directory for memfault (if not using journald exclusively)
d /var/log/memfault 0755 root root -
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
    ${bindir}/memfaultd \
    ${bindir}/memfault-watchdog \
    ${bindir}/memfault-core-handler \
    ${bindir}/soc-ctl \
    ${bindir}/soc-deregister \
    ${sysconfdir}/memfault \
    ${sysconfdir}/memfault/config.json \
    ${sysconfdir}/sysctl.d/90-memfault-coredump.conf \
    ${systemd_unitdir}/system/memfaultd.service \
    ${systemd_unitdir}/system/memfault-watchdog.service \
    ${localstatedir}/lib/memfault \
    "

# Add dashboard files if enabled
FILES:${PN} += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', '${bindir}/soc-dashboard', '', d)}"
FILES:${PN} += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', '${systemd_unitdir}/system/soc-dashboard.service', '', d)}"

# Add tmpfiles.d configuration for systemd-based systems
FILES:${PN} += "${@bb.utils.contains('DISTRO_FEATURES', 'systemd', '${nonarch_libdir}/tmpfiles.d/memfault.conf', '', d)}"

# Mark config file as configuration (preserve during upgrades)
CONFFILES:${PN} = "${sysconfdir}/memfault/config.json ${sysconfdir}/memfault/custom_metrics.conf"

# This package conflicts with the Rust version
RCONFLICTS:${PN} = "memfaultd"
RPROVIDES:${PN} = "memfaultd"

# Post-install script to create dynamic linker symlink if needed
pkg_postinst_${PN}() {
    #!/bin/sh
    # Ensure dynamic linker is accessible in /usr/lib for compatibility
    if [ ! -e $D/usr/lib/ld-linux-aarch64.so.1 ] && [ -e $D/lib/ld-linux-aarch64.so.1 ]; then
        mkdir -p $D/usr/lib
        ln -sf /lib/ld-linux-aarch64.so.1 $D/usr/lib/ld-linux-aarch64.so.1
    fi
}
