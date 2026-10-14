# OTA-Pulse bootloader compatibility mode on the NXP i.MX8M Plus FRDM (TODO-157 / S109, TASK-S109-022).
# Parsed only with the NXP board support layer (collection "mender-nxp"). The vendored u-boot-scr recipe only lists the
# Olimex i.MX8MP-EVB and the Voipac i.MX93 as compatible and has no FRDM boot script. The FRDM script is ours:
# files/imx8mp-lpddr4-frdm/boot.cmd (kernel and DTB from the chosen root file system, root=${mender_kernel_root}).
COMPATIBLE_MACHINE = "(olimex-imx8mp-evb|imx93-voipac|imx8mp-lpddr4-frdm)"
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"
