# Linux Kernel Configuration Append for SoC Monitoring
# Enables crash capture, coredump, and debug features

FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

SRC_URI += " \
    file://monitoring.cfg \
    file://watchdog.cfg \
    file://debug.cfg \
    "

# Enable kernel debugging symbols for crash analysis
# Remove DEBUG_BUILD override for production builds
DEBUG_BUILD ?= "0"

# Kernel configuration fragments
KERNEL_CONFIG_FRAGMENTS += " \
    ${WORKDIR}/monitoring.cfg \
    ${WORKDIR}/watchdog.cfg \
    ${WORKDIR}/debug.cfg \
    "

# Ensure coredump handler works properly
do_configure:append() {
    # Verify critical configs are enabled
    grep -q "CONFIG_COREDUMP=y" ${B}/.config || echo "WARNING: CONFIG_COREDUMP not enabled"
    grep -q "CONFIG_ELF_CORE=y" ${B}/.config || echo "WARNING: CONFIG_ELF_CORE not enabled"
}
