# Yocto recipe for frp (Fast Reverse Proxy) v0.69.0
#
# frp ships fully static Go binaries (no host toolchain needed); we fetch the
# pre-built release tarball for the target arch, install only the frpc (client)
# binary, and skip strip + SPDX checks that do not apply to foreign pre-built
# binaries.
#
# IMPORTANT — SHA256 CHECKSUMS:
# The SRC_URI[sha256sum] placeholder below is intentionally left blank.
# You MUST fill in the real per-architecture checksum before baking.
# To obtain it, run:
#   curl -sL <URL> | sha256sum
# for each architecture's tarball, where <URL> matches the pattern below.
# Leaving a wrong or empty checksum will cause BitBake to fail the fetch task
# with a checksum mismatch — that is the intended safety net.

SUMMARY = "frp (Fast Reverse Proxy) client — NAT-traversal reverse tunnel client"
DESCRIPTION = "frpc (frp client) connects to an frps server to establish reverse \
TCP/UDP proxies across NAT boundaries.  OTA-Pulse uses it for the Remote SSH \
terminal feature (soc-ota-tunneld supervises frpc on-device)."
HOMEPAGE = "https://github.com/fatedier/frp"
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://LICENSE;md5=ccc13f8f0e2ee8e7ce4c89d8ec11e1e9"

# -----------------------------------------------------------------------
# Architecture mapping: Yocto TARGET_ARCH → frp release arch suffix
# -----------------------------------------------------------------------
# frp release naming uses GOARCH identifiers (amd64, arm64, arm, etc.).
# BitBake's TARGET_ARCH uses kernel naming (x86_64, aarch64, arm, ...).
#
# We define FRP_ARCH here to translate.  Machines with other arches (e.g.
# mips, riscv64) must add a corresponding line in their machine conf or
# in a .bbappend before including this recipe, otherwise the fetch will
# fail with an unsupported-arch error that is easy to diagnose.
FRP_ARCH:aarch64    = "arm64"
FRP_ARCH:arm        = "arm"
FRP_ARCH:x86_64     = "amd64"
FRP_ARCH:i586       = "386"
FRP_ARCH:i686       = "386"
FRP_ARCH            = "UNSUPPORTED_ARCH_${TARGET_ARCH}"

# -----------------------------------------------------------------------
# Source URI — GitHub release tarball
# -----------------------------------------------------------------------
SRC_URI = "https://github.com/fatedier/frp/releases/download/v${PV}/frp_${PV}_linux_${FRP_ARCH}.tar.gz"

# INTEGRATOR ACTION REQUIRED:
# Replace "FILL_IN_SHA256_FOR_ARCH" with the real sha256sum of the tarball
# for each architecture you need to support.  Using a .bbappend per machine
# is the cleanest way to set the per-arch checksum:
#
#   SRC_URI[sha256sum] = "<actual_sha256_of_frp_0.69.0_linux_arm64.tar.gz>"
#
# Example (NOT authoritative — verify before use):
#   aarch64: verify with `sha256sum frp_0.69.0_linux_arm64.tar.gz`
#   arm:     verify with `sha256sum frp_0.69.0_linux_arm.tar.gz`
#   x86_64:  verify with `sha256sum frp_0.69.0_linux_amd64.tar.gz`
SRC_URI[sha256sum] = "FILL_IN_SHA256_FOR_ARCH_see_comment_above"

S = "${WORKDIR}/frp_${PV}_linux_${FRP_ARCH}"

# frp pre-built binaries are already stripped by the Go linker (-s -w).
# Yocto's strip task would either fail or corrupt them, so we skip it.
INSANE_SKIP:${PN} = "already-stripped"
INHIBIT_PACKAGE_STRIP = "1"
INHIBIT_SYSROOT_STRIP = "1"
INHIBIT_PACKAGE_DEBUG_SPLIT = "1"

# Skip SPDX — externally produced binary, no source tree to scan.
do_create_spdx[noexec] = "1"
do_create_runtime_spdx[noexec] = "1"

# This recipe has no compilation step — nothing to configure or build.
do_configure[noexec] = "1"
do_compile[noexec]   = "1"

do_install() {
    # Install only frpc (the client).  frps is NOT installed — soc-ota-tunneld
    # only needs the client side; frps runs on the gateway server.
    install -d ${D}${bindir}
    install -m 0755 ${S}/frpc ${D}${bindir}/frpc
}

FILES:${PN} = "${bindir}/frpc"

# Mark as machine-specific because we fetch different tarballs per arch.
PACKAGE_ARCH = "${MACHINE_ARCH}"
