################################################################################
#
# otapulse - OTA Update Agent for Embedded Linux
#
################################################################################

OTAPULSE_VERSION = 1.0.0
OTAPULSE_SITE = $(BR2_EXTERNAL_OTAPULSE_PATH)/../soc-ota-agent
OTAPULSE_SITE_METHOD = local
OTAPULSE_LICENSE = Apache-2.0
OTAPULSE_LICENSE_FILES = LICENSE

OTAPULSE_GOMOD = github.com/binaryblack/OTA-Pulse

OTAPULSE_DEPENDENCIES = host-go host-pkgconf host-mender-artifact openssl ca-certificates util-linux e2fsprogs dosfstools gptfdisk xz

ifeq ($(BR2_PACKAGE_OTAPULSE_DBUS),y)
OTAPULSE_DEPENDENCIES += dbus libglib2
endif

ifeq ($(BR2_PACKAGE_OTAPULSE_BOOT_UBOOT_ENV),y)
OTAPULSE_DEPENDENCIES += uboot-tools
endif

ifeq ($(BR2_PACKAGE_OTAPULSE_BOOT_GRUB),y)
OTAPULSE_DEPENDENCIES += grub2
endif

# soc-ota-tunneld (built below) execs frpc as a child process at runtime —
# same runtime dependency as meta-otapulse's soc-ota-tunneld recipe's
# RDEPENDS:${PN} = "frp". sudo is required for the shell-access sudoers.d
# allowlist (BUG-215) to be anything but inert — RDEPENDS:${PN} = "sudo"
# on the Yocto side (soc-shell-access_1.0.0.bb). Depending on sudo here
# (not just selecting BR2_PACKAGE_SUDO in Config.in) also guarantees sudo's
# own install step — which removes its dist example sudoers.d files — runs
# BEFORE our OTAPULSE_INSTALL_SHELL_ACCESS hook below, so our drop-in is
# never raced by sudo's post-install cleanup.
ifeq ($(BR2_PACKAGE_OTAPULSE_REMOTE_SSH),y)
OTAPULSE_DEPENDENCIES += frp sudo
endif

# ==============================================================================
# Shell-access support user (BUG-215)
# ==============================================================================
# Buildroot users-table syntax: username uid group gid password home shell
# groups comment (docs/manual/makeusers-syntax.adoc). Mirrors the Yocto
# recipe's USERADD_PARAM (--system --shell /bin/sh --home-dir /home/support
# --create-home --no-user-group --gid support --groups "" support):
#   - uid/gid -1: Buildroot allocates a stable system-range id.
#   - password '*': login via password is never possible (no valid crypt
#     hash). Unlike Yocto's OpenSSH target, this image's sshd is Dropbear,
#     which does NOT consult the shadow/password field during pubkey auth
#     (verified against dropbear's svr-authpubkey.c) — so there is no
#     equivalent of the no-PAM allowed_user() lockout that forced Yocto's
#     build-time random-secret SHA-512 hash dance (S16-009). A plain locked
#     '*' password is sufficient and simpler here.
#   - groups '-': no secondary groups — not sudo/wheel/root. The only
#     elevation path is the exact curated sudoers.d allowlist below.
ifeq ($(BR2_PACKAGE_OTAPULSE_REMOTE_SSH),y)
define OTAPULSE_USERS
	support -1 support -1 * /home/support /bin/sh - OTA-Pulse gateway support user
endef
endif

# ==============================================================================
# Build Configuration Validation
# ==============================================================================

# Validate required configuration - fail early if not set
define OTAPULSE_VALIDATE_CONFIG
	@echo "Validating OTA-Pulse configuration..."
	$(if $(BR2_INIT_SYSTEMD),,\
		$(error OTA-Pulse requires systemd as the init system. \
			Please set BR2_INIT_SYSTEMD=y in your defconfig. \
			The OTA agent depends on systemd for service ordering, \
			partition setup sequencing, and automatic restart.))
	$(if $(call qstrip,$(BR2_PACKAGE_OTAPULSE_SERVER_URL)),,\
		$(error BR2_PACKAGE_OTAPULSE_SERVER_URL is required but not set. \
			Please configure the OTA server URL in menuconfig.))
	$(if $(call qstrip,$(BR2_PACKAGE_OTAPULSE_VERIFY_KEY)),,\
		$(error BR2_PACKAGE_OTAPULSE_VERIFY_KEY is required but not set. \
			Signature verification is mandatory and cannot be disabled. \
			Please provide the path to your public verification key.))
	@if [ ! -f "$(call qstrip,$(BR2_PACKAGE_OTAPULSE_VERIFY_KEY))" ]; then \
		echo "ERROR: Verification key file not found: $(call qstrip,$(BR2_PACKAGE_OTAPULSE_VERIFY_KEY))"; \
		exit 1; \
	fi
	@echo "Configuration validated successfully."
endef

OTAPULSE_PRE_CONFIGURE_HOOKS += OTAPULSE_VALIDATE_CONFIG

# ==============================================================================
# Go Build Configuration
# ==============================================================================

# LibreSSL compatibility - define missing OpenSSL X509 error constants
OTAPULSE_LIBRESSL_COMPAT = \
	-DX509_V_ERR_DANE_NO_MATCH=65 \
	-DX509_V_ERR_NO_VALID_SCTS=67 \
	-DX509_V_ERR_PATH_LOOP=55 \
	-DX509_V_ERR_PROXY_SUBJECT_NAME_VIOLATION=68 \
	-DX509_V_ERR_SUITE_B_CANNOT_SIGN_P_384_WITH_P_256=59 \
	-DX509_V_ERR_SUITE_B_INVALID_ALGORITHM=57 \
	-DX509_V_ERR_SUITE_B_INVALID_CURVE=58 \
	-DX509_V_ERR_SUITE_B_INVALID_SIGNATURE_ALGORITHM=60 \
	-DX509_V_ERR_SUITE_B_INVALID_VERSION=56 \
	-DX509_V_ERR_SUITE_B_LOS_NOT_ALLOWED=61 \
	-DX509_V_ERR_EE_KEY_TOO_SMALL=66 \
	-DX509_V_ERR_CA_KEY_TOO_SMALL=67 \
	-DX509_V_ERR_CA_MD_TOO_WEAK=68 \
	-DX509_V_ERR_OCSP_VERIFY_NEEDED=73 \
	-DX509_V_ERR_OCSP_VERIFY_FAILED=74 \
	-DX509_V_ERR_OCSP_CERT_UNKNOWN=75

OTAPULSE_GO_ENV = \
	GOOS=linux \
	CGO_ENABLED=1 \
	PKG_CONFIG="$(PKG_CONFIG_HOST_BINARY)" \
	PKG_CONFIG_PATH="$(STAGING_DIR)/usr/lib/pkgconfig:$(STAGING_DIR)/usr/share/pkgconfig" \
	PKG_CONFIG_SYSROOT_DIR="$(STAGING_DIR)" \
	PKG_CONFIG_LIBDIR="$(STAGING_DIR)/usr/lib/pkgconfig" \
	CGO_CFLAGS="-Wno-implicit-fallthrough -Wno-stringop-overflow -Wno-deprecated-declarations $(OTAPULSE_LIBRESSL_COMPAT) $(TARGET_CFLAGS) -I$(STAGING_DIR)/usr/include" \
	CGO_LDFLAGS="$(TARGET_LDFLAGS) -L$(STAGING_DIR)/usr/lib" \
	CC="$(TARGET_CC)" \
	CXX="$(TARGET_CXX)"

# Determine GOARCH from Buildroot architecture
ifeq ($(BR2_aarch64),y)
OTAPULSE_GO_ENV += GOARCH=arm64
else ifeq ($(BR2_arm),y)
OTAPULSE_GO_ENV += GOARCH=arm
ifeq ($(BR2_ARM_CPU_ARMV7A),y)
OTAPULSE_GO_ENV += GOARM=7
else ifeq ($(BR2_ARM_CPU_ARMV6),y)
OTAPULSE_GO_ENV += GOARM=6
else
OTAPULSE_GO_ENV += GOARM=5
endif
else ifeq ($(BR2_i386),y)
OTAPULSE_GO_ENV += GOARCH=386
else ifeq ($(BR2_x86_64),y)
OTAPULSE_GO_ENV += GOARCH=amd64
else ifeq ($(BR2_riscv),y)
OTAPULSE_GO_ENV += GOARCH=riscv64
else ifeq ($(BR2_mips),y)
OTAPULSE_GO_ENV += GOARCH=mips
else ifeq ($(BR2_mipsel),y)
OTAPULSE_GO_ENV += GOARCH=mipsle
endif

OTAPULSE_GO_LDFLAGS = -X $(OTAPULSE_GOMOD)/conf.Version=$(OTAPULSE_VERSION)

# Build tags
OTAPULSE_GO_TAGS =

define OTAPULSE_BUILD_CMDS
	cd $(@D) && \
	$(OTAPULSE_GO_ENV) $(GO_BIN) build \
		-trimpath \
		-ldflags "$(OTAPULSE_GO_LDFLAGS)" \
		-tags "$(OTAPULSE_GO_TAGS)" \
		-o otapulse
endef

# ==============================================================================
# soc-ota-tunneld (Remote SSH tunnel supervisor) — BUG-208
# ==============================================================================
# soc-ota-tunneld lives in the same soc-ota-agent source tree (cmd/soc-ota-
# tunneld) as the main agent, so it's built as an extra step in this same
# package rather than a separate Buildroot package — mirrors why meta-
# otapulse's soc-ota-tunneld Yocto recipe shares soc-ota-agent's EXTERNALSRC
# tree and sets CLEANBROKEN=1 to avoid a 'make clean' from one recipe
# clobbering the other's build output.
#
# CGO_ENABLED=0 override: unlike the main otapulse binary (which needs CGO
# for openssl/lmdb bindings), soc-ota-tunneld is pure-Go and CGO-free — same
# as the Yocto recipe's "export CGO_ENABLED = 0". $(OTAPULSE_GO_ENV) still
# supplies the correct GOARCH/GOARM for this target; the trailing
# CGO_ENABLED=0 overrides its CGO_ENABLED=1.
ifeq ($(BR2_PACKAGE_OTAPULSE_REMOTE_SSH),y)
define OTAPULSE_BUILD_TUNNELD
	cd $(@D) && \
	$(OTAPULSE_GO_ENV) CGO_ENABLED=0 $(GO_BIN) build \
		-trimpath \
		-ldflags "-s -w" \
		-o soc-ota-tunneld \
		./cmd/soc-ota-tunneld
endef
OTAPULSE_POST_BUILD_HOOKS += OTAPULSE_BUILD_TUNNELD
endif

# ==============================================================================
# Installation
# ==============================================================================

define OTAPULSE_INSTALL_TARGET_CMDS
	# Install main binary
	$(INSTALL) -D -m 0755 $(@D)/otapulse $(TARGET_DIR)/usr/bin/otapulse
	ln -sf otapulse $(TARGET_DIR)/usr/bin/mender
	ln -sf otapulse $(TARGET_DIR)/usr/bin/soc-ota-agent

	# Install boot slot switching script (used by the OTA agent during InstallUpdate)
	$(INSTALL) -D -m 0755 $(OTAPULSE_PKGDIR)/switch-boot-slot.sh \
		$(TARGET_DIR)/usr/sbin/switch-boot-slot.sh

	# Create configuration directories
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/etc/otapulse
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/etc/otapulse/scripts
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/etc/otapulse/keys
	$(INSTALL) -d -m 0700 $(TARGET_DIR)/var/lib/otapulse
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/data

	# Install verification key (MANDATORY)
	$(INSTALL) -D -m 0644 $(call qstrip,$(BR2_PACKAGE_OTAPULSE_VERIFY_KEY)) \
		$(TARGET_DIR)/etc/otapulse/keys/artifact-verify-key.pem

	# Install secondary key if configured
	$(if $(call qstrip,$(BR2_PACKAGE_OTAPULSE_VERIFY_KEY_SECONDARY)),\
		$(INSTALL) -D -m 0644 $(call qstrip,$(BR2_PACKAGE_OTAPULSE_VERIFY_KEY_SECONDARY)) \
			$(TARGET_DIR)/etc/otapulse/keys/artifact-verify-key-secondary.pem)

	# Generate configuration file (0600 — may contain a TenantToken/provisioning
	# credential, so it must not be world/group-readable; mirrors the Yocto recipe)
	$(INSTALL) -D -m 0600 /dev/null $(TARGET_DIR)/etc/otapulse/otapulse.conf
	echo '{' > $(TARGET_DIR)/etc/otapulse/otapulse.conf
	echo '  "ServerURL": "$(call qstrip,$(BR2_PACKAGE_OTAPULSE_SERVER_URL))",' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf
	$(if $(call qstrip,$(BR2_PACKAGE_OTAPULSE_TENANT_TOKEN)),\
		echo '  "TenantToken": "$(call qstrip,$(BR2_PACKAGE_OTAPULSE_TENANT_TOKEN))"$(comma)' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf)
	echo '  "UpdatePollIntervalSeconds": $(call qstrip,$(BR2_PACKAGE_OTAPULSE_UPDATE_POLL_INTERVAL)),' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf
	echo '  "InventoryPollIntervalSeconds": $(call qstrip,$(BR2_PACKAGE_OTAPULSE_INVENTORY_POLL_INTERVAL)),' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf
	echo '  "RetryPollIntervalSeconds": $(call qstrip,$(BR2_PACKAGE_OTAPULSE_RETRY_POLL_INTERVAL)),' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf
	echo '  "RootfsPartA": "$(call qstrip,$(BR2_PACKAGE_OTAPULSE_ROOTFS_PART_A))",' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf
	echo '  "RootfsPartB": "$(call qstrip,$(BR2_PACKAGE_OTAPULSE_ROOTFS_PART_B))",' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf
	$(if $(BR2_PACKAGE_OTAPULSE_BOOT_FILE),\
		echo '  "UseFileBasedBootEnv": true$(comma)' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf)
	$(if $(call qstrip,$(BR2_PACKAGE_OTAPULSE_VERIFY_KEY_SECONDARY)),\
		echo '  "ArtifactVerifyKeys": ["/etc/otapulse/keys/artifact-verify-key.pem"$(comma) "/etc/otapulse/keys/artifact-verify-key-secondary.pem"]' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf,\
		echo '  "ArtifactVerifyKeys": ["/etc/otapulse/keys/artifact-verify-key.pem"]' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf)
	echo '}' >> $(TARGET_DIR)/etc/otapulse/otapulse.conf

	# Install device type file (in both locations the agent checks)
	# Format must be "device_type=<value>" (key=value) — the agent's GetManifestData()
	# uses strings.SplitN(line, "=", 2) and requires len==2; a plain value causes
	# "Broken device manifest file" errors.
	echo "device_type=$(call qstrip,$(BR2_PACKAGE_OTAPULSE_DEVICE_TYPE))" > $(TARGET_DIR)/etc/otapulse/device_type
	mkdir -p $(TARGET_DIR)/var/lib/otapulse
	echo "device_type=$(call qstrip,$(BR2_PACKAGE_OTAPULSE_DEVICE_TYPE))" > $(TARGET_DIR)/var/lib/otapulse/device_type

	# Install identity scripts
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/usr/share/otapulse/identity
	$(INSTALL) -m 0755 $(@D)/support/otapulse-device-identity \
		$(TARGET_DIR)/usr/share/otapulse/identity/
	# Provide the canonical name expected by the agent binary
	ln -sf otapulse-device-identity \
		$(TARGET_DIR)/usr/share/otapulse/identity/mender-device-identity

	# Install inventory scripts (if enabled)
	$(if $(BR2_PACKAGE_OTAPULSE_INVENTORY),\
		$(INSTALL) -d -m 0755 $(TARGET_DIR)/usr/share/otapulse/inventory; \
		$(INSTALL) -m 0755 $(@D)/support/otapulse-inventory-bootloader-integration \
			$(TARGET_DIR)/usr/share/otapulse/inventory/; \
		$(INSTALL) -m 0755 $(@D)/support/otapulse-inventory-hostinfo \
			$(TARGET_DIR)/usr/share/otapulse/inventory/; \
		$(INSTALL) -m 0755 $(@D)/support/otapulse-inventory-network \
			$(TARGET_DIR)/usr/share/otapulse/inventory/; \
		$(INSTALL) -m 0755 $(@D)/support/otapulse-inventory-os \
			$(TARGET_DIR)/usr/share/otapulse/inventory/; \
		$(INSTALL) -m 0755 $(@D)/support/otapulse-inventory-provides \
			$(TARGET_DIR)/usr/share/otapulse/inventory/; \
		$(INSTALL) -m 0755 $(@D)/support/otapulse-inventory-rootfs-type \
			$(TARGET_DIR)/usr/share/otapulse/inventory/; \
		$(INSTALL) -m 0755 $(@D)/support/otapulse-inventory-update-modules \
			$(TARGET_DIR)/usr/share/otapulse/inventory/; \
		$(INSTALL) -m 0755 $(@D)/support/otapulse-inventory-geo \
			$(TARGET_DIR)/usr/share/otapulse/inventory/)

	# Install update modules (if enabled)
	$(if $(BR2_PACKAGE_OTAPULSE_UPDATE_MODULES),\
		$(INSTALL) -d -m 0755 $(TARGET_DIR)/usr/share/otapulse/modules/v3; \
		$(INSTALL) -m 0755 $(@D)/support/modules/deb \
			$(TARGET_DIR)/usr/share/otapulse/modules/v3/; \
		$(INSTALL) -m 0755 $(@D)/support/modules/docker \
			$(TARGET_DIR)/usr/share/otapulse/modules/v3/; \
		$(INSTALL) -m 0755 $(@D)/support/modules/directory \
			$(TARGET_DIR)/usr/share/otapulse/modules/v3/; \
		$(INSTALL) -m 0755 $(@D)/support/modules/single-file \
			$(TARGET_DIR)/usr/share/otapulse/modules/v3/; \
		$(INSTALL) -m 0755 $(@D)/support/modules/rpm \
			$(TARGET_DIR)/usr/share/otapulse/modules/v3/; \
		$(INSTALL) -m 0755 $(@D)/support/modules/script \
			$(TARGET_DIR)/usr/share/otapulse/modules/v3/)

	# Install D-Bus configuration (if enabled)
	$(if $(BR2_PACKAGE_OTAPULSE_DBUS),\
		$(INSTALL) -d -m 0755 $(TARGET_DIR)/usr/share/dbus-1/system.d; \
		$(INSTALL) -m 0644 $(@D)/support/dbus/io.otapulse.AuthenticationManager.conf \
			$(TARGET_DIR)/usr/share/dbus-1/system.d/; \
		$(INSTALL) -m 0644 $(@D)/support/dbus/io.otapulse.UpdateManager.conf \
			$(TARGET_DIR)/usr/share/dbus-1/system.d/)
endef

# ==============================================================================
# Example State Scripts Installation
# ==============================================================================

ifeq ($(BR2_PACKAGE_OTAPULSE_STATE_SCRIPTS),y)
define OTAPULSE_INSTALL_STATE_SCRIPTS
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/etc/otapulse/scripts

	# State script version file (REQUIRED by the Mender agent).
	# The agent reads this file and refuses to run scripts if the version
	# is not in its supported list [2 3].  Without the file, the version
	# is treated as 0 and every deployment fails with:
	#   "statescript: The version read from the version file in the
	#    statescript directory does not match the versions supported by
	#    the client (supported: [2 3]; actual: 0)"
	echo "3" > $(TARGET_DIR)/etc/otapulse/scripts/version

	# Download state scripts
	echo '#!/bin/sh' > $(TARGET_DIR)/etc/otapulse/scripts/Download_Enter_00
	echo '# Runs before download starts' >> $(TARGET_DIR)/etc/otapulse/scripts/Download_Enter_00
	echo 'logger -t otapulse "Starting firmware download..."' >> $(TARGET_DIR)/etc/otapulse/scripts/Download_Enter_00
	echo 'exit 0' >> $(TARGET_DIR)/etc/otapulse/scripts/Download_Enter_00
	chmod 0755 $(TARGET_DIR)/etc/otapulse/scripts/Download_Enter_00

	# Install state scripts
	echo '#!/bin/sh' > $(TARGET_DIR)/etc/otapulse/scripts/ArtifactInstall_Enter_00
	echo '# Runs before installation starts' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactInstall_Enter_00
	echo 'logger -t otapulse "Installing firmware update..."' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactInstall_Enter_00
	echo '# Stop services that might interfere with update' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactInstall_Enter_00
	echo '# systemctl stop myapp.service 2>/dev/null || true' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactInstall_Enter_00
	echo 'exit 0' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactInstall_Enter_00
	chmod 0755 $(TARGET_DIR)/etc/otapulse/scripts/ArtifactInstall_Enter_00

	# Reboot state scripts
	echo '#!/bin/sh' > $(TARGET_DIR)/etc/otapulse/scripts/ArtifactReboot_Enter_00
	echo '# Runs before reboot into new firmware' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactReboot_Enter_00
	echo 'logger -t otapulse "Preparing for reboot into new firmware..."' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactReboot_Enter_00
	echo 'sync' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactReboot_Enter_00
	echo 'exit 0' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactReboot_Enter_00
	chmod 0755 $(TARGET_DIR)/etc/otapulse/scripts/ArtifactReboot_Enter_00

	# Commit state scripts
	echo '#!/bin/sh' > $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	echo '# Runs before committing (finalizing) the update' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	echo 'logger -t otapulse "Running health check before commit..."' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	echo '# Add health checks here - return non-zero to trigger rollback' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	echo '# Example: check if critical service is running' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	echo '# if ! systemctl is-active myapp.service >/dev/null 2>&1; then' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	echo '#     logger -t otapulse "Health check failed: myapp not running"' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	echo '#     exit 1' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	echo '# fi' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	echo 'logger -t otapulse "Health check passed, committing update"' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	echo 'exit 0' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00
	chmod 0755 $(TARGET_DIR)/etc/otapulse/scripts/ArtifactCommit_Enter_00

	# Rollback state scripts
	echo '#!/bin/sh' > $(TARGET_DIR)/etc/otapulse/scripts/ArtifactRollback_Enter_00
	echo '# Runs when rolling back to previous firmware' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactRollback_Enter_00
	echo 'logger -t otapulse "WARNING: Rolling back to previous firmware!"' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactRollback_Enter_00
	echo 'exit 0' >> $(TARGET_DIR)/etc/otapulse/scripts/ArtifactRollback_Enter_00
	chmod 0755 $(TARGET_DIR)/etc/otapulse/scripts/ArtifactRollback_Enter_00
endef
OTAPULSE_POST_INSTALL_TARGET_HOOKS += OTAPULSE_INSTALL_STATE_SCRIPTS
endif

# ==============================================================================
# Systemd Service Installation
# ==============================================================================

ifeq ($(BR2_PACKAGE_OTAPULSE_SYSTEMD),y)
define OTAPULSE_INSTALL_SYSTEMD_SERVICE
	$(INSTALL) -D -m 0644 $(@D)/support/otapulse-client.service \
		$(TARGET_DIR)/usr/lib/systemd/system/otapulse.service
	mkdir -p $(TARGET_DIR)/etc/systemd/system/multi-user.target.wants
	ln -sf /usr/lib/systemd/system/otapulse.service \
		$(TARGET_DIR)/etc/systemd/system/multi-user.target.wants/otapulse.service
endef
OTAPULSE_POST_INSTALL_TARGET_HOOKS += OTAPULSE_INSTALL_SYSTEMD_SERVICE
endif

# ==============================================================================
# soc-ota-tunneld Installation (binary + config + systemd unit) — BUG-208
# ==============================================================================

ifeq ($(BR2_PACKAGE_OTAPULSE_REMOTE_SSH),y)
define OTAPULSE_INSTALL_TUNNELD
	# Install the tunnel supervisor binary.
	$(INSTALL) -D -m 0755 $(@D)/soc-ota-tunneld $(TARGET_DIR)/usr/bin/soc-ota-tunneld

	# Install the tunnel config, substituting the frps gateway address/port.
	# Mode 0600 — embeds the frps server address, same rationale as
	# otapulse.conf above.
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/etc/otapulse
	sed -e "s|@SERVER_ADDR@|$(call qstrip,$(BR2_PACKAGE_OTAPULSE_TUNNEL_SERVER_ADDR))|g" \
	    -e "s|@SERVER_PORT@|$(call qstrip,$(BR2_PACKAGE_OTAPULSE_TUNNEL_SERVER_PORT))|g" \
	    $(OTAPULSE_PKGDIR)/tunnel.conf.in > $(TARGET_DIR)/etc/otapulse/tunnel.conf
	chmod 0600 $(TARGET_DIR)/etc/otapulse/tunnel.conf

	# Install and enable the systemd unit (mirrors meta-otapulse's
	# soc-ota-tunneld.service — same ExecStart, same credential gate).
	$(INSTALL) -D -m 0644 $(OTAPULSE_PKGDIR)/soc-ota-tunneld.service \
		$(TARGET_DIR)/usr/lib/systemd/system/soc-ota-tunneld.service
	mkdir -p $(TARGET_DIR)/etc/systemd/system/multi-user.target.wants
	ln -sf /usr/lib/systemd/system/soc-ota-tunneld.service \
		$(TARGET_DIR)/etc/systemd/system/multi-user.target.wants/soc-ota-tunneld.service

	# Seed the update-safe /data/frp directory at image build time so the
	# daemon can write its credential and rendered frpc.toml there on first
	# boot. The actual files are written at runtime; we only seed the dir.
	$(INSTALL) -d -m 0700 $(TARGET_DIR)/data/frp
endef
OTAPULSE_POST_INSTALL_TARGET_HOOKS += OTAPULSE_INSTALL_TUNNELD
endif

# ==============================================================================
# Shell-access layer installation (sudoers allowlist + gateway key) — BUG-215,
# extended with the root/elevated-tier key — BUG-262/BUG-276
# ==============================================================================
# Mirrors meta-otapulse/recipes-core/soc-shell-access/soc-shell-access_1.0.0.bb
# do_install(). The 'support' user itself is created by OTAPULSE_USERS above
# (processed by Buildroot's mkusers at rootfs-finalize time, which also
# chowns /home/support and everything under it to support:support — so no
# explicit chown is needed here for the .ssh files below). Step 4 below adds
# the elevated/break-glass tier's root key, which BUG-215 never covered
# (it was explicitly support-tier-only in scope).
ifeq ($(BR2_PACKAGE_OTAPULSE_REMOTE_SSH),y)
define OTAPULSE_INSTALL_SHELL_ACCESS
	# ------------------------------------------------------------------
	# 1. Sudoers drop-in. Mode 0440, root:root — required by sudo; any
	#    other mode/owner causes sudo to ignore the file entirely.
	# ------------------------------------------------------------------
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/etc/sudoers.d
	$(INSTALL) -m 0440 $(OTAPULSE_PKGDIR)/soc-support.sudoers \
		$(TARGET_DIR)/etc/sudoers.d/10-soc-support

	# Belt-and-suspenders: confirm /etc/sudoers actually pulls in
	# sudoers.d. Upstream sudo's default template ships this, but if a
	# future toolchain/version ever omits it, a silently-ignored
	# allowlist is exactly the BUG-215 failure mode again — so we assert
	# it rather than assume it (same defensive pattern as meta-otapulse's
	# BUG-114 sshd_config Include guard).
	if [ -f $(TARGET_DIR)/etc/sudoers ] && \
	   ! grep -q "^#includedir /etc/sudoers.d\|^@includedir /etc/sudoers.d" \
		$(TARGET_DIR)/etc/sudoers; then \
		echo "@includedir /etc/sudoers.d" >> $(TARGET_DIR)/etc/sudoers; \
	fi

	# ------------------------------------------------------------------
	# 2. Vendor diagnostic scripts directory + the validated log-reader
	#    wrapper (covered by the SOC_DIAG sudoers alias).
	# ------------------------------------------------------------------
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/usr/libexec/soc-diag
	$(INSTALL) -m 0755 $(OTAPULSE_PKGDIR)/soc-readlog \
		$(TARGET_DIR)/usr/libexec/soc-diag/soc-readlog

	# ------------------------------------------------------------------
	# 3. Support user home + gateway authorized key.
	#    Dropbear (this image's sshd — BR2_PACKAGE_DROPBEAR) has no
	#    OpenSSH-style AuthorizedKeysFile global override: it only reads
	#    ~/.ssh/authorized_keys for the login user. So (unlike Yocto's
	#    /etc/ssh/authorized_keys/%u + sshd_config.d drop-in) the gateway
	#    key must go directly into support's own home directory.
	#    0700/0600: authorized_keys must not be group/world-readable.
	# ------------------------------------------------------------------
	$(INSTALL) -d -m 0750 $(TARGET_DIR)/home/support
	$(INSTALL) -d -m 0700 $(TARGET_DIR)/home/support/.ssh
	$(INSTALL) -m 0600 $(OTAPULSE_PKGDIR)/gateway-authorized-key \
		$(TARGET_DIR)/home/support/.ssh/authorized_keys

	# ------------------------------------------------------------------
	# 4. Root (elevated/break-glass tier) authorized key — BUG-262/BUG-276.
	#    The gateway's elevated_ssh_user/breakglass_ssh_user both default
	#    to "root" (socMonitoring/server/gateway/app/config.py), and it
	#    presents the SAME key as 'support' (gateway_ssh_key_path is one
	#    key for all three tiers — only the target username differs). This
	#    was never wired for Buildroot: BUG-215's shell-access layer above
	#    was explicitly support-tier-only in scope, so an elevated-tier
	#    session on a Buildroot board had no credential to authenticate
	#    against, ever (confirmed via BUG-262's investigation — not a
	#    regression, a gap that predates this fix). /root already exists
	#    (Buildroot's target skeleton, root:root, 0700) so no directory
	#    ownership fixup is needed beyond creating .ssh under it.
	#    0700/0600, same rationale as support's: authorized_keys must not
	#    be group/world-readable, doubly so for root's.
	# ------------------------------------------------------------------
	$(INSTALL) -d -m 0700 $(TARGET_DIR)/root/.ssh
	$(INSTALL) -m 0600 $(OTAPULSE_PKGDIR)/gateway-authorized-key \
		$(TARGET_DIR)/root/.ssh/authorized_keys
endef
OTAPULSE_POST_INSTALL_TARGET_HOOKS += OTAPULSE_INSTALL_SHELL_ACCESS
endif

# ==============================================================================
# U-Boot Environment Configuration
# ==============================================================================

ifeq ($(BR2_PACKAGE_OTAPULSE_BOOT_UBOOT_ENV),y)
define OTAPULSE_INSTALL_UBOOT_ENV_CONFIG
	# Create fw_env.config if not exists
	if [ ! -f $(TARGET_DIR)$(call qstrip,$(BR2_PACKAGE_OTAPULSE_UBOOT_ENV_CONFIG)) ]; then \
		$(INSTALL) -D -m 0644 /dev/null \
			$(TARGET_DIR)$(call qstrip,$(BR2_PACKAGE_OTAPULSE_UBOOT_ENV_CONFIG)); \
		echo "# U-Boot environment configuration for OTA-Pulse" > \
			$(TARGET_DIR)$(call qstrip,$(BR2_PACKAGE_OTAPULSE_UBOOT_ENV_CONFIG)); \
		echo "# Format: Device offset size [sector_size] [sector_count]" >> \
			$(TARGET_DIR)$(call qstrip,$(BR2_PACKAGE_OTAPULSE_UBOOT_ENV_CONFIG)); \
		echo "# Adjust according to your platform's U-Boot configuration" >> \
			$(TARGET_DIR)$(call qstrip,$(BR2_PACKAGE_OTAPULSE_UBOOT_ENV_CONFIG)); \
		echo "# Example for eMMC: /dev/mmcblk0 0x400000 0x4000" >> \
			$(TARGET_DIR)$(call qstrip,$(BR2_PACKAGE_OTAPULSE_UBOOT_ENV_CONFIG)); \
	fi
endef
OTAPULSE_POST_INSTALL_TARGET_HOOKS += OTAPULSE_INSTALL_UBOOT_ENV_CONFIG
endif

# ==============================================================================
# Persistent Machine-ID Service
# ==============================================================================

define OTAPULSE_INSTALL_MACHINE_ID
	$(INSTALL) -D -m 0755 $(OTAPULSE_PKGDIR)/otapulse-machine-id.sh \
		$(TARGET_DIR)/usr/sbin/otapulse-machine-id

	$(INSTALL) -D -m 0644 $(OTAPULSE_PKGDIR)/otapulse-machine-id.service \
		$(TARGET_DIR)/usr/lib/systemd/system/otapulse-machine-id.service
	mkdir -p $(TARGET_DIR)/etc/systemd/system/sysinit.target.wants
	ln -sf /usr/lib/systemd/system/otapulse-machine-id.service \
		$(TARGET_DIR)/etc/systemd/system/sysinit.target.wants/otapulse-machine-id.service
endef
OTAPULSE_POST_INSTALL_TARGET_HOOKS += OTAPULSE_INSTALL_MACHINE_ID

# ==============================================================================
# Firstboot Script for Dynamic Partitioning
# ==============================================================================

ifeq ($(BR2_PACKAGE_OTAPULSE_PARTITION_DYNAMIC),y)
define OTAPULSE_INSTALL_FIRSTBOOT
	$(INSTALL) -D -m 0755 $(OTAPULSE_PKGDIR)/otapulse-firstboot.sh \
		$(TARGET_DIR)/usr/sbin/otapulse-firstboot

	# Install systemd service for firstboot
	$(INSTALL) -D -m 0644 $(OTAPULSE_PKGDIR)/otapulse-firstboot.service \
		$(TARGET_DIR)/usr/lib/systemd/system/otapulse-firstboot.service
	mkdir -p $(TARGET_DIR)/etc/systemd/system/multi-user.target.wants
	ln -sf /usr/lib/systemd/system/otapulse-firstboot.service \
		$(TARGET_DIR)/etc/systemd/system/multi-user.target.wants/otapulse-firstboot.service
endef
OTAPULSE_POST_INSTALL_TARGET_HOOKS += OTAPULSE_INSTALL_FIRSTBOOT
endif

$(eval $(generic-package))
