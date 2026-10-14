# OTA-Pulse bootloader compatibility mode on the Raspberry Pi (TODO-157 / S109, TASK-S109-019).
# Parsed ONLY when the Raspberry Pi board support layer is part of the build (see rpi-u-boot-scr.bbappend).
#
# The root device on the firmware command line (cmdline.txt) must be chosen by the bootloader's boot-slot
# setup, not fixed to partition 2: the boot script passes this text on as the kernel command line and the
# boot-slot setup resolves the reference below to /dev/mmcblk0p2 or p3. With a fixed root= an update would
# never take effect and a failed one would never be rolled back. (Same as the board support's own
# cmdline for its update client, which OTA-Pulse never installs.)
CMDLINE_ROOTFS:remove = "root=/dev/mmcblk0p2"
CMDLINE_ROOTFS:append = " root=\${mender_kernel_root} "
