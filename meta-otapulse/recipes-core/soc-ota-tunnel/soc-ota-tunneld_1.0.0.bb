# Yocto recipe for soc-ota-tunneld v1.0.0
#
# soc-ota-tunneld is a small, CGO-free, statically-linked reverse-SSH tunnel
# supervisor.  It supervises the external frpc binary (provided by the frp
# recipe) which does the actual NAT traversal.  On SIGTERM it cancels the frpc
# process gracefully.
#
# Build strategy (mirrors soc-ota-agent recipe conventions):
#   - inherit externalsrc: source comes from the checked-out soc-ota-agent tree.
#   - inherit goarch:      sets TARGET_GOARCH/TARGET_GOOS from Yocto tune vars.
#   - inherit systemd:     installs and enables the systemd unit.
#   - CGO_ENABLED=0:       pure-Go static binary — no cross-toolchain C deps.
#   - Makefile build-tunneld target: isolates the tunneld build from the main
#     CGO agent (which requires lmdb, openssl, etc.).

SUMMARY = "OTA-Pulse reverse-SSH tunnel supervisor daemon"
DESCRIPTION = "soc-ota-tunneld supervises frpc to establish a persistent reverse \
TCP tunnel from the firmware device to the OTA-Pulse gateway, enabling the \
Remote SSH terminal feature (TODO-002).  It loads a JSON config from \
/etc/otapulse/tunnel.conf and a per-device credential from /data/frp/credential \
(both update-safe paths), renders an frpc TOML config to /data/frp/frpc.toml \
(mode 0600), and launches frpc with exponential back-off restart supervision."
HOMEPAGE = "https://github.com/binaryblack/OTA-Pulse"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

# Runtime dependency: frpc must be present on the device.
RDEPENDS:${PN} = "frp"

# Build dependencies: only the Go cross-compiler; no C toolchain (CGO_ENABLED=0).
DEPENDS = "go-cross-${TUNE_PKGARCH}"

# Source: same externalsrc tree as the main OTA agent.
inherit externalsrc systemd goarch
EXTERNALSRC = "${THISDIR}/../../../soc-ota-agent"
EXTERNALSRC_BUILD = "${EXTERNALSRC}"

# Config files shipped alongside this recipe.
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

SRC_URI = " \
    file://soc-ota-tunneld.service \
    file://tunnel.conf.in \
"

# -----------------------------------------------------------------------
# Configurable recipe variables (set in local.conf or machine conf).
# -----------------------------------------------------------------------
# TUNNEL_SERVER_ADDR: hostname or IP of the frps gateway.  MUST be set.
TUNNEL_SERVER_ADDR ?= ""
# TUNNEL_SERVER_PORT: TCP port frps listens on for frpc connections.
TUNNEL_SERVER_PORT ?= "7000"

S = "${EXTERNALSRC}"

# Force CGO off — pure-Go static binary regardless of host CGO_ENABLED.
export CGO_ENABLED = "0"
export GO111MODULE = "on"

do_compile() {
    cd ${S}

    # Add go-cross bin directory to PATH (same pattern as soc-ota-agent recipe).
    export PATH="${STAGING_LIBDIR_NATIVE}/${TARGET_SYS}/go/bin:${PATH}"

    # Cross-compilation settings from goarch inherit.
    export GOOS="${TARGET_GOOS}"
    export GOARCH="${TARGET_GOARCH}"
    export GOARM="${TARGET_GOARM}"
    export GO386="${TARGET_GO386}"

    # Static build — no CGO, no external linker needed.
    export CGO_ENABLED="0"

    # Remove build system paths from binary for reproducibility.
    export GO_BUILD_FLAGS="-trimpath -mod=vendor"

    oe_runmake build-tunneld
}

do_install() {
    FILESDIR="${THISDIR}/files"

    # Install the static binary.
    install -d ${D}${bindir}
    install -m 0755 ${S}/soc-ota-tunneld ${D}${bindir}/soc-ota-tunneld

    # Install the config template, substituting recipe variables.
    install -d ${D}${sysconfdir}/otapulse
    sed -e "s|@SERVER_ADDR@|${TUNNEL_SERVER_ADDR}|g" \
        -e "s|@SERVER_PORT@|${TUNNEL_SERVER_PORT}|g" \
        ${FILESDIR}/tunnel.conf.in > ${D}${sysconfdir}/otapulse/tunnel.conf
    # Config embeds server address — restrict access.
    chmod 0600 ${D}${sysconfdir}/otapulse/tunnel.conf

    # Install systemd unit.
    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${FILESDIR}/soc-ota-tunneld.service \
        ${D}${systemd_system_unitdir}/soc-ota-tunneld.service

    # Create the update-safe /data/frp directory at image build time so the
    # daemon can write its credential and rendered config there on first boot.
    # The actual files are written at runtime; we only seed the directory.
    install -d -m 0700 ${D}/data/frp
}

# -----------------------------------------------------------------------
# systemd integration
# -----------------------------------------------------------------------
SYSTEMD_SERVICE:${PN} = "soc-ota-tunneld.service"
SYSTEMD_AUTO_ENABLE = "enable"

# -----------------------------------------------------------------------
# Package file lists
# -----------------------------------------------------------------------
FILES:${PN} = " \
    ${bindir}/soc-ota-tunneld \
    ${sysconfdir}/otapulse/tunnel.conf \
    ${systemd_system_unitdir}/soc-ota-tunneld.service \
    /data/frp \
"

CONFFILES:${PN} = " \
    ${sysconfdir}/otapulse/tunnel.conf \
"

# -----------------------------------------------------------------------
# QA / strip suppression (same rationale as soc-ota-agent recipe)
# -----------------------------------------------------------------------
# already-stripped: Go linker strips with -s -w in the Makefile target.
# buildpaths:       -trimpath removes most paths but some Go metadata remains.
INSANE_SKIP:${PN} = "already-stripped buildpaths"
INSANE_SKIP:${PN}-dbg = "buildpaths"

INHIBIT_PACKAGE_STRIP = "1"
INHIBIT_SYSROOT_STRIP = "1"
INHIBIT_PACKAGE_DEBUG_SPLIT = "1"

# Skip SPDX — externalsrc; same pattern as soc-ota-agent.
do_create_spdx[noexec] = "1"
do_create_runtime_spdx[noexec] = "1"

# Invalidate sstate when configurable vars change so a stale cached package
# is not reused when the server address changes between builds.
do_install[vardeps] += "TUNNEL_SERVER_ADDR TUNNEL_SERVER_PORT"
