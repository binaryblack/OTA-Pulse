################################################################################
#
# host-mender-artifact - Mender artifact creation tool (host)
#
# Builds the mender-artifact CLI on the host so that board post-image scripts
# can automatically sign and package rootfs images as .mender OTA artifacts.
#
# Requires libssl-dev on the build host:
#   Debian/Ubuntu: sudo apt-get install libssl-dev
#
################################################################################

HOST_MENDER_ARTIFACT_VERSION = 3.11.2
HOST_MENDER_ARTIFACT_SITE = $(call github,mendersoftware,mender-artifact,$(HOST_MENDER_ARTIFACT_VERSION))
HOST_MENDER_ARTIFACT_LICENSE = Apache-2.0
HOST_MENDER_ARTIFACT_LICENSE_FILES = LICENSE

# Build the main binary at the repo root
HOST_MENDER_ARTIFACT_BUILD_TARGETS = .
HOST_MENDER_ARTIFACT_INSTALL_BINS = mender-artifact

# CGO is required for openssl-based signing support.
# The host build uses the system C compiler and system openssl headers.
HOST_MENDER_ARTIFACT_GO_ENV = \
	CGO_ENABLED=1 \
	CGO_CFLAGS="-I/usr/include" \
	CGO_LDFLAGS="-L/usr/lib"

$(eval $(host-golang-package))
