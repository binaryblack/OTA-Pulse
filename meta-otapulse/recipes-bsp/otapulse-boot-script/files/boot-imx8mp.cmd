# OTAPulse A/B Boot Script for i.MX8M Plus
# Reads boot slot preference and boots appropriate rootfs partition
#
# Boot slot file: mender_boot_part on boot partition
# File format: single digit partition number (2=rootfs_a, 3=rootfs_b)
#
# Copyright 2026 OTA-Pulse Project
# SPDX-License-Identifier: MIT

echo "=== OTAPulse Boot Script v1.2 ==="

# Set defaults matching the board config
# mmcdev: 1 = mmcblk1 (eMMC on i.MX8MP FRDM)
# mmcpart: 1 = boot partition
setenv mmcdev 1
setenv mmcpart 1
setenv bootpart 2
setenv console ttymxc1,115200
setenv image Image
setenv fdt_file imx8mp-frdm.dtb
setenv loadaddr 0x40480000
setenv fdt_addr_r 0x43000000
setenv scriptaddr 0x43500000

# Try to read boot slot from mender_boot_part file on boot partition
if load mmc ${mmcdev}:${mmcpart} ${scriptaddr} mender_boot_part; then
    echo "OTAPulse: Found mender_boot_part file"
    # Read the first character (ASCII: '2' = 0x32 = 50, '3' = 0x33 = 51)
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
            echo "OTAPulse: Invalid boot slot value, defaulting to partition 2"
            setenv bootpart 2
        fi
    fi
else
    echo "OTAPulse: No mender_boot_part file, defaulting to rootfs_a (partition 2)"
    setenv bootpart 2
fi

# Build boot arguments
setenv bootargs console=${console} root=/dev/mmcblk${mmcdev}p${bootpart} rootwait rw

echo "OTAPulse: Booting from /dev/mmcblk${mmcdev}p${bootpart}"
echo "OTAPulse: bootargs=${bootargs}"

# Load kernel
echo "OTAPulse: Loading kernel ${image}..."
if load mmc ${mmcdev}:${mmcpart} ${loadaddr} ${image}; then
    echo "OTAPulse: Kernel loaded successfully"
else
    echo "OTAPulse: ERROR - Failed to load kernel!"
    reset
fi

# Load device tree
echo "OTAPulse: Loading device tree ${fdt_file}..."
if load mmc ${mmcdev}:${mmcpart} ${fdt_addr_r} ${fdt_file}; then
    echo "OTAPulse: Device tree loaded successfully"
else
    echo "OTAPulse: ERROR - Failed to load device tree!"
    reset
fi

# Boot the kernel
echo "OTAPulse: Starting kernel..."
booti ${loadaddr} - ${fdt_addr_r}

# If booti fails, we shouldn't reach here
echo "OTAPulse: ERROR - Boot failed!"
reset
