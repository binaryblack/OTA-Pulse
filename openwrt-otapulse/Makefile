#
# OTAPulse — OpenWrt package Makefile
#
# Builds the OTA agent from Go source using OpenWrt's cross-compilation
# infrastructure. Generates configuration from UCI at startup via procd.
#

include $(TOPDIR)/rules.mk

PKG_NAME:=otapulse
PKG_VERSION:=1.0.0
PKG_RELEASE:=1

PKG_SOURCE_PROTO:=git
PKG_SOURCE_URL:=https://github.com/binaryblack/OTA-Pulse.git
PKG_SOURCE_VERSION:=v$(PKG_VERSION)
PKG_MIRROR_HASH:=skip
PKG_SOURCE_SUBDIR:=$(PKG_NAME)-$(PKG_VERSION)

PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)
PKG_BUILD_DEPENDS:=golang/host

GO_PKG:=github.com/binaryblack/OTA-Pulse
GO_PKG_BUILD_PKG:=$(GO_PKG)/soc-ota-agent

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/otapulse
  SECTION:=utils
  CATEGORY:=Utilities
  TITLE:=OTAPulse OTA Update Agent
  URL:=https://github.com/binaryblack/OTA-Pulse
  DEPENDS:=$(GO_ARCH_DEPENDS) +libopenssl +liblmdb +ca-bundle
endef

define Package/otapulse/description
  OTAPulse is a secure Over-The-Air (OTA) update agent for embedded Linux.
  Provides atomic A/B partition updates with automatic rollback, firmware
  signature verification, and fleet management integration.
endef

define Package/otapulse/conffiles
/etc/config/otapulse
endef

# Go build configuration
GO_PKG_LDFLAGS_X:= \
	github.com/binaryblack/OTA-Pulse/conf.Version=$(PKG_VERSION)

CGO_ENABLED:=1

define Build/Compile
	$(call GoPackage/Build/Compile)
endef

define Package/otapulse/install
	# Binary
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/soc-ota-agent $(1)/usr/bin/otapulse
	$(LN) otapulse $(1)/usr/bin/mender

	# Procd init script
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/otapulse.init $(1)/etc/init.d/otapulse

	# UCI config
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/otapulse.config $(1)/etc/config/otapulse

	# UCI defaults (first boot setup)
	$(INSTALL_DIR) $(1)/etc/uci-defaults
	$(INSTALL_BIN) ./files/otapulse.defaults $(1)/etc/uci-defaults/99-otapulse

	# Configuration directory
	$(INSTALL_DIR) $(1)/etc/otapulse

	# Identity scripts
	$(INSTALL_DIR) $(1)/usr/share/otapulse/identity
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/soc-ota-agent/support/otapulse-device-identity \
		$(1)/usr/share/otapulse/identity/

	# Inventory scripts
	$(INSTALL_DIR) $(1)/usr/share/otapulse/inventory
	for script in otapulse-inventory-bootloader-integration \
	              otapulse-inventory-hostinfo \
	              otapulse-inventory-network \
	              otapulse-inventory-os \
	              otapulse-inventory-provides \
	              otapulse-inventory-rootfs-type \
	              otapulse-inventory-update-modules; do \
		$(INSTALL_BIN) $(PKG_BUILD_DIR)/soc-ota-agent/support/$$script \
			$(1)/usr/share/otapulse/inventory/ ; \
	done

	# Update modules
	$(INSTALL_DIR) $(1)/usr/share/otapulse/modules/v3
	for module in deb docker directory single-file rpm script; do \
		$(INSTALL_BIN) $(PKG_BUILD_DIR)/soc-ota-agent/support/modules/$$module \
			$(1)/usr/share/otapulse/modules/v3/ ; \
	done

	# Data directories
	$(INSTALL_DIR) $(1)/data/ota
	$(INSTALL_DIR) $(1)/var/lib/otapulse
endef

$(eval $(call BuildPackage,otapulse))
