# OTA-Pulse bootloader compatibility mode on the Raspberry Pi (TODO-157 / S109, TASK-S109-019).
# Parsed ONLY when the Raspberry Pi board support layer (meta-otapulse-bsp/boards/raspberrypi, collection
# "mender-raspberrypi") is part of the build (BBFILES_DYNAMIC in conf/layer.conf), so native builds never see it.
#
# Why this exists: the board support only swaps in its own boot script when a device-side update client is
# installed ("mender-update-install"). OTA-Pulse never installs one: the OTA-Pulse agent is the only client
# (see otapulse-full-bootloader.bbclass, section 3). Without this append the image would boot with the stock
# Raspberry Pi script, which loads the kernel from the FAT partition, never runs the boot-slot setup (no slot
# choice, no panic=, no rollback) and is only caught late by the boot script check of the finished image.
#
# The script itself is the board support's own boot.cmd.in (fdt bootargs -> run <boot-slot setup> -> load the
# kernel from the chosen root file system -> boot -> recover). It is used from the board support layer where it
# is vendored, so there is a single, pinned copy of it.
FILESEXTRAPATHS:prepend := "${LAYERDIR_OTAPULSE}/../meta-otapulse-bsp/boards/raspberrypi/recipes-bsp/rpi-u-boot-scr/files:"
