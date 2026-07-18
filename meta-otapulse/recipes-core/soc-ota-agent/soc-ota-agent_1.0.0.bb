# Yocto recipe for OTAPulse (white-labeled from Mender client)
# Provides firmware OTA updates with signature verification

SUMMARY = "OTAPulse - White-labeled OTA client for firmware updates"
DESCRIPTION = "OTAPulse is a white-labeled OTA update client (based on Mender) \
that integrates with SoC Monitoring backend using X-API-Key authentication. \
Supports firmware signature verification with RSA-4096 and ECDSA-P384 algorithms."
HOMEPAGE = "https://github.com/binaryblack/OTA-Pulse"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

# Dependencies
DEPENDS = " \
    openssl \
    xz \
    lmdb \
    glib-2.0 \
    go-cross-${TUNE_PKGARCH} \
    pkgconfig-native \
"

RDEPENDS:${PN} = " \
    openssl \
    ca-certificates \
    bash \
    otapulse-firstboot \
"

# Boot switching infrastructure - recommended for A/B OTA support
# These packages provide switch-boot-slot.sh and partition tools
# Platforms can override or omit if using different boot switching methods
RRECOMMENDS:${PN} = " \
    u-boot-env-config \
    gptfdisk \
"

# Only depend on signing-keys if signature verification is enabled
RDEPENDS:${PN}:append = " ${@bb.utils.contains('SOC_OTA_SIGNATURE_VERIFICATION', '1', 'signing-keys', '', d)}"

# For Rockchip platforms, include the PARTUUID-based boot switching state script
RRECOMMENDS:${PN}:append:rockchip = " otapulse-rockchip-bootenv"

# Use externalsrc for local Go project
inherit externalsrc systemd goarch
EXTERNALSRC = "${THISDIR}/../../../soc-ota-agent"
EXTERNALSRC_BUILD = "${EXTERNALSRC}"

# Configuration files from files/ directory
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

SRC_URI = " \
    file://otapulse.conf.in \
    file://soc-ota-agent.service \
    file://device_type \
    file://soc-ota-provision \
    file://ArtifactReboot_Enter_01 \
"

# Signature verification configuration.
# Default "0" here only sets the agent's runtime config value. At build time,
# otapulse-sanity.bbclass's 'signing' check (GAP-OTA-002) makes an image build
# FAIL when this is "0", unless OTAPULSE_ALLOW_UNSIGNED="1" is set as an
# explicit dev escape hatch — a stock image must not silently ship unsigned.
SOC_OTA_SIGNATURE_VERIFICATION ?= "0"
SOC_OTA_SIGNING_KEYS_DIR ?= "/etc/soc-monitoring/signing-keys"
# Verification keys to include (space-separated list of key filenames)
# Example: "production-rsa-public.pem production-ecdsa-public.pem"
SOC_OTA_VERIFY_KEY_FILES ?= "production-rsa-public.pem"

# TLS verification is independent of artifact signature verification (GAP-OTA-001) -
# defaults ON regardless of SOC_OTA_SIGNATURE_VERIFICATION. Only set to "1" for an
# explicit self-signed dev setup where the OTA server's TLS cert isn't in the device's
# trust store.
SOC_OTA_TLS_SKIP_VERIFY ?= "0"
# OTA server URL - MUST be set in your platform-specific layer or local.conf
OTA_SERVER_URL ?= "http://192.168.0.123:8000"

# Tenant token / API key for device authentication
# Set this in local.conf to an API key (e.g., "smk_...") for the device to authenticate
# with the OTA server. Leave empty if using runtime provisioning.
OTAPULSE_TENANT_TOKEN ?= ""

S = "${EXTERNALSRC}"

# Prevent base_do_configure from running 'make clean' when the configure task
# hash changes.  soc-ota-tunneld shares this EXTERNALSRC tree; a 'make clean'
# from either recipe would delete build artifacts that the other recipe's
# do_install still needs.
CLEANBROKEN = "1"

# Build configuration
export CGO_ENABLED = "1"
export GO111MODULE = "on"

do_compile() {
    cd ${S}

    # externalsrc builds in-place in a shared source tree, and the Makefile's
    # 'build' target is a file target (build: $(BINARY_NAME)). A stale 'otapulse'
    # binary left in the tree by a previous board's build makes make report
    # "Nothing to be done for 'build'", so do_compile silently ships that stale
    # binary — linked against whichever sysroot compiled it first — to every
    # board. On Tegra/Kirkstone (glibc 2.35) a binary left by a Scarthgap build
    # (glibc 2.39) requires GLIBC_2.38 and fails do_package_qa [file-rdeps].
    # Remove any stale artifact so the agent is always recompiled against THIS
    # board's sysroot. GOCACHE is pinned per-recipe (below) for the same reason.
    rm -f ${S}/otapulse ${S}/mender

    # Isolate the Go build cache per build so cgo objects compiled for one
    # target's sysroot are never reused for another's.
    export GOCACHE="${WORKDIR}/go-build-cache"

    # Add go-cross bin directory to PATH
    export PATH="${STAGING_LIBDIR_NATIVE}/${TARGET_SYS}/go/bin:${PATH}"

    # Configure Go for cross-compilation
    export GOOS="${TARGET_GOOS}"
    export GOARCH="${TARGET_GOARCH}"
    export GOARM="${TARGET_GOARM}"
    export GO386="${TARGET_GO386}"
    export CGO_ENABLED="1"

    # Set cross-compiler for CGO
    export CC="${CC}"
    export CXX="${CXX}"
    export AR="${AR}"

    # Add warning suppressions needed by lmdb library
    export CGO_CFLAGS="${CFLAGS} -Wno-implicit-fallthrough -Wno-stringop-overflow"
    export CGO_CXXFLAGS="${CXXFLAGS}"
    export CGO_LDFLAGS="${LDFLAGS}"

    # Go build flags to fix QA warnings
    # -trimpath: Remove build paths from binary (fixes buildpaths warning)
    # -mod=vendor: use vendor/ directory; avoids GOPROXY network access
    export GO_BUILD_FLAGS="-trimpath -mod=vendor"

    # Extra linker flags: strip symbols, use external linker
    export EXTRA_GO_LDFLAGS="-s -w -linkmode=external -extldflags '${LDFLAGS}'"

    # Build using the Makefile
    oe_runmake build VERSION="${PV}"
}

do_install() {
    # Phase 1 White-Label Migration: Install to /etc/otapulse, /var/lib/otapulse, /usr/share/otapulse
    # The agent code has fallback logic to /etc/mender paths for backward compatibility
    # Binary is now built as 'otapulse' (see go.mod and Makefile changes)

    # With externalsrc, config files are in THISDIR/files not WORKDIR
    FILESDIR="${THISDIR}/files"

    # Install binary (now built as 'otapulse')
    install -d ${D}${bindir}
    install -m 0755 ${S}/otapulse ${D}${bindir}/soc-ota-agent

    # Create backward compatibility symlinks
    ln -sf soc-ota-agent ${D}${bindir}/otapulse
    ln -sf soc-ota-agent ${D}${bindir}/mender

    # Install provisioning script
    install -m 0755 ${FILESDIR}/soc-ota-provision ${D}${bindir}/soc-ota-provision

    # Install configuration to new OTAPulse directory
    # Code has fallback logic to /etc/mender for backward compatibility
    install -d ${D}${sysconfdir}/otapulse

    # Generate otapulse.conf from template with signature verification settings
    SIGNING_KEYS_DIR="${SOC_OTA_SIGNING_KEYS_DIR}"
    SIG_VERIFY="${SOC_OTA_SIGNATURE_VERIFICATION}"
    SERVER_URL="${OTA_SERVER_URL}"

    # Determine signature verification settings
    if [ "${SIG_VERIFY}" = "1" ]; then
        # Signature verification enabled - include ArtifactVerifyKeys line
        # Build JSON array of verification key paths
        VERIFY_KEY_FILES="${SOC_OTA_VERIFY_KEY_FILES}"
        KEYS_JSON=""
        for keyfile in ${VERIFY_KEY_FILES}; do
            if [ -n "${KEYS_JSON}" ]; then
                KEYS_JSON="${KEYS_JSON}, "
            fi
            KEYS_JSON="${KEYS_JSON}\"${SIGNING_KEYS_DIR}/active/${keyfile}\""
        done
        VERIFY_KEYS_LINE=",\n\n    \"ArtifactVerifyKeys\": [${KEYS_JSON}]"
    else
        # Signature verification disabled - no ArtifactVerifyKeys line
        VERIFY_KEYS_LINE=""
    fi

    # TLS server-certificate verification (GAP-OTA-001): independent of artifact
    # signature verification above. Defaults to verifying the OTA server's TLS cert
    # regardless of SIG_VERIFY; only an explicit SOC_OTA_TLS_SKIP_VERIFY = "1" (dev
    # self-signed setups) disables it.
    TLS_SKIP_VERIFY="${SOC_OTA_TLS_SKIP_VERIFY}"
    if [ "${TLS_SKIP_VERIFY}" = "1" ]; then
        SKIP_VERIFY="true"
        bbwarn "OTA TLS certificate verification is DISABLED (SOC_OTA_TLS_SKIP_VERIFY=1) - dev/self-signed use only"
    else
        SKIP_VERIFY="false"
    fi

    # Tenant token for device authentication
    TENANT_TOKEN="${OTAPULSE_TENANT_TOKEN}"

    # Process the template and install as otapulse.conf
    sed -e "s|@SERVER_URL@|${SERVER_URL}|g" \
        -e "s|@SKIP_TLS_VERIFY@|${SKIP_VERIFY}|g" \
        -e "s|@TENANT_TOKEN@|${TENANT_TOKEN}|g" \
        -e "s|@ARTIFACT_VERIFY_KEYS_LINE@|${VERIFY_KEYS_LINE}|g" \
        ${FILESDIR}/otapulse.conf.in > ${D}${sysconfdir}/otapulse/otapulse.conf

    chmod 0600 ${D}${sysconfdir}/otapulse/otapulse.conf

    # Generate device_type file dynamically from MACHINE variable
    # This ensures the device_type matches the mender artifact device type
    echo "device_type=${MACHINE}" > ${D}${sysconfdir}/otapulse/device_type
    chmod 0444 ${D}${sysconfdir}/otapulse/device_type

    # Install systemd service
    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${FILESDIR}/soc-ota-agent.service ${D}${systemd_system_unitdir}/

    # Create data directory (new OTAPulse path)
    install -d ${D}${localstatedir}/lib/otapulse

    # Generate device_type in data directory as well (agent looks for it here)
    echo "device_type=${MACHINE}" > ${D}${localstatedir}/lib/otapulse/device_type
    chmod 0444 ${D}${localstatedir}/lib/otapulse/device_type

    # Create scripts directory
    install -d ${D}${sysconfdir}/otapulse/scripts

    # Stage Mender state scripts for embedding into the .mender artifact.
    # mender-artifact.bbclass scans ${datadir}/otapulse/state-scripts and passes
    # each via `mender-artifact write --script`. This is REQUIRED for Artifact*
    # transition scripts (ArtifactReboot_Enter/_Leave, ArtifactCommit_*) to run on
    # device during an OTA — Mender takes those scripts from the ARTIFACT, not the
    # rootfs, so a rootfs-only install would never execute them.
    # ArtifactReboot_Enter_01 self-gates: on RPi4 (VideoCore direct boot) it issues
    # `reboot "0 tryboot"` when /boot/tryboot.txt exists; on U-Boot boards it syncs
    # /data/ota/mender_boot_part to the FAT boot partition; otherwise it is a no-op.
    # NOTE: externalsrc recipe — SRC_URI files are referenced from ${FILESDIR}
    # (${THISDIR}/files), not ${WORKDIR}, exactly like otapulse.conf.in/soc-ota-provision above.
    install -d ${D}${datadir}/otapulse/state-scripts
    install -m 0755 ${FILESDIR}/ArtifactReboot_Enter_01 ${D}${datadir}/otapulse/state-scripts/ArtifactReboot_Enter_01

    # Install identity script (to new OTAPulse path with new naming)
    install -d ${D}${datadir}/otapulse/identity
    install -m 0755 ${S}/support/otapulse-device-identity ${D}${datadir}/otapulse/identity/otapulse-device-identity
    # Backward compatibility: agent binary may look for mender-device-identity
    ln -sf otapulse-device-identity ${D}${datadir}/otapulse/identity/mender-device-identity

    # Install inventory scripts (to new OTAPulse path with new naming - Phase 3.2)
    install -d ${D}${datadir}/otapulse/inventory
    install -m 0755 ${S}/support/otapulse-inventory-bootloader-integration ${D}${datadir}/otapulse/inventory/
    install -m 0755 ${S}/support/otapulse-inventory-hostinfo ${D}${datadir}/otapulse/inventory/
    install -m 0755 ${S}/support/otapulse-inventory-network ${D}${datadir}/otapulse/inventory/
    install -m 0755 ${S}/support/otapulse-inventory-os ${D}${datadir}/otapulse/inventory/
    install -m 0755 ${S}/support/otapulse-inventory-provides ${D}${datadir}/otapulse/inventory/
    install -m 0755 ${S}/support/otapulse-inventory-rootfs-type ${D}${datadir}/otapulse/inventory/
    install -m 0755 ${S}/support/otapulse-inventory-update-modules ${D}${datadir}/otapulse/inventory/

    # Log signature verification status
    if [ "${SIG_VERIFY}" = "1" ]; then
        bbwarn "OTA signature verification is ENABLED"
        bbwarn "Public keys directory: ${SIGNING_KEYS_DIR}/active/"
    else
        bbnote "OTA signature verification is DISABLED"
    fi
}

# Systemd configuration
SYSTEMD_SERVICE:${PN} = "soc-ota-agent.service"
SYSTEMD_AUTO_ENABLE = "enable"

FILES:${PN} = " \
    ${bindir}/soc-ota-agent \
    ${bindir}/otapulse \
    ${bindir}/mender \
    ${bindir}/soc-ota-provision \
    ${sysconfdir}/otapulse \
    ${localstatedir}/lib/otapulse \
    ${systemd_system_unitdir}/soc-ota-agent.service \
    ${datadir}/otapulse \
"

CONFFILES:${PN} = " \
    ${sysconfdir}/otapulse/otapulse.conf \
    ${sysconfdir}/otapulse/device_type \
"

# Don't allow empty package - we need the binary
ALLOW_EMPTY:${PN} = "0"

# Increase task resource limits for build
TASK_maxmem = "8192"

# Skip QA checks that are known issues with CGO/Go binaries
# textrel: CGO binaries may have text relocations (common with external C libraries)
# buildpaths: Go may embed paths even with -trimpath when using CGO
# already-stripped: Go linker strips the binary with -s -w flags
INSANE_SKIP:${PN} = "textrel buildpaths already-stripped"
INSANE_SKIP:${PN}-dbg = "buildpaths"

# Inhibit Yocto's stripping since Go already strips with -s -w
INHIBIT_PACKAGE_STRIP = "1"
INHIBIT_SYSROOT_STRIP = "1"
INHIBIT_PACKAGE_DEBUG_SPLIT = "1"

# Skip SPDX generation - not compatible with externalsrc
# Using noexec instead of deltask to maintain task chain integrity
# This prevents "Unable to find SPDX provider" errors during image builds
do_create_spdx[noexec] = "1"
do_create_runtime_spdx[noexec] = "1"

# Track config variables in do_install hash so sstate is invalidated when they change.
# Without this, BitBake cannot detect shell variable changes in do_install and will
# reuse a stale cached package even when OTA_SERVER_URL or credentials change.
do_install[vardeps] += "OTA_SERVER_URL OTAPULSE_TENANT_TOKEN SOC_OTA_SIGNATURE_VERIFICATION SOC_OTA_VERIFY_KEY_FILES SOC_OTA_SIGNING_KEYS_DIR SOC_OTA_TLS_SKIP_VERIFY"

# Ensure SPDX tasks don't fail even when SPDX generation is enabled globally
# This is needed for compatibility with builds that have INHERIT += "create-spdx"
SPDX_INCLUDE_SOURCES = "0"
