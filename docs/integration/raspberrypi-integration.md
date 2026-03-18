# OTA-Pulse Integration Guide: Raspberry Pi

This guide covers OTA-Pulse integration for Raspberry Pi boards. Note that Raspberry Pi uses a different boot mechanism than standard ARM boards - it uses `config.txt` instead of U-Boot boot scripts.

## Supported Platforms

| Platform | Status |
|----------|--------|
| Raspberry Pi 5 | ⚠️ Supported (needs testing) |
| Raspberry Pi 4 | ⚠️ Supported (needs testing) |
| Raspberry Pi 3 | ⚠️ Supported (needs testing) |
| Raspberry Pi CM4 | ⚠️ Supported (needs testing) |

## Prerequisites

- Yocto Project (Scarthgap or later recommended)
- meta-raspberrypi layer
- At least 8GB storage (16GB+ recommended)

## Understanding Raspberry Pi Boot

Raspberry Pi does **NOT** use U-Boot by default. Instead:

1. GPU loads `bootcode.bin` from SD card
2. GPU reads `config.txt` for configuration
3. GPU loads kernel directly (or U-Boot if configured)

### Two Approaches

| Approach | Pros | Cons |
|----------|------|------|
| **A: Native boot (config.txt)** | Simpler, faster boot | Requires custom boot slot handling |
| **B: U-Boot** | Standard OTA-Pulse boot script | Extra boot stage, slower |

This guide covers both approaches.

---

## Approach A: Native Boot (Recommended)

### How It Works

```
┌──────────────────────────────────────────────────────────┐
│                    config.txt                             │
│  cmdline=cmdline_a.txt  OR  cmdline=cmdline_b.txt        │
└──────────────────────────────────────────────────────────┘
                          │
          ┌───────────────┴───────────────┐
          ▼                               ▼
┌──────────────────────┐      ┌──────────────────────┐
│   cmdline_a.txt      │      │   cmdline_b.txt      │
│ root=/dev/mmcblk0p2  │      │ root=/dev/mmcblk0p3  │
└──────────────────────┘      └──────────────────────┘
```

### Step 1: Create Custom config.txt Handler

Create `meta-your-layer/recipes-bsp/rpi-config/files/otapulse-boot-select.sh`:

```bash
#!/bin/sh
# OTA-Pulse Boot Slot Selector for Raspberry Pi
# Runs before kernel boot to select correct cmdline

BOOT_MOUNT="/boot"
SLOT_FILE="/data/ota/mender_boot_part"
CONFIG_FILE="${BOOT_MOUNT}/config.txt"

# Read current boot slot
if [ -f "$SLOT_FILE" ]; then
    SLOT=$(cat "$SLOT_FILE" | tr -d '[:space:]')
else
    SLOT="2"  # Default to slot A
fi

# Update config.txt to point to correct cmdline
if [ "$SLOT" = "3" ]; then
    # Slot B
    sed -i 's/^cmdline=.*/cmdline=cmdline_b.txt/' "$CONFIG_FILE"
else
    # Slot A
    sed -i 's/^cmdline=.*/cmdline=cmdline_a.txt/' "$CONFIG_FILE"
fi

sync
```

### Step 2: Create WKS File

Create `meta-your-layer/wic/otapulse-dynamic-ab-rpi.wks`:

```wks
# OTA-Pulse Dynamic A/B WKS - Raspberry Pi
# No raw bootloader partition needed - RPi boots from FAT partition

# Boot partition (FAT32) - firmware, kernel, DTBs, config.txt
part /boot --source bootimg-partition --ondisk mmcblk \
    --fstype=vfat --label boot --part-name boot --active --align 4096 --size 100

# Root filesystem A
part / --source rootfs --ondisk mmcblk --fstype=ext4 \
    --label rootfs_a --part-name rootfs_a --align 8192

# GPT partition table
bootloader --ptable gpt
```

### Step 3: Configure cmdline Files

Create two cmdline files on boot partition:

**cmdline_a.txt:**
```
console=serial0,115200 console=tty1 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait
```

**cmdline_b.txt:**
```
console=serial0,115200 console=tty1 root=/dev/mmcblk0p3 rootfstype=ext4 rootwait
```

### Step 4: Modify OTA Agent for RPi

The OTA agent needs to update cmdline selection instead of boot.scr. On direct-boot platforms (where U-Boot env tools `fw_printenv`/`fw_setenv` are absent), `file_bootenv.go` automatically updates `cmdline.txt` on the FAT boot partition so that the VideoCore firmware boots the correct root partition after an OTA update.

### Step 5: Configure local.conf

```bitbake
# =============================================================
# OTA-Pulse Configuration for Raspberry Pi
# =============================================================

MACHINE = "raspberrypi4-64"

# OTA server URL
OTA_SERVER_URL = "https://your-ota-server.com"

# Use dynamic partitioning
OTAPULSE_PARTITION_MODE = "dynamic"

# Raspberry Pi WKS file
WKS_FILE = "otapulse-dynamic-ab-rpi.wks"

# Raspberry Pi doesn't use boot.scr by default
OTAPULSE_BOOT_SCRIPT = "0"

# Enable U-Boot if you prefer Approach B
# RPI_USE_U_BOOT = "1"
```

---

## Approach B: U-Boot Boot

If you prefer standard U-Boot boot scripts:

### Step 1: Enable U-Boot

```bitbake
# local.conf
RPI_USE_U_BOOT = "1"
OTAPULSE_BOOT_SCRIPT = "1"
```

### Step 2: Create RPi Boot Script

Create `meta-your-layer/recipes-bsp/otapulse-boot-script/files/boot-raspberrypi4-64.cmd`:

```bash
# OTA-Pulse A/B Boot Script for Raspberry Pi (U-Boot)

echo "=== OTAPulse Boot Script (Raspberry Pi) ==="

setenv mmcdev 0
setenv mmcpart 1
setenv bootpart 2
setenv console ttyS0,115200

# Raspberry Pi uses different kernel names
# RPi 4/5 64-bit: Image
# RPi 3 64-bit: Image
# RPi 32-bit: zImage
setenv image Image
setenv fdt_file bcm2711-rpi-4-b.dtb

# RPi memory addresses (from U-Boot config)
setenv kernel_addr_r 0x00080000
setenv fdt_addr_r 0x02600000
setenv scriptaddr 0x02400000

# Read boot slot
if load mmc ${mmcdev}:${mmcpart} ${scriptaddr} mender_boot_part; then
    echo "OTAPulse: Found mender_boot_part"
    setexpr.b bootpart *${scriptaddr}
    if test ${bootpart} = 32; then
        setenv bootpart 2
        echo "OTAPulse: Slot A"
    fi
    if test ${bootpart} = 33; then
        setenv bootpart 3
        echo "OTAPulse: Slot B"
    fi
else
    echo "OTAPulse: No boot slot file, using slot A"
    setenv bootpart 2
fi

setenv bootargs console=${console} root=/dev/mmcblk${mmcdev}p${bootpart} rootwait rw

echo "OTAPulse: Loading kernel..."
load mmc ${mmcdev}:${mmcpart} ${kernel_addr_r} ${image}

echo "OTAPulse: Loading DTB..."
load mmc ${mmcdev}:${mmcpart} ${fdt_addr_r} ${fdt_file}

echo "OTAPulse: Booting..."
booti ${kernel_addr_r} - ${fdt_addr_r}
```

---

## Partition Layout

```
┌─────────────┬─────────────────────────────────────────────────────────┐
│ Partition   │ Content                                                  │
├─────────────┼─────────────────────────────────────────────────────────┤
│ Partition 1 │ /boot (FAT32, 100MB)                                    │
│             │ - bootcode.bin, start4.elf (GPU firmware)               │
│             │ - config.txt, cmdline.txt                               │
│             │ - kernel (Image or zImage)                              │
│             │ - Device tree files (*.dtb)                             │
│             │ - boot.scr (if using U-Boot)                            │
├─────────────┼─────────────────────────────────────────────────────────┤
│ Partition 2 │ rootfs_a (ext4) - Primary root filesystem               │
├─────────────┼─────────────────────────────────────────────────────────┤
│ Partition 3 │ rootfs_b (ext4) - Secondary (created on first boot)     │
├─────────────┼─────────────────────────────────────────────────────────┤
│ Partition 4 │ data (ext4) - Persistent data (created on first boot)   │
└─────────────┴─────────────────────────────────────────────────────────┘
```

## Device Configuration by Model

| Model | MACHINE | DTB File | Console |
|-------|---------|----------|---------|
| RPi 5 | raspberrypi5 | bcm2712-rpi-5-b.dtb | ttyAMA0 |
| RPi 4 | raspberrypi4-64 | bcm2711-rpi-4-b.dtb | ttyS0 |
| RPi 4 CM | raspberrypi-cm4 | bcm2711-rpi-cm4.dtb | ttyS0 |
| RPi 3 | raspberrypi3-64 | bcm2710-rpi-3-b.dtb | ttyS0 |

## Troubleshooting

### Rainbow Screen / No Boot

**Cause:** GPU can't find firmware files

**Solution:** Verify boot partition has:
```
bootcode.bin
start4.elf (RPi 4/5)
fixup4.dat
config.txt
```

### Kernel Panic: No root filesystem

**Cause:** Wrong root= in cmdline

**Solution:** Check cmdline.txt and verify partition numbers:
```bash
# On RPi
cat /proc/cmdline
lsblk
```

### Serial Console Not Working

**Cause:** Wrong console device

**Solution:** RPi 4 uses `ttyS0` (mini UART) or `ttyAMA0` (PL011); RPi 5 uses `ttyAMA0` by default. In config.txt:
```
enable_uart=1
dtoverlay=disable-bt  # Use ttyAMA0 for serial
```

### First Boot Partition Setup Slow

**Cause:** SD card write speed

**Solution:** Normal - RPi SD card speeds vary. Use high-quality A2 card.

## Compute Module 4 (CM4) Notes

CM4 with eMMC requires special handling:

1. **Flash bootloader to eMMC first** using rpiboot
2. **Device mapping changes:**
   - eMMC: `/dev/mmcblk0`
   - SD card (if present): `/dev/mmcblk1`

```bitbake
# For CM4 with eMMC
MACHINE = "raspberrypi-cm4"
```

## Support

- GitHub Issues: https://github.com/binaryblack/OTA-Pulse/issues
- Raspberry Pi Forums: https://forums.raspberrypi.com/
- meta-raspberrypi: https://github.com/agherzan/meta-raspberrypi
