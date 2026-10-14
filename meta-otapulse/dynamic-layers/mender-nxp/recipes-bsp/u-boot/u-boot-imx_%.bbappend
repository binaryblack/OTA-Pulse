# OTA-Pulse bootloader compatibility mode on the NXP i.MX8M Plus FRDM (TODO-157 / S109, TASK-S109-022).
# Parsed ONLY when the NXP board support layer (meta-otapulse-bsp/boards/nxp, collection "mender-nxp") is part of the build
# (BBFILES_DYNAMIC in conf/layer.conf), so native builds never see it.
#
# Why this exists: the vendored upstream board support (meta-mender-nxp) integrates exactly two boards, the Olimex
# i.MX8MP-EVB and the Voipac i.MX93 EVK. The FRDM U-Boot (imx8mp_frdm_defconfig, added by meta-imx-frdm) keeps its
# environment on mmc device 1 (the SD slot) at a non-redundant offset, has no boot counter, and does not carry the
# Mender boot code. Its auto-configuration is switched off by the vendored bbappend (MENDER_UBOOT_AUTO_CONFIGURE = "0"),
# so, like Olimex, the board needs a manual integration:
#   0001  (ours, SRC_URI below)  defconfig + board header: environment on the eMMC user area (mmc 2), redundant, boot counter
#   0002  "Integration of Mender boot code into U-Boot": the board support's core recipe already lists a file of that name
#         (generic, written for mainline U-Boot). The NXP U-Boot tree needs the variant the vendored NXP layer ships for
#         Olimex (same file name, different context), and the layer finds it by NAME: adding its directory in front of
#         the search path below makes the core recipe's own SRC_URI entry resolve to it. There is a single, pinned copy
#         of that patch (the vendored layer's) and it is NOT listed a second time (a second entry fails do_patch with
#         "already applied").
FILESEXTRAPATHS:prepend:imx8mp-lpddr4-frdm = "${LAYERDIR_OTAPULSE}/../meta-otapulse-bsp/boards/nxp/recipes-bsp/u-boot/u-boot-imx/olimex-imx8mp-evb:${LAYERDIR_OTAPULSE}/dynamic-layers/mender-nxp/recipes-bsp/u-boot/files/imx8mp-lpddr4-frdm:"

SRC_URI:append:imx8mp-lpddr4-frdm = " file://0001-configs-imx8mp-frdm-otapulse-bootloader-mode.patch"

# same environment size as the Olimex integration (the vendored bbappend sets it for Olimex only)
BOOTENV_SIZE:imx8mp-lpddr4-frdm = "0x4000"
