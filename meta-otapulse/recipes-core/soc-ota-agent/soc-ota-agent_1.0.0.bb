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
"

# Only depend on signing-keys if signature verification is enabled
RDEPENDS:${PN}:append = " ${@bb.utils.contains('SOC_OTA_SIGNATURE_VERIFICATION', '1', 'signing-keys', '', d)}"

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
"

# Signature verification configuration
SOC_OTA_SIGNATURE_VERIFICATION ?= "0"
SOC_OTA_SIGNING_KEYS_DIR ?= "/etc/soc-monitoring/signing-keys"
# Verification keys to include (space-separated list of key filenames)
# Example: "production-rsa-public.pem production-ecdsa-public.pem"
SOC_OTA_VERIFY_KEY_FILES ?= "production-rsa-public.pem"
# OTA server URL - MUST be set in your platform-specific layer or local.conf
OTA_SERVER_URL ?= "https://ota.example.com"

S = "${EXTERNALSRC}"

# Build configuration
export CGO_ENABLED = "1"
export GO111MODULE = "on"

do_compile() {
    cd ${S}

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
    export GO_BUILD_FLAGS="-trimpath"

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
        SKIP_VERIFY="false"
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
        SKIP_VERIFY="true"
        VERIFY_KEYS_LINE=""
    fi

    # Process the template and install as otapulse.conf
    sed -e "s|@SERVER_URL@|${SERVER_URL}|g" \
        -e "s|@SKIP_TLS_VERIFY@|${SKIP_VERIFY}|g" \
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

    # Install identity script (to new OTAPulse path with new naming)
    install -d ${D}${datadir}/otapulse/identity
    install -m 0755 ${S}/support/otapulse-device-identity ${D}${datadir}/otapulse/identity/otapulse-device-identity

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

# Ensure SPDX tasks don't fail even when SPDX generation is enabled globally
# This is needed for compatibility with builds that have INHERIT += "create-spdx"
SPDX_INCLUDE_SOURCES = "0"
