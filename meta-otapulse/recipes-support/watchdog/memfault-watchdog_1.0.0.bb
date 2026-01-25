# Memfault Watchdog Service Recipe
# This recipe is now a virtual package that depends on memfaultd
# The watchdog functionality is included in the main memfaultd package

SUMMARY = "SoC Monitoring Watchdog Service"
DESCRIPTION = "Application-level watchdog that monitors system health \
and memfaultd service, triggering reboots on failures. This is now \
included in the main memfaultd package."
HOMEPAGE = "https://github.com/example/soc-monitoring"
SECTION = "system"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

# The watchdog is now part of memfaultd
RDEPENDS:${PN} = "memfaultd"

# This is a virtual package
ALLOW_EMPTY:${PN} = "1"
