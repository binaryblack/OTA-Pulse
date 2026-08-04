################################################################################
#
# frp
#
################################################################################

# frp ships fully static Go binaries (no host toolchain needed); we fetch the
# pre-built release tarball for the target arch and install only the frpc
# (client) binary. Mirrors meta-otapulse's recipes-connectivity/frp/
# frp_0.69.0.bb on the Yocto side — same version, same per-arch checksums
# (independently re-verified against frp's official release manifest).

FRP_VERSION = 0.69.0
FRP_SITE = https://github.com/fatedier/frp/releases/download/v$(FRP_VERSION)

# frp release tarball naming uses GOARCH-style arch suffixes.
ifeq ($(BR2_aarch64),y)
FRP_ARCH = arm64
else ifeq ($(BR2_arm),y)
FRP_ARCH = arm
else ifeq ($(BR2_x86_64),y)
FRP_ARCH = amd64
else ifeq ($(BR2_i386),y)
FRP_ARCH = 386
endif

FRP_SOURCE = frp_$(FRP_VERSION)_linux_$(FRP_ARCH).tar.gz

FRP_LICENSE = MIT
FRP_LICENSE_FILES = LICENSE

# Pre-built binary — nothing to configure, compile, or stage.
FRP_INSTALL_STAGING = NO

define FRP_INSTALL_TARGET_CMDS
	# Install only frpc (the client). frps is NOT installed — soc-ota-tunneld
	# only needs the client side; frps runs on the OTA-Pulse gateway.
	$(INSTALL) -D -m 0755 $(@D)/frpc $(TARGET_DIR)/usr/bin/frpc
endef

$(eval $(generic-package))
