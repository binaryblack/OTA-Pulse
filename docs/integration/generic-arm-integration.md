# OTA-Pulse Integration Guide: Generic ARM Platforms

This guide covers OTA-Pulse integration for ARM platforms not covered by specific guides, including STM32MP, Allwinner, TI AM/Sitara, Xilinx/AMD Zynq, and custom boards.

## Overview

OTA-Pulse is designed to work with any ARM platform that has:
- U-Boot with FAT filesystem support
- GPT partition table support
- At least 8GB storage

The generic approach uses U-Boot environment variables that are already set by your board's defconfig.

## Generic Integration Steps

### Step 1: Add meta-otapulse Layer

```bash
cd <your-yocto-dir>/sources
git clone https://github.com/binaryblack/OTA-Pulse.git ota-pulse

bitbake-layers add-layer ../sources/ota-pulse/meta-otapulse
```

### Step 2: Understand Your Platform

Before configuring, gather this information about your board:

| Information | How to Find | Example |
|-------------|-------------|---------|
| Boot device | `lsblk` or schematic | `/dev/mmcblk0` |
| Console UART | Board docs / schematic | `ttyS0,115200` |
| Kernel load address | U-Boot defconfig | `0x80000000` |
| DTB load address | U-Boot defconfig | `0x82000000` |
| Device tree file | `ls /boot/*.dtb` | `myboard.dtb` |
| Bootloader placement | Vendor docs | Sector 8, 256KB |

### Step 3: Create Platform-Specific WKS File

Create `meta-your-layer/wic/otapulse-dynamic-ab-myplatform.wks`:

```wks
# OTA-Pulse Dynamic A/B WKS - My Platform
#
# Customize the bootloader section for your platform.
# The bootloader placement varies significantly between SoC vendors.

# =============================================================================
# BOOTLOADER SECTION - CUSTOMIZE THIS FOR YOUR PLATFORM
# =============================================================================
#
# Examples:
#
# STM32MP1:
#   part fsbl1 --source rawcopy --sourceparams="file=tf-a-stm32mp157.stm32" \
#       --ondisk mmcblk --no-table --align 17
#   part fsbl2 --source rawcopy --sourceparams="file=tf-a-stm32mp157.stm32" \
#       --ondisk mmcblk --no-table --align 273
#   part fip --source rawcopy --sourceparams="file=fip.bin" \
#       --ondisk mmcblk --no-table --align 529
#
# TI AM335x (BeagleBone):
#   part MLO --source rawcopy --sourceparams="file=MLO" \
#       --ondisk mmcblk --no-table --align 128
#   part u-boot --source rawcopy --sourceparams="file=u-boot.img" \
#       --ondisk mmcblk --no-table --align 768
#
# Allwinner (sunxi):
#   part spl --source rawcopy --sourceparams="file=sunxi-spl.bin" \
#       --ondisk mmcblk --no-table --align 8
#   part u-boot --source rawcopy --sourceparams="file=u-boot.itb" \
#       --ondisk mmcblk --no-table --align 40
#
# Xilinx Zynq:
#   part boot --source rawcopy --sourceparams="file=BOOT.BIN" \
#       --ondisk mmcblk --no-table --align 8
#
# =============================================================================

# TODO: Add your bootloader partition(s) here
# part bootloader --source rawcopy --sourceparams="file=your-bootloader.bin" \
#     --ondisk mmcblk --no-table --align <your-offset>

# Boot partition (FAT32) - kernel, DTB, boot.scr
part /boot --source bootimg-partition --ondisk mmcblk --fstype=vfat \
    --label boot --part-name boot --active --align 8192 --size 64

# Root filesystem A
part / --source rootfs --ondisk mmcblk --fstype=ext4 \
    --label rootfs_a --part-name rootfs_a --align 8192

# GPT partition table
bootloader --ptable gpt

# Note: rootfs_b and data partitions are created on first boot
```

### Step 4: Create Boot Script (or Use Generic)

The generic boot script reads from U-Boot's existing environment variables:

**Option A: Use the generic boot script (recommended start)**

Set in local.conf:
```bitbake
OTAPULSE_BOOT_SCRIPT = "1"
# Uses boot-generic.cmd which reads from U-Boot env vars
```

**Option B: Create custom boot script**

Create `meta-your-layer/recipes-bsp/otapulse-boot-script/files/boot-myboard.cmd`:

```bash
# OTA-Pulse A/B Boot Script - My Board

echo "=== OTAPulse Boot Script ==="

# Board-specific defaults
# Adjust these for your platform
setenv mmcdev 0
setenv mmcpart 1
setenv bootpart 2
setenv console ttyS0,115200
setenv image Image
setenv fdt_file myboard.dtb

# Memory addresses from your U-Boot defconfig
setenv kernel_addr_r 0x80000000
setenv fdt_addr_r 0x82000000
setenv scriptaddr 0x82100000

# OTA-Pulse boot slot selection (don't modify this part)
if load mmc ${mmcdev}:${mmcpart} ${scriptaddr} mender_boot_part; then
    echo "OTAPulse: Found boot slot file"
    setexpr.b bootpart *${scriptaddr}
    if test ${bootpart} = 32; then
        setenv bootpart 2
        echo "OTAPulse: Slot A (partition 2)"
    fi
    if test ${bootpart} = 33; then
        setenv bootpart 3
        echo "OTAPulse: Slot B (partition 3)"
    fi
    if test ${bootpart} != 2; then
        if test ${bootpart} != 3; then
            echo "OTAPulse: Invalid slot, default to 2"
            setenv bootpart 2
        fi
    fi
else
    echo "OTAPulse: No boot slot file, default to slot A"
    setenv bootpart 2
fi

# Build bootargs
setenv bootargs console=${console} root=/dev/mmcblk${mmcdev}p${bootpart} rootwait rw

echo "OTAPulse: Booting /dev/mmcblk${mmcdev}p${bootpart}"

# Load kernel
load mmc ${mmcdev}:${mmcpart} ${kernel_addr_r} ${image}

# Load device tree
load mmc ${mmcdev}:${mmcpart} ${fdt_addr_r} ${fdt_file}

# Boot (use booti for arm64, bootz for arm32)
booti ${kernel_addr_r} - ${fdt_addr_r} || bootz ${kernel_addr_r} - ${fdt_addr_r}

echo "OTAPulse: Boot failed!"
reset
```

### Step 5: Configure local.conf

```bitbake
# =============================================================
# OTA-Pulse Configuration - Generic Platform
# =============================================================

MACHINE = "my-custom-board"

# OTA server
OTA_SERVER_URL = "https://your-ota-server.com"

# Enable boot script
OTAPULSE_BOOT_SCRIPT = "1"

# Your WKS file
WKS_FILE = "otapulse-dynamic-ab-myplatform.wks"

# Dynamic partitioning (recommended)
OTAPULSE_PARTITION_MODE = "dynamic"

# Optional: Override boot script selection
# OTAPULSE_BOOT_CMD = "boot-myboard.cmd"
```

### Step 6: Add Packages to Image

```bitbake
IMAGE_INSTALL:append = " \
    soc-ota-agent \
    otapulse-firstboot \
"
```

### Step 7: Build and Test

```bash
bitbake my-image
# Flash and test boot
```

---

## Platform-Specific Notes

### STM32MP1 (STMicroelectronics)

```
Bootloader Layout:
- Sector 17 (0x4400): FSBL copy 1 (TF-A)
- Sector 273 (0x22200): FSBL copy 2 (TF-A)
- Sector 529 (0x42200): FIP (U-Boot + firmware)

Console: ttySTM0,115200
Kernel addr: 0xc0000000
```

### TI AM335x / AM57xx (BeagleBone, etc.)

```
Bootloader Layout:
- Sector 128 (0x20000): MLO (SPL)
- Sector 768 (0x60000): u-boot.img

Console: ttyS0,115200
Kernel addr: 0x82000000
```

### Allwinner (sunxi - A64, H5, H6, etc.)

```
Bootloader Layout:
- Sector 8 (0x2000): SPL
- Sector 40 (0x8000): U-Boot

Console: ttyS0,115200
Kernel addr: 0x40080000
```

### Xilinx Zynq / ZynqMP

```
Bootloader Layout:
- Sector 0 or separate partition: BOOT.BIN (FSBL + bitstream + U-Boot)

Console: ttyPS0,115200
Kernel addr: 0x00200000 (Zynq) or 0x00080000 (ZynqMP)
```

### QEMU ARM (Testing)

```bitbake
MACHINE = "qemuarm64"
WKS_FILE = "otapulse-dynamic-ab-generic.wks"
# No raw bootloader needed for QEMU
```

---

## Determining Memory Addresses

### Method 1: Check U-Boot defconfig

```bash
grep -E "CONFIG_SYS_LOAD_ADDR|kernel_addr_r|fdt_addr" u-boot/configs/your_defconfig
```

### Method 2: U-Boot printenv

```bash
# In U-Boot console
printenv kernel_addr_r
printenv fdt_addr_r
printenv loadaddr
```

### Method 3: Common Defaults by Architecture

| Architecture | Kernel Load | DTB Load | Script Load |
|--------------|-------------|----------|-------------|
| ARMv7 (32-bit) | 0x80008000 | 0x82000000 | 0x82100000 |
| ARMv8 (64-bit) | 0x80080000 | 0x83000000 | 0x83100000 |

---

## Bootloader Sector Calculation

To convert byte offset to sector (assuming 512-byte sectors):

```
sector = byte_offset / 512
```

Examples:
- 32KB → 32768 / 512 = sector 64
- 8MB → 8388608 / 512 = sector 16384
- 17KB → 17408 / 512 = sector 34 (round up to 64 for alignment)

---

## Troubleshooting Generic Platforms

### Boot Hangs at U-Boot

**Check:** Boot script syntax errors
```bash
# Compile and test locally
mkimage -A arm64 -O linux -T script -d boot.cmd boot.scr
```

### "Bad Linux ARM64 Image magic"

**Cause:** Wrong kernel load address or wrong image type
**Solution:** Verify `kernel_addr_r` matches your U-Boot config and you're using correct Image/zImage

### Partition Setup Fails with "sgdisk: not found"

**Cause:** Missing gptfdisk package
**Solution:**
```bitbake
IMAGE_INSTALL:append = " gptfdisk"
```

### Root Filesystem Read-Only After OTA

**Cause:** Write operation interrupted
**Solution:** OTA agent has retry logic. Check logs:
```bash
journalctl -u soc-ota-agent
```

---

## Testing Without Hardware

Use QEMU for initial testing:

```bash
# Add to local.conf
MACHINE = "qemuarm64"

# Build
bitbake my-image

# Run
runqemu qemuarm64 nographic
```

---

## Contributing Platform Support

If you successfully integrate OTA-Pulse on a new platform:

1. Create a platform-specific integration guide
2. Share your WKS file and boot script
3. Submit a PR to https://github.com/binaryblack/OTA-Pulse

## Support

- GitHub Issues: https://github.com/binaryblack/OTA-Pulse/issues
- Yocto Documentation: https://docs.yoctoproject.org/
