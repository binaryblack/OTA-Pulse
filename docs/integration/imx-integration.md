# OTA-Pulse Integration Guide: NXP i.MX Platforms

This guide covers OTA-Pulse integration for NXP i.MX processors including i.MX6, i.MX7, and i.MX8 families.

## Supported Platforms

| Platform | Tested Boards | Status |
|----------|--------------|--------|
| i.MX8M Plus | FRDM, EVK | ✅ Fully Tested |
| i.MX8M Mini | EVK | ✅ Supported |
| i.MX8M Nano | EVK | ✅ Supported |
| i.MX6 | Various | ⚠️ Should work (untested) |

## Prerequisites

- Yocto Project (Scarthgap or later recommended)
- NXP BSP (meta-imx or meta-freescale)
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

### Step 2: Configure local.conf

Add to your `conf/local.conf`:

```bitbake
# =============================================================
# OTA-Pulse Configuration for i.MX
# =============================================================

# Your OTA server URL
OTA_SERVER_URL = "https://your-ota-server.com"
OTAPULSE_PROVISIONING_TOKEN = "your-provisioning-token"

# Enable A/B boot script (required for partition switching)
OTAPULSE_BOOT_SCRIPT = "1"

# Use dynamic partitioning (recommended - creates A/B on first boot)
OTAPULSE_PARTITION_MODE = "dynamic"

# i.MX specific WKS file
WKS_FILE = "otapulse-dynamic-ab-imx.wks"
```

### Step 3: Add Packages to Image

In your image recipe (e.g., `my-image.bb`):

```bitbake
# Core OTA packages
IMAGE_INSTALL:append = " \
    soc-ota-agent \
    otapulse-firstboot \
"

# Optional: Monitoring and diagnostics
IMAGE_INSTALL:append = " \
    memfaultd \
"
```

### Step 4: Build and Flash

```bash
bitbake my-image

# Flash to SD card
zstd -d -c tmp/deploy/images/<MACHINE>/my-image-<MACHINE>.wic.zst | \
    sudo dd of=/dev/sdX bs=4M status=progress && sync
```

## i.MX Platform Details

### Storage Device Mapping

The eMMC/SD `mmcblk*` index is **board-specific and not a reliable constant** — e.g. an i.MX8MP FRDM board's soldered eMMC enumerates as `/dev/mmcblk2` (not `/dev/mmcblk1`), depending on how many other MMC controllers probe first. Don't hardcode a device index; verify per-board with `lsblk` before writing your boot script or `local.conf`.

| Board Type | Example eMMC Device | Example SD Card Device |
|------------|----------------------|--------------------------|
| i.MX8MP FRDM | `/dev/mmcblk2` | `/dev/mmcblk0` or `/dev/mmcblk1` |
| i.MX8MP EVK | `/dev/mmcblk2` | `/dev/mmcblk1` |
| i.MX8MM EVK | `/dev/mmcblk2` | `/dev/mmcblk1` |

The boot script auto-detects based on which device contains the boot partition.

### Partition Layout

```
┌─────────────┬─────────────────────────────────────────────────────────┐
│ Offset      │ Content                                                  │
├─────────────┼─────────────────────────────────────────────────────────┤
│ 0 - 32KB    │ MBR / GPT header                                        │
│ 32KB - 4MB  │ imx-boot (U-Boot + ATF + DDR firmware)                  │
│ Partition 1 │ /boot (FAT32, 64MB) - kernel, DTB, boot.scr             │
│ Partition 2 │ rootfs_a (ext4) - Primary root filesystem               │
│ Partition 3 │ rootfs_b (ext4) - Secondary root (created on first boot)│
│ Partition 4 │ data (ext4) - Persistent data (created on first boot)   │
└─────────────┴─────────────────────────────────────────────────────────┘
```

Partition numbers 2/3/4 above are the **default/dynamic-mode layout**, not a guarantee. The agent resolves rootfs_a/rootfs_b primarily by **GPT partlabel** (`/dev/disk/by-partlabel/rootfs_a`, `/dev/disk/by-partlabel/rootfs_b` — see `installer/partitions.go`), falling back to the static partition numbers above only if partlabels aren't present. Don't assume a fixed `mmcblk0p3`/`mmcblk0p4` mapping in custom tooling.

### Boot Script Operation

The i.MX boot script (`boot-imx8mp.cmd`) performs:

1. Reads `mender_boot_part` file from boot partition
2. Sets root partition based on file content (2=slot A, 3=slot B)
3. Loads kernel and device tree
4. Boots with correct root= parameter

Note: the boot script itself still selects the slot number from `mender_boot_part`; it's userspace (the agent and `switch-boot-slot.sh`) that resolves which partition number a given slot maps to via GPT partlabel, so the correct partition is written even if the board's static numbering differs from the defaults above.

### Console Configuration

Default console for i.MX8:
- `ttymxc1,115200` (UART2 on most boards)

To override, create a custom boot script via `OTAPULSE_BOOT_CMD` with your console setting.

## First Boot Process

On first boot, `otapulse-partition-setup.service` will:

1. Detect the storage device
2. Create `rootfs_b` partition (same size as rootfs_a)
3. Create `data` partition (remaining space, minimum 1GB)
4. Format partitions with ext4
5. Set GPT partition labels

This takes approximately 30-60 seconds on first boot.

## Verifying Installation

After boot, verify OTA-Pulse is running:

```bash
# Check partition layout
lsblk -o NAME,SIZE,LABEL,PARTLABEL,FSTYPE

# Check OTA agent status
systemctl status soc-ota-agent

# Configure API key
soc-ctl config apikey <your-api-key>
soc-ctl restart

# Check device registration
soc-ctl status
```

## Performing an OTA Update

```bash
# On your build machine, create update artifact
mender-artifact write rootfs-image \
    -f my-image-<MACHINE>.ext4 \
    -n "v1.2.0" \
    -t "<MACHINE>" \
    -o my-update-v1.2.0.mender

# Upload to OTA server and deploy
# Device will automatically download, install, and reboot
```

## Troubleshooting

### Boot Stuck at "Loading kernel"

**Cause:** Wrong memory addresses in boot script

**Solution:** Verify your board's U-Boot defconfig has correct addresses:
```
CONFIG_SYS_LOAD_ADDR=0x40480000
CONFIG_SYS_TEXT_BASE=0x40200000
```

### Partition Setup Fails

**Cause:** Not enough free space

**Solution:** Ensure SD card is at least 8GB. Check with:
```bash
sgdisk -p /dev/mmcblk1
```

### OTA Agent Won't Start

**Cause:** Missing configuration

**Solution:**
```bash
# Check if config exists
ls -la /etc/otapulse/

# Create config
soc-ctl config apikey <key>
soc-ctl config server <url>
```

### Wrong Root Partition After Update

**Cause:** Boot slot file not synced

**Solution:**
```bash
# Check current boot slot
cat /data/ota/mender_boot_part
cat /boot/mender_boot_part

# They should match. If not:
cp /data/ota/mender_boot_part /boot/
sync
```

## Advanced Configuration

### Custom Boot Script

To use a custom boot script for your board:

```bitbake
# In local.conf
OTAPULSE_BOOT_CMD = "boot-my-custom-board.cmd"
```

Then create `meta-your-layer/recipes-bsp/otapulse-boot-script/files/boot-my-custom-board.cmd`.

### Static Partition Mode

For production with pre-allocated partitions:

```bitbake
OTAPULSE_PARTITION_MODE = "static"
WKS_FILE = "soc-monitoring-large.wks"
SOC_WKS_ROOTFS_SIZE = "4096M"
SOC_WKS_DATA_SIZE = "2048M"
```

### Signature Verification

For secure OTA updates:

```bitbake
SOC_OTA_SIGNATURE_VERIFICATION = "1"
SOC_OTA_SIGNING_KEY = "/path/to/private-key.pem"
```

## Support

- GitHub Issues: https://github.com/binaryblack/OTA-Pulse/issues
- Documentation: https://github.com/binaryblack/OTA-Pulse/docs
