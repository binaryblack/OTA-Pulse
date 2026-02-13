# Memfault-like monitoring daemon for SoC Monitoring platform
# Provides telemetry collection, crash capture, and OTA client
# This is the Rust implementation

SUMMARY = "SoC Monitoring embedded systems monitoring daemon"
DESCRIPTION = "A monitoring daemon for embedded Linux devices that provides \
telemetry collection, crash capture, coredump handling, and OTA update \
client functionality for the SoC Monitoring platform. Written in Rust for \
performance and reliability."
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
#     PACKAGECONFIG:append:pn-memfaultd = " dashboard"
#   
#   To DISABLE dashboard (default):
#     PACKAGECONFIG:remove:pn-memfaultd = "dashboard"
#
# Or set directly:
#     PACKAGECONFIG:pn-memfaultd = "dashboard"
# =============================================================================

# PACKAGECONFIG options:
#   dashboard - Include local web dashboard (requires python3, adds ~5MB)
#
# Format: PACKAGECONFIG[feature] = "enable-flag,disable-flag,depends,rdepends,conflicts"
PACKAGECONFIG ??= ""
PACKAGECONFIG[dashboard] = ",,,python3-core"

# Source files (base)
# NOTE: data.mount removed - /data is handled by otapulse-partition-setup.service
SRC_URI = " \
    file://memfaultd-rs \
    file://memfaultd \
    file://memfault-watchdog \
    file://memfaultd.service \
    file://memfault-watchdog.service \
    file://memfaultd.init \
    file://memfaultd.json \
    file://core-handler.sh \
    file://soc-deregister \
    file://soc-ctl \
    file://show-version \
    "

# Add dashboard files only if dashboard feature is enabled
SRC_URI += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', 'file://soc-dashboard file://soc-dashboard.service', '', d)}"

S = "${WORKDIR}/memfaultd-rs"

# Build dependencies
DEPENDS = "openssl"

# Runtime dependencies
RDEPENDS:${PN} = "ca-certificates jq curl"
RDEPENDS:${PN} += "${@bb.utils.contains('DISTRO_FEATURES', 'systemd', 'systemd', '', d)}"

# Runtime recommendations
RRECOMMENDS:${PN} = "kernel-module-watchdog"

# Inherit cargo for Rust compilation, and init system support
inherit cargo
inherit ${@bb.utils.contains('DISTRO_FEATURES', 'systemd', 'systemd', '', d)}
inherit ${@bb.utils.contains('DISTRO_FEATURES', 'sysvinit', 'update-rc.d', '', d)}

# Use bundled OpenSSL
CARGO_FEATURES = ""

# Cargo build flags for embedded targets
CARGO_BUILD_FLAGS = "--release"

# Package architecture
PACKAGE_ARCH = "${MACHINE_ARCH}"

# Systemd service configuration (if systemd is used)
# NOTE: data.mount removed - /data is handled by otapulse-partition-setup.service
SYSTEMD_SERVICE:${PN} = "memfaultd.service memfault-watchdog.service"
SYSTEMD_AUTO_ENABLE = "enable"

# SysVinit configuration (if sysvinit is used)
INITSCRIPT_NAME = "memfaultd"
INITSCRIPT_PARAMS = "defaults 99"

do_compile() {
    # Set up cargo environment
    export CARGO_HOME="${WORKDIR}/cargo_home"
    export RUSTFLAGS="-C target-feature=-crt-static"

    # Build all binaries
    cargo build ${CARGO_BUILD_FLAGS}
}

do_install() {
    # Install main daemon binary
    install -d ${D}${bindir}

    # Install Rust binaries (or fall back to shell scripts if not built)
    if [ -f ${S}/target/release/memfaultd ]; then
        install -m 0755 ${S}/target/release/memfaultd ${D}${bindir}/memfaultd
    else
        bbwarn "Rust binary not found, installing shell script fallback"
        install -m 0755 ${WORKDIR}/memfaultd ${D}${bindir}/memfaultd
    fi

    if [ -f ${S}/target/release/memfault-watchdog ]; then
        install -m 0755 ${S}/target/release/memfault-watchdog ${D}${bindir}/memfault-watchdog
    else
        bbwarn "Rust watchdog not found, installing shell script fallback"
        install -m 0755 ${WORKDIR}/memfault-watchdog ${D}${bindir}/memfault-watchdog
    fi

    if [ -f ${S}/target/release/memfault-core-handler ]; then
        install -m 0755 ${S}/target/release/memfault-core-handler ${D}${bindir}/memfault-core-handler
    else
        # Fall back to shell script core handler
        install -m 0755 ${WORKDIR}/core-handler.sh ${D}${bindir}/memfault-core-handler
    fi

    # Install configuration directory and default config
    install -d ${D}${sysconfdir}/memfault
    install -m 0644 ${WORKDIR}/memfaultd.json ${D}${sysconfdir}/memfault/config.json

    # Install version utility
    install -m 0755 ${WORKDIR}/show-version ${D}${bindir}/show-version

    # Install init scripts based on init system
    if ${@bb.utils.contains('DISTRO_FEATURES', 'systemd', 'true', 'false', d)}; then
        # Install systemd service files
        # NOTE: data.mount not installed - /data is handled by otapulse-partition-setup.service
        install -d ${D}${systemd_unitdir}/system
        install -m 0644 ${WORKDIR}/memfaultd.service ${D}${systemd_unitdir}/system/memfaultd.service
        install -m 0644 ${WORKDIR}/memfault-watchdog.service ${D}${systemd_unitdir}/system/memfault-watchdog.service

        # Create tmpfiles.d configuration for runtime directories
        install -d ${D}${nonarch_libdir}/tmpfiles.d
        cat > ${D}${nonarch_libdir}/tmpfiles.d/memfault.conf << 'EOF'
# Runtime directory for memfault daemon IPC socket
d /run/memfault 0755 root root -
# Log directory for memfault (if not using journald exclusively)
d /var/log/memfault 0755 root root -
EOF
    fi

    if ${@bb.utils.contains('DISTRO_FEATURES', 'sysvinit', 'true', 'false', d)}; then
        # Install SysVinit script
        install -d ${D}${sysconfdir}/init.d
        install -m 0755 ${WORKDIR}/memfaultd.init ${D}${sysconfdir}/init.d/memfaultd
    fi

    # Create directories for persistent data storage
    install -d ${D}${localstatedir}/lib/memfault
    install -d ${D}${localstatedir}/lib/memfault/ota
    install -d ${D}${localstatedir}/lib/memfault/coredumps
    install -d ${D}${localstatedir}/lib/memfault/metrics

    # Install sysctl configuration for coredump handling
    install -d ${D}${sysconfdir}/sysctl.d
    echo "kernel.core_pattern=|${bindir}/memfault-core-handler %p %u %g %s %t %c %h %e" > ${D}${sysconfdir}/sysctl.d/90-memfault-coredump.conf
    chmod 0644 ${D}${sysconfdir}/sysctl.d/90-memfault-coredump.conf

    # Install soc-deregister utility script
    install -m 0755 ${WORKDIR}/soc-deregister ${D}${bindir}/soc-deregister

    # Install soc-ctl CLI tool
    install -m 0755 ${WORKDIR}/soc-ctl ${D}${bindir}/soc-ctl

    # Install soc-dashboard (only if dashboard feature is enabled)
    if ${@bb.utils.contains('PACKAGECONFIG', 'dashboard', 'true', 'false', d)}; then
        install -m 0755 ${WORKDIR}/soc-dashboard ${D}${bindir}/soc-dashboard
        if ${@bb.utils.contains('DISTRO_FEATURES', 'systemd', 'true', 'false', d)}; then
            install -m 0644 ${WORKDIR}/soc-dashboard.service ${D}${systemd_unitdir}/system/soc-dashboard.service
        fi
    fi
}

# Files to include in the package
FILES:${PN} = " \
    ${bindir}/memfaultd \
    ${bindir}/memfault-watchdog \
    ${bindir}/memfault-core-handler \
    ${bindir}/soc-deregister \
    ${bindir}/soc-ctl \
    ${sysconfdir}/memfault \
    ${sysconfdir}/memfault/config.json \
    ${sysconfdir}/sysctl.d/90-memfault-coredump.conf \
    ${localstatedir}/lib/memfault \
    "

# Add systemd files if systemd is used
# NOTE: data.mount removed - /data is handled by otapulse-partition-setup.service
FILES:${PN} += "${@bb.utils.contains('DISTRO_FEATURES', 'systemd', '${systemd_unitdir}/system/memfaultd.service ${systemd_unitdir}/system/memfault-watchdog.service ${nonarch_libdir}/tmpfiles.d/memfault.conf', '', d)}"

# Add SysVinit script if sysvinit is used
FILES:${PN} += "${@bb.utils.contains('DISTRO_FEATURES', 'sysvinit', '${sysconfdir}/init.d/memfaultd', '', d)}"

# Mark config file as configuration (preserve during upgrades)
CONFFILES:${PN} = "${sysconfdir}/memfault/config.json"

# Dashboard files (only included if dashboard feature is enabled)
FILES:${PN} += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', '${bindir}/soc-dashboard', '', d)}"
FILES:${PN} += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', bb.utils.contains('DISTRO_FEATURES', 'systemd', '${systemd_unitdir}/system/soc-dashboard.service', '', d), '', d)}"

# Dashboard systemd service (disabled by default, user must enable manually)
SYSTEMD_SERVICE:${PN} += "${@bb.utils.contains('PACKAGECONFIG', 'dashboard', 'soc-dashboard.service', '', d)}"

# QA checks
INSANE_SKIP:${PN} += "ldflags"
