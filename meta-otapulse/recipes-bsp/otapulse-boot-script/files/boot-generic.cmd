# OTAPulse A/B Boot Script - Generic Template
# Reads boot slot preference and boots appropriate rootfs partition
#
# This is a generic template. For your platform, either:
# 1. Create a machine-specific boot-${MACHINE}.cmd file
# 2. Set the required variables in your U-Boot environment
#
# Required variables (set these for your platform):
#   mmcdev    - MMC device number (0 or 1)
#   mmcpart   - Boot partition number (usually 1)
#   console   - Console device (e.g., ttyS0,115200 or ttymxc1,115200)
#   image     - Kernel image name (Image for arm64, zImage for arm)
#   fdt_file  - Device tree filename
#   loadaddr  - Kernel load address
#   fdt_addr_r - Device tree load address
#
# Copyright 2026 OTA-Pulse Project
# SPDX-License-Identifier: MIT

echo "=== OTAPulse A/B Boot Script ==="

# Default partition (rootfs_a = 2, rootfs_b = 3)
setenv bootpart 2

# Use existing U-Boot variables if set, otherwise use defaults
# These should be set in your U-Boot environment or defconfig
test -n "${mmcdev}" || setenv mmcdev 0
test -n "${mmcpart}" || setenv mmcpart 1
test -n "${console}" || setenv console ttyS0,115200
test -n "${image}" || setenv image Image
test -n "${fdt_file}" || setenv fdt_file ${fdtfile}
test -n "${loadaddr}" || setenv loadaddr ${kernel_addr_r}
test -n "${fdt_addr_r}" || setenv fdt_addr_r ${fdt_addr}
test -n "${scriptaddr}" || setenv scriptaddr 0x43500000

# Try to read boot slot from mender_boot_part file on boot partition
if load mmc ${mmcdev}:${mmcpart} ${scriptaddr} mender_boot_part; then
    echo "OTAPulse: Found mender_boot_part file"
    # Read the first character (ASCII: '2' = 0x32, '3' = 0x33)
    setexpr.b bootpart *${scriptaddr}
    if test ${bootpart} = 50; then
        setenv bootpart 2
        echo "OTAPulse: Boot slot A (rootfs_a, partition 2)"
    elif test ${bootpart} = 51; then
        setenv bootpart 3
        echo "OTAPulse: Boot slot B (rootfs_b, partition 3)"
    fi
    # Handle unexpected values - default to slot A
    if test ${bootpart} != 2; then
        if test ${bootpart} != 3; then
            echo "OTAPulse: Invalid boot slot, defaulting to partition 2"
            setenv bootpart 2
        fi
    fi
else
    echo "OTAPulse: No mender_boot_part file, defaulting to rootfs_a"
    setenv bootpart 2
fi

# Build boot arguments
setenv bootargs console=${console} root=/dev/mmcblk${mmcdev}p${bootpart} rootwait rw

echo "OTAPulse: Booting from /dev/mmcblk${mmcdev}p${bootpart}"

# Load kernel
echo "OTAPulse: Loading kernel..."
if load mmc ${mmcdev}:${mmcpart} ${loadaddr} ${image}; then
    echo "OTAPulse: Kernel loaded"
else
    echo "OTAPulse: ERROR - Failed to load kernel!"
    reset
fi

# Load device tree
echo "OTAPulse: Loading device tree..."
if load mmc ${mmcdev}:${mmcpart} ${fdt_addr_r} ${fdt_file}; then
    echo "OTAPulse: Device tree loaded"
else
    echo "OTAPulse: ERROR - Failed to load device tree!"
    reset
fi

# Boot - use booti for arm64, bootz for arm32
echo "OTAPulse: Starting kernel..."
booti ${loadaddr} - ${fdt_addr_r} || bootz ${loadaddr} - ${fdt_addr_r}

echo "OTAPulse: ERROR - Boot failed!"
reset
