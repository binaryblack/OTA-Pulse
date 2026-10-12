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

# BUG-232: otapulse-rockchip-bootenv used to be RRECOMMENDS'd here for Rockchip
# platforms. It installed an ArtifactCommit_* script into ${sysconfdir}/otapulse/scripts
# (RootfsScriptsPath) — a location the agent NEVER executes Artifact* transition
# scripts from (those come from ArtScriptsPath, populated FROM THE ARTIFACT; see
# installer/installer.go). Worse, CheckRootfsScriptsVersion (statescript/executor.go)
# fails EVERY commit when RootfsScriptsPath is non-empty but lacks a 'version' file,
# which this package never provided — so the script never ran AND broke every OTA
# commit on Rockchip boards. The PARTUUID/extlinux/etc. slot switch this package
# tried to do is already handled correctly and generically by
# u-boot-env-config's switch-boot-slot.sh (RRECOMMENDS'd universally above), which
# the agent invokes natively (installer/dual_rootfs_device.go switchBootSlot()) on
# install, and via ArtifactRollbackReboot_Enter_01 (staged below) on rollback.
# Removed entirely rather than fixed in place — see otapulse-rockchip-bootenv_1.0.bb
# for the (also fixed) package itself, kept as a documented no-op.

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
    file://ArtifactRollbackReboot_Enter_01 \
    file://ArtifactCommit_Enter_01 \
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

# ── Bootloader compat mode (TODO-157 / sprint S109) ──────────────────────────
# Two flavours of image share this recipe:
#   * native (default): the agent owns the boot-slot flag (file-based boot
#     environment, by-partlabel rootfs devices, switch-boot-slot.sh).
#   * bootloader: the bootloader's own environment (U-Boot env or GRUB grubenv)
#     owns the boot-slot flag and its fallback (bootlimit/altbootcmd).
# The flavour is selected by the OVERRIDE "otapulse-bootloader" (defined by
# otapulse-full.bbclass, TASK-S109-008; for tests use
# OVERRIDES:append = ":otapulse-bootloader" in local.conf).
#
# NATIVE OUTPUT IS BYTE-IDENTICAL to the pre-S109 recipe: every variable below
# resolves, in native mode, to exactly the literal text the template used to
# hardcode, no BootEnvMode key is written, no boot_mode file is installed and
# no systemd drop-in is installed. Consumers (state scripts, TASK-S109-010)
# treat an ABSENT /etc/otapulse/boot_mode as "native". The file exists only on
# bootloader images, where it holds the single word "bootloader".
OTAPULSE_BOOT_MODE = "native"
OTAPULSE_BOOT_MODE:otapulse-bootloader = "bootloader"

# Bootloader kind for bootloader mode: "uboot" or "grub". Set by the customer's
# choice (TASK-S109-008). Unused (and may stay empty) in native mode; REQUIRED
# and validated at parse time in bootloader mode.
OTAPULSE_BOOTLOADER ?= ""

# Root filesystem devices written to RootfsPartA/B. Native: by-partlabel (GPT
# labels created by otapulse-partition-setup). Bootloader mode: the root
# filesystem partition variables of the board integration layer.
OTAPULSE_ROOTFS_PART_A = "/dev/disk/by-partlabel/rootfs_a"
OTAPULSE_ROOTFS_PART_B = "/dev/disk/by-partlabel/rootfs_b"
OTAPULSE_ROOTFS_PART_A:otapulse-bootloader = "${MENDER_ROOTFS_PART_A}"
OTAPULSE_ROOTFS_PART_B:otapulse-bootloader = "${MENDER_ROOTFS_PART_B}"

# device_type written to /etc/otapulse/device_type and /var/lib/otapulse/
# device_type. It is the string artifacts are checked against, so bootloader
# images must carry the board integration's device type. Native: ${MACHINE}.
OTAPULSE_DEVICE_TYPE = "${MACHINE}"
OTAPULSE_DEVICE_TYPE:otapulse-bootloader = "${MENDER_DEVICE_TYPE}"

# Bootloader-environment tools the agent needs at runtime (fw_printenv /
# fw_setenv for U-Boot, grub-editenv + the grubenv layout for GRUB).
RDEPENDS:${PN}:append:otapulse-bootloader = " ${@'libubootenv-bin' if d.getVar('OTAPULSE_BOOTLOADER') == 'uboot' else ('grub-editenv grub-mender-grubenv' if d.getVar('OTAPULSE_BOOTLOADER') == 'grub' else '')}"

# The native boot-switching helpers (switch-boot-slot.sh, partition tools) are
# useless in bootloader mode (the bootloader owns the slot flag; the agent never
# runs switch-boot-slot.sh there) and u-boot-env-config would drag a second,
# conflicting U-Boot environment definition (fw_env.config) into the image.
#  * gptfdisk (a core recipe): this removal is its only gate.
#  * u-boot-env-config: the GATE is otapulse-full-bootloader.bbclass's BBMASK of the
#    whole recipe (TASK-S109-009 reconciliation: one mechanism, so anything else that
#    asks for it, e.g. an image listing it, fails with "Nothing PROVIDES"). Removing it
#    from THIS recommends list is that mask's required companion, not a second gate:
#    verified in TASK-S109-009, bitbake reports a recommendation of a masked recipe as an
#    ERROR ("Nothing RPROVIDES 'u-boot-env-config'"), it does not skip it.
RRECOMMENDS:${PN}:remove:otapulse-bootloader = "u-boot-env-config gptfdisk"

# Bootloader mode only: grow /data (partition + file system) to the medium before
# the agent starts, see the otapulse-growfs-data recipe. Customers can opt out with
# OTAPULSE_GROW_DATA_PART = "0" (documented in otapulse-full.bbclass); a layout
# whose /data is not the last partition is left alone by the unit itself.
OTAPULSE_GROW_DATA_PART ?= "1"
RDEPENDS:${PN}:append:otapulse-bootloader = " ${@'' if d.getVar('OTAPULSE_GROW_DATA_PART') == '0' else 'otapulse-growfs-data'}"

# Unit ordering drop-in: installed in bootloader mode only (see do_install).
SRC_URI:append:otapulse-bootloader = " file://10-bootloader-mode.conf"
FILES:${PN}:append:otapulse-bootloader = " ${systemd_system_unitdir}/soc-ota-agent.service.d"

python () {
    # Fail early and clearly (parse time) instead of shipping an image whose
    # agent cannot find its bootloader environment.
    if d.getVar('OTAPULSE_BOOT_MODE') != 'bootloader':
        return
    bl = d.getVar('OTAPULSE_BOOTLOADER') or ''
    if bl not in ('uboot', 'grub'):
        bb.fatal("soc-ota-agent: bootloader mode (override otapulse-bootloader) requires "
                 "OTAPULSE_BOOTLOADER to be 'uboot' or 'grub' (got '%s')" % bl)
    for var, what in (('MENDER_ROOTFS_PART_A', 'root filesystem partition A'),
                      ('MENDER_ROOTFS_PART_B', 'root filesystem partition B'),
                      ('MENDER_DEVICE_TYPE', 'device type')):
        if not (d.getVar(var) or '').strip():
            bb.fatal("soc-ota-agent: bootloader mode requires the %s to be defined by the "
                     "board integration layer (%s is empty or unset); is the vendored "
                     "board-integration layer (meta-otapulse-bsp/core) in bblayers.conf?"
                     % (what, var))
    # These values are substituted into otapulse.conf with sed ("s|...|...|g").
    for var in ('MENDER_ROOTFS_PART_A', 'MENDER_ROOTFS_PART_B', 'MENDER_DEVICE_TYPE'):
        val = d.getVar(var) or ''
        if any(c in val for c in '|&\\'):
            bb.fatal("soc-ota-agent: %s must not contain '|', '&' or a backslash (got '%s')"
                     % (var, val))
}

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
    # TODO-011/S102-001: report the real agent version (git describe), not the
    # recipe PV, so the fleet gate can see which agents have rolled. Only v*
    # tags count (release tags such as otapulse-artifact-v* must not leak in).
    AGENT_VERSION="$(git -C ${S} describe --tags --always --dirty --match 'v[0-9]*' 2>/dev/null || echo ${PV})"
    bbnote "soc-ota-agent version: ${AGENT_VERSION}"
    oe_runmake build VERSION="${AGENT_VERSION}"
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
        # Signature verification enabled - include ArtifactVerifyKeys AND
        # RequireArtifactVerification. The latter (GAP-SEC-F1) makes runtime
        # enforcement fail-closed independent of stock Mender's own reader
        # behavior: if ArtifactVerifyKeys ever resolves empty at runtime
        # (mis-templated config, tampered file, future refactor), the agent
        # hard-fails GetVerificationKeys instead of silently accepting an
        # unsigned artifact. See soc-ota-agent/conf/config.go:385-421.
        # Build JSON array of verification key paths
        VERIFY_KEY_FILES="${SOC_OTA_VERIFY_KEY_FILES}"
        KEYS_JSON=""
        for keyfile in ${VERIFY_KEY_FILES}; do
            if [ -n "${KEYS_JSON}" ]; then
                KEYS_JSON="${KEYS_JSON}, "
            fi
            KEYS_JSON="${KEYS_JSON}\"${SIGNING_KEYS_DIR}/active/${keyfile}\""
        done
        VERIFY_KEYS_LINE=",\n\n    \"ArtifactVerifyKeys\": [${KEYS_JSON}],\n    \"RequireArtifactVerification\": true"
    else
        # Signature verification disabled (SOC_OTA_SIGNATURE_VERIFICATION=0): no
        # ArtifactVerifyKeys/RequireArtifactVerification lines. Since GC-AG-07 the
        # agent REFUSES installs when no verify key is configured unless the
        # operator opts in explicitly, so this explicitly-unsigned dev/QEMU build
        # must say so: AllowUnsignedArtifacts=true. (otapulse-sanity.bbclass
        # already blocks this combination for 'inherit otapulse' images unless
        # OTAPULSE_ALLOW_UNSIGNED=1.) The verification-enabled branch above does
        # NOT emit this flag, so production output is unchanged.
        VERIFY_KEYS_LINE=",\n\n    \"AllowUnsignedArtifacts\": true"
        bbwarn "soc-ota-agent: SOC_OTA_SIGNATURE_VERIFICATION=0 - otapulse.conf gets AllowUnsignedArtifacts=true; artifacts install WITHOUT authentication (dev/QEMU only)"
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

    # Boot-environment mode (TODO-157/S109). Native: the template's historical
    # literals (no BootEnvMode key at all, file-based boot env, by-partlabel
    # devices) so the rendered file is byte-identical to the pre-S109 one.
    # Bootloader: BootEnvMode=bootloader, bootloader-owned boot env, and the
    # board integration's root filesystem devices. The block's trailing
    # "\n    " keeps the following key on its own indented line.
    if [ "${OTAPULSE_BOOT_MODE}" = "bootloader" ]; then
        BOOT_ENV_MODE_BLOCK='"BootEnvMode": "bootloader",\n    '
        USE_FILE_BOOT_ENV="false"
    else
        BOOT_ENV_MODE_BLOCK=""
        USE_FILE_BOOT_ENV="true"
    fi
    ROOTFS_PART_A="${OTAPULSE_ROOTFS_PART_A}"
    ROOTFS_PART_B="${OTAPULSE_ROOTFS_PART_B}"

    # Process the template and install as otapulse.conf
    sed -e "s|@SERVER_URL@|${SERVER_URL}|g" \
        -e "s|@SKIP_TLS_VERIFY@|${SKIP_VERIFY}|g" \
        -e "s|@TENANT_TOKEN@|${TENANT_TOKEN}|g" \
        -e "s|@ARTIFACT_VERIFICATION_BLOCK@|${VERIFY_KEYS_LINE}|g" \
        -e "s|@BOOT_ENV_MODE_BLOCK@|${BOOT_ENV_MODE_BLOCK}|g" \
        -e "s|@USE_FILE_BOOT_ENV@|${USE_FILE_BOOT_ENV}|g" \
        -e "s|@ROOTFS_PART_A@|${ROOTFS_PART_A}|g" \
        -e "s|@ROOTFS_PART_B@|${ROOTFS_PART_B}|g" \
        ${FILESDIR}/otapulse.conf.in > ${D}${sysconfdir}/otapulse/otapulse.conf

    chmod 0600 ${D}${sysconfdir}/otapulse/otapulse.conf

    # Generate device_type file dynamically from MACHINE variable
    # This ensures the device_type matches the mender artifact device type
    # (OTAPULSE_DEVICE_TYPE is ${MACHINE} natively; the board integration's
    # device type in bootloader mode.) otapulse-partition-setup seeds
    # /data/otapulse/device_type from this file.
    echo "device_type=${OTAPULSE_DEVICE_TYPE}" > ${D}${sysconfdir}/otapulse/device_type
    chmod 0444 ${D}${sysconfdir}/otapulse/device_type

    # Bootloader mode only: mark the image so consumers (state scripts, boot
    # health) can tell the two flavours apart. Deliberately NOT written in
    # native mode - an absent file means native.
    if [ "${OTAPULSE_BOOT_MODE}" = "bootloader" ]; then
        echo "bootloader" > ${D}${sysconfdir}/otapulse/boot_mode
        chmod 0444 ${D}${sysconfdir}/otapulse/boot_mode
    fi

    # Install systemd service
    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${FILESDIR}/soc-ota-agent.service ${D}${systemd_system_unitdir}/

    # Bootloader mode only: the native-only otapulse-partition-setup service may
    # be gated or absent (TASK-S109-009), and a missing Requires= target makes
    # the agent fail to start ("Unit otapulse-partition-setup.service not
    # found"). A drop-in cannot fix that: on systemd 255 an empty "Requires=" in
    # a drop-in does NOT reset the base unit's list. So the installed copy of
    # the base unit loses the service from its Requires= line(s) (After= is kept:
    # while the service is installed the agent must still start after it). The
    # source unit and the native install stay byte-identical. The sed must work
    # whether the service is alone or among other units on the line, and the
    # build FAILS if it changes nothing, so a change of the base unit can never
    # silently bring the dependency back.
    if [ "${OTAPULSE_BOOT_MODE}" = "bootloader" ]; then
        UNIT=${D}${systemd_system_unitdir}/soc-ota-agent.service
        sed -i -E '/^Requires=/ s/^(Requires=([^[:space:]]+[[:space:]]+)*)otapulse-partition-setup\.service[[:space:]]*/\1/' "$UNIT"
        if grep -E '^Requires=.*otapulse-partition-setup' "$UNIT" >/dev/null; then
            bbfatal "soc-ota-agent: bootloader mode could not remove otapulse-partition-setup.service from the Requires= line of soc-ota-agent.service (base unit changed?); the agent would not start without that service"
        fi
        if cmp -s "$UNIT" ${FILESDIR}/soc-ota-agent.service; then
            bbfatal "soc-ota-agent: bootloader mode left soc-ota-agent.service unchanged: the Requires=otapulse-partition-setup.service line was not found (base unit changed?)"
        fi
        if ! grep -E '^After=.*otapulse-partition-setup\.service' "$UNIT" >/dev/null; then
            bbfatal "soc-ota-agent: bootloader mode expects soc-ota-agent.service to keep After=otapulse-partition-setup.service"
        fi

        # Order the agent after the /data mount (the bootloader env config
        # lives there).
        install -d ${D}${systemd_system_unitdir}/soc-ota-agent.service.d
        install -m 0644 ${FILESDIR}/10-bootloader-mode.conf ${D}${systemd_system_unitdir}/soc-ota-agent.service.d/10-bootloader-mode.conf
    fi

    # Create data directory (new OTAPulse path)
    install -d ${D}${localstatedir}/lib/otapulse

    # Generate device_type in data directory as well (agent looks for it here)
    echo "device_type=${OTAPULSE_DEVICE_TYPE}" > ${D}${localstatedir}/lib/otapulse/device_type
    chmod 0444 ${D}${localstatedir}/lib/otapulse/device_type

    # Create scripts directory
    install -d ${D}${sysconfdir}/otapulse/scripts

    # Stage OTA state scripts for embedding into the .otapulse artifact.
    # otapulse-artifact.bbclass scans ${datadir}/otapulse/state-scripts and passes
    # each via `otapulse-artifact write --script`. This is REQUIRED for Artifact*
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

    # BUG-232: ArtifactRollbackReboot_Enter_01 mirrors switchBootSlot() (called
    # natively by InstallUpdate() for the forward path, see
    # installer/dual_rootfs_device.go) for the ROLLBACK path, which Rollback()
    # does not call. Self-gates on /usr/sbin/switch-boot-slot.sh being present
    # and on there being a recorded rollback target; see the script header.
    install -m 0755 ${FILESDIR}/ArtifactRollbackReboot_Enter_01 ${D}${datadir}/otapulse/state-scripts/ArtifactRollbackReboot_Enter_01

    # BUG-245: ArtifactCommit_Enter_01 makes the RPi4/Dragon Q6A slot switch
    # permanent ONLY after the new slot has booted and the agent has decided
    # to commit (i.e. this state fires). ArtifactReboot_Enter_01's RPi4
    # section 1 no longer rewrites the permanent cmdline.txt itself -- it
    # boots the new slot via the firmware tryboot one-shot instead, so an
    # unverified/failed try never touches the permanent config at all.
    install -m 0755 ${FILESDIR}/ArtifactCommit_Enter_01 ${D}${datadir}/otapulse/state-scripts/ArtifactCommit_Enter_01

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

    # TODO-058: Apache-2.0 attribution. Ship the NOTICE file and the go-licenses
    # third-party bundle (soc-ota-agent/scripts/generate-third-party-licenses.sh)
    # on-device under ${datadir}/licenses/soc-ota-agent/ so shipped images carry
    # the upstream (Northern.tech/Mender) and vendored-module license texts.
    # NOTICE lives at the ota-pulse repo root, one level above the agent source.
    install -d ${D}${datadir}/licenses/soc-ota-agent
    install -m 0644 ${THISDIR}/../../../NOTICE ${D}${datadir}/licenses/soc-ota-agent/NOTICE
    install -m 0644 ${S}/third_party_licenses.csv ${D}${datadir}/licenses/soc-ota-agent/third_party_licenses.csv
    cp -R --no-preserve=ownership ${S}/third_party_licenses ${D}${datadir}/licenses/soc-ota-agent/
    find ${D}${datadir}/licenses/soc-ota-agent -type d -exec chmod 0755 {} +
    find ${D}${datadir}/licenses/soc-ota-agent -type f -exec chmod 0644 {} +

    # Log signature verification status
    if [ "${SIG_VERIFY}" = "1" ]; then
        bbwarn "OTA signature verification is ENABLED"
        bbwarn "Public keys directory: ${SIGNING_KEYS_DIR}/active/"
    else
        bbnote "OTA signature verification is DISABLED"
    fi
}

# Bootloader mode (TASK-S109-009): the image must not ship anything named "mender".
# The native do_install above installs the /usr/bin/mender compatibility symlink
# (and an inventory script that calls it); this append runs after it, only in
# bootloader mode, so the native task body and its signature stay exactly as they
# were. Nothing on a bootloader image uses the name: the agent, the state scripts
# and the testing framework all call soc-ota-agent / otapulse.
do_install:append:otapulse-bootloader() {
    rm -f ${D}${bindir}/mender

    # otapulse-inventory-provides calls "/usr/bin/mender show-provides"; point the
    # installed copy at the agent binary (same command, same output). The source
    # script is shared with native images and is not modified.
    INV=${D}${datadir}/otapulse/inventory/otapulse-inventory-provides
    sed -i 's|/usr/bin/mender show-provides|/usr/bin/soc-ota-agent show-provides|' "$INV"
    if grep -q '/usr/bin/mender' "$INV" || ! grep -q '/usr/bin/soc-ota-agent show-provides' "$INV"; then
        bbfatal "soc-ota-agent: bootloader mode could not repoint otapulse-inventory-provides at /usr/bin/soc-ota-agent (source script changed?)"
    fi
}
FILES:${PN}:remove:otapulse-bootloader = "${bindir}/mender"

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
    ${datadir}/licenses/soc-ota-agent \
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
do_install[vardeps] += "OTA_SERVER_URL OTAPULSE_TENANT_TOKEN SOC_OTA_SIGNATURE_VERIFICATION SOC_OTA_VERIFY_KEY_FILES SOC_OTA_SIGNING_KEYS_DIR SOC_OTA_TLS_SKIP_VERIFY OTAPULSE_BOOT_MODE OTAPULSE_BOOTLOADER OTAPULSE_ROOTFS_PART_A OTAPULSE_ROOTFS_PART_B OTAPULSE_DEVICE_TYPE"

# Ensure SPDX tasks don't fail even when SPDX generation is enabled globally
# This is needed for compatibility with builds that have INHERIT += "create-spdx"
SPDX_INCLUDE_SOURCES = "0"
