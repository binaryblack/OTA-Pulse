# Yocto recipe for frp (Fast Reverse Proxy) v0.69.0
#
# frp ships fully static Go binaries (no host toolchain needed); we fetch the
# pre-built release tarball for the target arch, install only the frpc (client)
# binary, and skip strip + SPDX checks that do not apply to foreign pre-built
# binaries.
#
# SHA256 CHECKSUMS:
# Because SRC_URI fetches a DIFFERENT tarball per architecture, the checksum
# must also be per-arch.  We carry the verified checksums in FRP_SHA256:<arch>
# override variables below and feed the active one into SRC_URI[sha256sum].
# The values were taken from frp's official release manifest
# (frp_sha256_checksums.txt for v0.69.0) and independently re-verified by
# downloading each tarball from the release URL.  To add a new arch, drop in
# its tarball checksum as another FRP_SHA256:<arch> line (or in a .bbappend).
# A wrong/empty checksum makes BitBake fail the fetch — the intended safety net.

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

# Per-arch verified sha256 of frp_0.69.0_linux_<arch>.tar.gz (matches frp's
# official frp_sha256_checksums.txt for v0.69.0).
FRP_SHA256:aarch64 = "24a4fc82b4c041835103419685ea124c4d6a7dbf83d0425481c5831b4ce4b3a4"
FRP_SHA256:arm     = "8ee99ad9b09eafe5f77fea7cbd9db15deb056dc2857955477972ccb31a74e708"
FRP_SHA256:x86_64  = "6b90d1cd28fc661f170c0de90dde03d2c63e4fd7ce0ae2da2ca1c28014b8146e"
# Unsupported arches get an obviously-invalid value so the fetch fails loudly.
FRP_SHA256         = "UNSUPPORTED_ARCH_${TARGET_ARCH}_supply_FRP_SHA256_override"

SRC_URI[sha256sum] = "${FRP_SHA256}"

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
