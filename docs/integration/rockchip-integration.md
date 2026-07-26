# OTA-Pulse Integration Guide: Rockchip Platforms

This guide covers OTA-Pulse integration for Rockchip processors including RK3588, RK3568, RK3566, and RK3399 families.

## Supported Platforms

| Platform | Example Boards | Status |
|----------|---------------|--------|
| RK3588S | Radxa CM5 | ✅ Tested — see [Radxa CM5](#radxa-cm5-rk3588s-slot-switching) below |
| RK3588/RK3588S | Rock 5B, Orange Pi 5 | ⚠️ Supported (needs testing) |
| RK3568 | Rock 3A, Odroid M1 | ⚠️ Supported (needs testing) |
| RK3566 | Quartz64, Pine64 | ⚠️ Supported (needs testing) |
| RK3399 | Rock Pi 4, NanoPC-T4 | ⚠️ Supported (needs testing) |

## Prerequisites

- Yocto Project (Scarthgap or later recommended)
- Rockchip BSP (meta-rockchip)
- U-Boot with FAT filesystem support
- At least 8GB storage (16GB+ recommended for A/B updates)

## Quick Start

### Step 1: Add meta-otapulse Layer

```bash
cd <your-yocto-dir>/sources
git clone https://github.com/binaryblack/OTA-Pulse.git ota-pulse

# Add to bblayers.conf
bitbake-layers add-layer ../sources/ota-pulse/meta-otapulse
```

### Step 2: Create Rockchip WKS File

Create `meta-your-layer/wic/otapulse-dynamic-ab-rockchip.wks`:

```wks
# OTA-Pulse Dynamic A/B WKS - Rockchip
# Bootloader placement for Rockchip SoCs
#
# Sector layout:
#   64 sectors (32KB)    - idbloader.img (SPL + TPL)
#   16384 sectors (8MB)  - u-boot.itb (U-Boot proper + ATF)

# Rockchip bootloader (idbloader at sector 64 = 32KB)
part idbloader --source rawcopy --sourceparams="file=idbloader.img" \
    --ondisk mmcblk --no-table --align 32

# U-Boot at sector 16384 = 8MB (after idbloader)
part u-boot --source rawcopy --sourceparams="file=u-boot.itb" \
    --ondisk mmcblk --no-table --align 8192

# Boot partition (FAT32) - kernel, DTB, boot.scr
part /boot --source bootimg-partition --ondisk mmcblk --fstype=vfat \
    --label boot --part-name boot --active --align 16384 --size 64

# Root filesystem A
part / --source rootfs --ondisk mmcblk --fstype=ext4 \
    --label rootfs_a --part-name rootfs_a --align 8192

# GPT partition table
bootloader --ptable gpt
```

### Step 3: Create Rockchip Boot Script

Create `meta-your-layer/recipes-bsp/otapulse-boot-script/files/boot-rockchip.cmd`:

```bash
# OTA-Pulse A/B Boot Script for Rockchip
# Tested on: RK3588, RK3568, RK3566

echo "=== OTAPulse Boot Script (Rockchip) ==="

# Rockchip defaults - adjust for your board
# Most Rockchip boards use mmcblk0 for eMMC, mmcblk1 for SD
setenv mmcdev 0
setenv mmcpart 1
setenv bootpart 2

# Console varies by board:
# RK3588: ttyS2,1500000
# RK3568/RK3566: ttyS2,1500000
# RK3399: ttyS2,1500000
setenv console ttyS2,1500000

setenv image Image
# Set your device tree file
setenv fdt_file rockchip/rk3588-rock-5b.dtb

# Rockchip standard memory addresses
setenv kernel_addr_r 0x00280000
setenv fdt_addr_r 0x0a100000
setenv scriptaddr 0x00500000
setenv ramdisk_addr_r 0x0a200000

# Try to read boot slot from mender_boot_part file
if load mmc ${mmcdev}:${mmcpart} ${scriptaddr} mender_boot_part; then
    echo "OTAPulse: Found mender_boot_part file"
    setexpr.b bootpart *${scriptaddr}
    if test ${bootpart} = 32; then
        setenv bootpart 2
        echo "OTAPulse: Boot slot A (partition 2)"
    fi
    if test ${bootpart} = 33; then
        setenv bootpart 3
        echo "OTAPulse: Boot slot B (partition 3)"
    fi
    if test ${bootpart} != 2; then
        if test ${bootpart} != 3; then
            echo "OTAPulse: Invalid slot, defaulting to 2"
            setenv bootpart 2
        fi
    fi
else
    echo "OTAPulse: No mender_boot_part, defaulting to slot A"
    setenv bootpart 2
fi

# Build boot arguments
setenv bootargs console=${console} root=/dev/mmcblk${mmcdev}p${bootpart} rootwait rw

echo "OTAPulse: Booting from /dev/mmcblk${mmcdev}p${bootpart}"
echo "OTAPulse: bootargs=${bootargs}"

# Load kernel
echo "OTAPulse: Loading kernel..."
if load mmc ${mmcdev}:${mmcpart} ${kernel_addr_r} ${image}; then
    echo "OTAPulse: Kernel loaded"
else
    echo "OTAPulse: ERROR - Kernel load failed!"
    reset
fi

# Load device tree
echo "OTAPulse: Loading device tree..."
if load mmc ${mmcdev}:${mmcpart} ${fdt_addr_r} ${fdt_file}; then
    echo "OTAPulse: Device tree loaded"
else
    echo "OTAPulse: ERROR - DTB load failed!"
    reset
fi

# Boot
echo "OTAPulse: Starting kernel..."
booti ${kernel_addr_r} - ${fdt_addr_r}

echo "OTAPulse: ERROR - Boot failed!"
reset
```

### Step 4: Create bbappend for Boot Script

Create `meta-your-layer/recipes-bsp/otapulse-boot-script/otapulse-boot-script_%.bbappend`:

```bitbake
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

SRC_URI += "file://boot-rockchip.cmd"

# The boot script is selected via OTAPULSE_BOOT_CMD in local.conf
```

### Step 5: Configure local.conf

```bitbake
# =============================================================
# OTA-Pulse Configuration for Rockchip
# =============================================================

# Your OTA server URL
OTA_SERVER_URL = "https://your-ota-server.com"
OTAPULSE_PROVISIONING_TOKEN = "your-provisioning-token"

# Enable A/B boot script
OTAPULSE_BOOT_SCRIPT = "1"

# Use dynamic partitioning
OTAPULSE_PARTITION_MODE = "dynamic"

# Rockchip WKS file
WKS_FILE = "otapulse-dynamic-ab-rockchip.wks"

# Boot script selection (set to your custom script name)
OTAPULSE_BOOT_CMD = "boot-rockchip.cmd"
```

### Step 6: Add Packages to Image

```bitbake
IMAGE_INSTALL:append = " \
    soc-ota-agent \
    otapulse-firstboot \
    otapulse-rockchip-bootenv \
"
```

### Step 7: Build and Flash

```bash
bitbake my-image

# Flash to SD card
zstd -d -c tmp/deploy/images/<MACHINE>/my-image-<MACHINE>.wic.zst | \
    sudo dd of=/dev/sdX bs=4M status=progress && sync
```

## Rockchip Platform Details

### Bootloader Layout

Rockchip uses a two-stage bootloader:

```
┌─────────────┬─────────────────────────────────────────────────────────┐
│ Offset      │ Content                                                  │
├─────────────┼─────────────────────────────────────────────────────────┤
│ Sector 64   │ idbloader.img (TPL + SPL, ~256KB)                       │
│ (32KB)      │ - Initializes DRAM                                      │
│             │ - Loads U-Boot                                          │
├─────────────┼─────────────────────────────────────────────────────────┤
│ Sector 16384│ u-boot.itb (~1-4MB)                                     │
│ (8MB)       │ - U-Boot proper                                         │
│             │ - ARM Trusted Firmware (ATF)                            │
│             │ - OP-TEE (optional)                                     │
├─────────────┼─────────────────────────────────────────────────────────┤
│ Partition 1 │ /boot (FAT32, 64MB) - kernel, DTB, boot.scr             │
│ Partition 2 │ rootfs_a (ext4) - Primary root filesystem               │
│ Partition 3 │ rootfs_b (ext4) - Secondary root (created on first boot)│
│ Partition 4 │ data (ext4) - Persistent data (created on first boot)   │
└─────────────┴─────────────────────────────────────────────────────────┘
```

### Storage Device Mapping

| Board | eMMC | SD Card | NVMe |
|-------|------|---------|------|
| Rock 5B | `/dev/mmcblk0` | `/dev/mmcblk1` | `/dev/nvme0n1` |
| Orange Pi 5 | `/dev/mmcblk0` | `/dev/mmcblk1` | - |
| Rock 3A | `/dev/mmcblk0` | `/dev/mmcblk1` | - |

### Console Configuration

| SoC | Default UART | Baud Rate |
|-----|--------------|-----------|
| RK3588 | ttyS2 | 1500000 |
| RK3568 | ttyS2 | 1500000 |
| RK3566 | ttyS2 | 1500000 |
| RK3399 | ttyS2 | 1500000 |

### Memory Addresses

Standard Rockchip memory layout:

| Variable | Address | Description |
|----------|---------|-------------|
| kernel_addr_r | 0x00280000 | Kernel load address |
| fdt_addr_r | 0x0a100000 | Device tree address |
| scriptaddr | 0x00500000 | Boot script address |
| ramdisk_addr_r | 0x0a200000 | Initramfs address |

## Board-Specific Configuration

### Rock 5B (RK3588)

```bitbake
MACHINE = "rock-5b"
# Console and FDT are set in the boot script (boot-rockchip.cmd)
# Edit setenv fdt_file and setenv console in your boot script for your board
```

### Orange Pi 5 (RK3588S)

```bitbake
MACHINE = "orangepi-5"
# Edit boot-rockchip.cmd: setenv fdt_file rockchip/rk3588s-orangepi-5.dtb
```

### Rock 3A (RK3568)

```bitbake
MACHINE = "rock-3a"
# Edit boot-rockchip.cmd: setenv fdt_file rockchip/rk3568-rock-3a.dtb
```

## Radxa CM5 (RK3588S) Slot Switching

The Radxa CM5 is the most heavily field-tested Rockchip target and uses a different boot/slot-switch mechanism than the generic `boot-rockchip.cmd` template above:

- **Boot method**: RK vendor U-Boot in **bootstd** mode — `CONFIG_BOOTCOMMAND` must be `bootflow scan`, not `run distro_bootcmd`. The board does not use the `mender_boot_part`-file U-Boot script shown earlier in this guide; slot selection is via **systemd-boot EFI ABA**.
- **Real device tree required**: the generic `rk3588s-radxa-cm5.dtb` (EVB variant) hangs on peripheral probe. You must inject the board's actual `rk3588s-radxa-cm5-io.dts` via a `KERNEL_DEVICETREE:forcevariable` bbappend — see `sources/meta-custom/recipes-bsp/otapulse-boot-script/files/README-rockchip-rk3588.md` in the parent Yocto project for the exact mechanism.
- **Partition resolution**: like i.MX (see [imx-integration.md](imx-integration.md#partition-layout)), rootfs A/B slots are resolved by **GPT partlabel**, not a static `mmcblk0pN` mapping — fixed in commit `877a401`.
- **Slot switching**: implemented in `switch-boot-slot.sh` (shipped in three places: `meta-otapulse/recipes-core/otapulse-firstboot/files/`, `meta-otapulse/recipes-bsp/u-boot-env-config/files/`, and the buildroot equivalent). Recent hardening on this path, all specific to Radxa CM5 field issues:
  - **BUG-155**: primary-NIC MAC address churned across reboots (identity instability) — fixed by persisting the MAC.
  - **BUG-157**: `switch-boot-slot.sh` was receiving synthetic partition paths instead of the real resolved paths — fixed to pass real partition paths through; a follow-up closed remaining false-success paths where the script could report success without actually switching.
  - **BUG-158**: added a systemd-boot EFI ABA method to `switch-boot-slot.sh` as the primary mechanism for this board (rather than relying solely on the U-Boot env write path).
- **Console/hush caveats**: RK vendor U-Boot's hush shell silently no-ops on comments inside a *sourced* `boot.scr` (i.e. `#` comments after the script is already loaded are not always safe — inline them before sourcing instead), and `setexpr.b` is unsupported on this U-Boot build (used by the generic Rockchip boot script above; avoid it here). Use Ctrl+C during autoboot to break into the prompt for debugging.

## Troubleshooting

### Boot Hangs After "Starting kernel"

**Cause:** Wrong console or baud rate

**Solution:** Verify console in boot script matches your serial connection:
```bash
# In U-Boot, check what kernel expects
printenv console
```

### idbloader Not Found

**Cause:** Bootloader images not built or wrong names

**Solution:** Verify bootloader images exist:
```bash
ls tmp/deploy/images/<MACHINE>/idbloader.img
ls tmp/deploy/images/<MACHINE>/u-boot.itb
```

### Partition Table Corruption

**Cause:** idbloader/u-boot overlap with GPT

**Solution:** Verify WKS file has correct offsets. Boot partition should start after U-Boot (typically sector 32768 or 16MB).

### SD Card Boot Priority

**Cause:** Rockchip tries SD before eMMC

**Solution:** This is expected behavior. Remove SD card to boot from eMMC, or configure U-Boot boot order.

## Advanced: NVMe Boot (RK3588)

For boards with NVMe support (Rock 5B, Orange Pi 5 Plus):

```wks
# Boot from NVMe
part /boot --source bootimg-partition --ondisk nvme0n1 --fstype=vfat \
    --label boot --part-name boot --active --align 8192 --size 64
part / --source rootfs --ondisk nvme0n1 --fstype=ext4 \
    --label rootfs_a --part-name rootfs_a --align 8192
```

Note: Rockchip cannot boot directly from NVMe. You need:
1. SPI flash with idbloader + U-Boot, or
2. SD/eMMC with bootloader that chainloads from NVMe

## Support

- GitHub Issues: https://github.com/binaryblack/OTA-Pulse/issues
- Rockchip Wiki: https://opensource.rock-chips.com/
