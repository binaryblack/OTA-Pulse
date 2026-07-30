################################################################################
#
# socmond - SoC Monitoring Daemon and CLI Tools
#
################################################################################

SOCMOND_VERSION = 1.0.0
SOCMOND_SITE = $(BR2_EXTERNAL_OTAPULSE_PATH)/../meta-otapulse/recipes-core/socmond/files
SOCMOND_SITE_METHOD = local
SOCMOND_LICENSE = Apache-2.0

SOCMOND_DEPENDENCIES = ca-certificates libcurl jq

# ==============================================================================
# Installation
# ==============================================================================

define SOCMOND_INSTALL_TARGET_CMDS
	# Install daemon and core tools
	$(INSTALL) -D -m 0755 $(@D)/socmond $(TARGET_DIR)/usr/bin/socmond
	$(INSTALL) -D -m 0755 $(@D)/socmond-watchdog $(TARGET_DIR)/usr/bin/socmond-watchdog
	$(INSTALL) -D -m 0755 $(@D)/core-handler.sh $(TARGET_DIR)/usr/bin/socmond-core-handler

	# Install CLI tools
	$(INSTALL) -D -m 0755 $(@D)/soc-ctl $(TARGET_DIR)/usr/bin/soc-ctl
	$(INSTALL) -D -m 0755 $(@D)/soc-deregister $(TARGET_DIR)/usr/bin/soc-deregister
	$(INSTALL) -D -m 0755 $(@D)/show-version $(TARGET_DIR)/usr/bin/show-version

	# Install default configuration. Mode 0600 (not 0644) — the build-time
	# overrides below may bake in a real provisioning token, mirroring the
	# otapulse.mk convention for its own credential-bearing config file.
	$(INSTALL) -D -m 0600 $(@D)/config.json $(TARGET_DIR)/etc/socmond/config.json

	# Optional build-time server/token overrides — needed for devices with
	# an ephemeral/snapshot-mode disk (e.g. QEMU) where a runtime credential
	# push can't survive a reboot; see BR2_PACKAGE_SOCMOND_SERVER_URL /
	# BR2_PACKAGE_SOCMOND_PROVISIONING_TOKEN. Left blank, config.json keeps
	# its shipped defaults and socmond provisions itself at runtime instead.
	$(if $(call qstrip,$(BR2_PACKAGE_SOCMOND_SERVER_URL)),\
		sed -i 's|"base_url": *"[^"]*"|"base_url": "$(call qstrip,$(BR2_PACKAGE_SOCMOND_SERVER_URL))"|' \
			$(TARGET_DIR)/etc/socmond/config.json)
	$(if $(call qstrip,$(BR2_PACKAGE_SOCMOND_PROVISIONING_TOKEN)),\
		sed -i 's|"provisioning_token": *"[^"]*"|"provisioning_token": "$(call qstrip,$(BR2_PACKAGE_SOCMOND_PROVISIONING_TOKEN))"|' \
			$(TARGET_DIR)/etc/socmond/config.json)

	# Install systemd service files
	$(INSTALL) -D -m 0644 $(@D)/socmond.service \
		$(TARGET_DIR)/usr/lib/systemd/system/socmond.service
	$(INSTALL) -D -m 0644 $(@D)/socmond-watchdog.service \
		$(TARGET_DIR)/usr/lib/systemd/system/socmond-watchdog.service
	mkdir -p $(TARGET_DIR)/etc/systemd/system/multi-user.target.wants
	ln -sf /usr/lib/systemd/system/socmond.service \
		$(TARGET_DIR)/etc/systemd/system/multi-user.target.wants/socmond.service
	# NOTE: socmond-watchdog is NOT auto-enabled to prevent reboot loops
	# if socmond fails to start (e.g. missing /bin/bash on busybox systems).
	# Enable manually with: systemctl enable socmond-watchdog

	# Install sysctl configuration for coredump handling
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/etc/sysctl.d
	echo "fs.suid_dumpable=0" > $(TARGET_DIR)/etc/sysctl.d/90-socmond-coredump.conf
	echo "kernel.core_pattern=|/usr/bin/socmond-core-handler %p %u %g %s %t %c %h %e" >> \
		$(TARGET_DIR)/etc/sysctl.d/90-socmond-coredump.conf
	echo "kernel.core_pipe_limit=4" >> $(TARGET_DIR)/etc/sysctl.d/90-socmond-coredump.conf
	chmod 0644 $(TARGET_DIR)/etc/sysctl.d/90-socmond-coredump.conf

	# Install tmpfiles.d for runtime directories
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/usr/lib/tmpfiles.d
	echo "d /run/socmond 0755 root root -" > $(TARGET_DIR)/usr/lib/tmpfiles.d/socmond.conf
	echo "d /var/log/socmond 0755 root root -" >> $(TARGET_DIR)/usr/lib/tmpfiles.d/socmond.conf

	# Create persistent data directories
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/var/lib/socmond
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/var/lib/socmond/ota
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/var/lib/socmond/coredumps
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/var/lib/socmond/metrics
endef

# ==============================================================================
# Dashboard Installation (optional)
# ==============================================================================

ifeq ($(BR2_PACKAGE_SOCMOND_DASHBOARD),y)
define SOCMOND_INSTALL_DASHBOARD
	$(INSTALL) -D -m 0755 $(@D)/soc-dashboard $(TARGET_DIR)/usr/bin/soc-dashboard
	$(INSTALL) -D -m 0644 $(@D)/soc-dashboard.service \
		$(TARGET_DIR)/usr/lib/systemd/system/soc-dashboard.service
endef
SOCMOND_POST_INSTALL_TARGET_HOOKS += SOCMOND_INSTALL_DASHBOARD
endif

$(eval $(generic-package))
