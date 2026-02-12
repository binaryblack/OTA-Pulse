################################################################################
#
# memfault - SoC Monitoring Daemon and CLI Tools
#
################################################################################

MEMFAULT_VERSION = 1.0.0
MEMFAULT_SITE = $(BR2_EXTERNAL_OTAPULSE_PATH)/../meta-otapulse/recipes-core/memfault/files
MEMFAULT_SITE_METHOD = local
MEMFAULT_LICENSE = Apache-2.0

MEMFAULT_DEPENDENCIES = ca-certificates curl jq

# ==============================================================================
# Installation
# ==============================================================================

define MEMFAULT_INSTALL_TARGET_CMDS
	# Install daemon and core tools
	$(INSTALL) -D -m 0755 $(@D)/memfaultd $(TARGET_DIR)/usr/bin/memfaultd
	$(INSTALL) -D -m 0755 $(@D)/memfault-watchdog $(TARGET_DIR)/usr/bin/memfault-watchdog
	$(INSTALL) -D -m 0755 $(@D)/core-handler.sh $(TARGET_DIR)/usr/bin/memfault-core-handler

	# Install CLI tools
	$(INSTALL) -D -m 0755 $(@D)/soc-ctl $(TARGET_DIR)/usr/bin/soc-ctl
	$(INSTALL) -D -m 0755 $(@D)/soc-deregister $(TARGET_DIR)/usr/bin/soc-deregister
	$(INSTALL) -D -m 0755 $(@D)/show-version $(TARGET_DIR)/usr/bin/show-version

	# Install default configuration
	$(INSTALL) -D -m 0644 $(@D)/config.json $(TARGET_DIR)/etc/memfault/config.json

	# Install systemd service files
	$(INSTALL) -D -m 0644 $(@D)/memfaultd.service \
		$(TARGET_DIR)/usr/lib/systemd/system/memfaultd.service
	$(INSTALL) -D -m 0644 $(@D)/memfault-watchdog.service \
		$(TARGET_DIR)/usr/lib/systemd/system/memfault-watchdog.service
	mkdir -p $(TARGET_DIR)/etc/systemd/system/multi-user.target.wants
	ln -sf /usr/lib/systemd/system/memfaultd.service \
		$(TARGET_DIR)/etc/systemd/system/multi-user.target.wants/memfaultd.service
	ln -sf /usr/lib/systemd/system/memfault-watchdog.service \
		$(TARGET_DIR)/etc/systemd/system/multi-user.target.wants/memfault-watchdog.service

	# Install sysctl configuration for coredump handling
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/etc/sysctl.d
	echo "fs.suid_dumpable=0" > $(TARGET_DIR)/etc/sysctl.d/90-memfault-coredump.conf
	echo "kernel.core_pattern=|/usr/bin/memfault-core-handler %p %u %g %s %t %c %h %e" >> \
		$(TARGET_DIR)/etc/sysctl.d/90-memfault-coredump.conf
	echo "kernel.core_pipe_limit=4" >> $(TARGET_DIR)/etc/sysctl.d/90-memfault-coredump.conf
	chmod 0644 $(TARGET_DIR)/etc/sysctl.d/90-memfault-coredump.conf

	# Install tmpfiles.d for runtime directories
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/usr/lib/tmpfiles.d
	echo "d /run/memfault 0755 root root -" > $(TARGET_DIR)/usr/lib/tmpfiles.d/memfault.conf
	echo "d /var/log/memfault 0755 root root -" >> $(TARGET_DIR)/usr/lib/tmpfiles.d/memfault.conf

	# Create persistent data directories
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/var/lib/memfault
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/var/lib/memfault/ota
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/var/lib/memfault/coredumps
	$(INSTALL) -d -m 0755 $(TARGET_DIR)/var/lib/memfault/metrics
endef

# ==============================================================================
# Dashboard Installation (optional)
# ==============================================================================

ifeq ($(BR2_PACKAGE_MEMFAULT_DASHBOARD),y)
define MEMFAULT_INSTALL_DASHBOARD
	$(INSTALL) -D -m 0755 $(@D)/soc-dashboard $(TARGET_DIR)/usr/bin/soc-dashboard
	$(INSTALL) -D -m 0644 $(@D)/soc-dashboard.service \
		$(TARGET_DIR)/usr/lib/systemd/system/soc-dashboard.service
endef
MEMFAULT_POST_INSTALL_TARGET_HOOKS += MEMFAULT_INSTALL_DASHBOARD
endif

$(eval $(generic-package))
