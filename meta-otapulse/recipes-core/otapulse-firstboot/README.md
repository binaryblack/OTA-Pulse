# OTAPulse Dynamic Partitioning

Creates A/B partitions dynamically on first boot, enabling minimal image sizes.

## Problem

Traditional A/B OTA implementations require pre-allocated partitions in the image:
- **Static A/B**: 9-17GB images (boot + rootfs_a + rootfs_b + data)
- **Flashing issues**: Large images fail with Rufus, RPi Imager (stuck at 99.9%)
- **Platform specific**: Each platform needs custom WKS files with correct sizes

## Solution

Dynamic partitioning creates A/B partitions at runtime:
- **Minimal image**: Only boot + rootfs_a (~4-6GB for Qt images)
- **First boot**: Creates rootfs_b and data partitions using free space
- **Platform agnostic**: Works on any storage size (16GB, 32GB, 64GB)
- **Fast flashing**: Small images flash reliably with any tool

## Image Size Comparison

| Mode | Uncompressed | Description |
|------|--------------|-------------|
| Base (no A/B) | 3.5 GB | Single rootfs |
| Static A/B | 9-17 GB | Pre-allocated partitions |
| **Dynamic A/B** | **4-6 GB** | Minimal, A/B created at runtime |

## Usage

### 1. Add to local.conf

```bitbake
# Enable dynamic partitioning (default)
OTAPULSE_PARTITION_MODE = "dynamic"

# Use dynamic WKS template
WKS_FILE = "otapulse-dynamic-ab-imx.wks"
```

### 2. Add to image recipe

```bitbake
inherit otapulse  # Automatically includes otapulse-firstboot in dynamic mode
```

Or explicitly:
```bitbake
IMAGE_INSTALL:append = " otapulse-firstboot"
```

## How It Works

### Initial Image Layout
```
+----------+--------+------------+-------------+
| imx-boot |  boot  |  rootfs_a  | free space  |
+----------+--------+------------+-------------+
   32KB      64MB     ~4-5GB     (remaining)
```

### After First Boot
```
+----------+--------+------------+------------+--------+
| imx-boot |  boot  |  rootfs_a  |  rootfs_b  |  data  |
+----------+--------+------------+------------+--------+
   32KB      64MB     ~4-5GB      ~4-5GB      ~2GB+
```

## First Boot Process

1. `otapulse-partition-setup.service` runs before OTA agent
2. Detects boot device and rootfs_a size
3. Creates rootfs_b partition (same size as rootfs_a)
4. Creates data partition (remaining space, minimum 1GB)
5. Formats partitions with ext4 and sets GPT labels
6. Creates marker file to skip on subsequent boots

## Partition Labels

The script sets GPT partition labels for OTA agent:
- `/dev/disk/by-partlabel/boot`
- `/dev/disk/by-partlabel/rootfs_a`
- `/dev/disk/by-partlabel/rootfs_b`
- `/dev/disk/by-partlabel/data`

## Requirements

- GPT partition table (set in WKS)
- `gptfdisk` package (sgdisk command)
- `e2fsprogs` package (mkfs.ext4)
- Sufficient free space on storage device

## Troubleshooting

### Check partition setup status
```bash
systemctl status otapulse-partition-setup
journalctl -u otapulse-partition-setup
```

### Verify partitions were created
```bash
ls -la /dev/disk/by-partlabel/
lsblk -o NAME,SIZE,LABEL,PARTLABEL,FSTYPE
```

### Re-run partition setup (if needed)
```bash
rm /var/lib/otapulse/.partitions-created
systemctl start otapulse-partition-setup
```

## Static Mode (Alternative)

If you prefer pre-allocated partitions:

```bitbake
OTAPULSE_PARTITION_MODE = "static"
WKS_FILE = "your-static-ab.wks"  # With fixed partition sizes
```

Static mode does not include `otapulse-firstboot`.

## Cross-Platform Support

### Included WKS Templates

| Template | Platform | Bootloader |
|----------|----------|------------|
| `otapulse-dynamic-ab-imx.wks` | NXP i.MX8 | imx-boot @ 32KB |
| `otapulse-dynamic-ab-rpi.wks` | Raspberry Pi | Boot partition |
| `otapulse-dynamic-ab-generic.wks` | Template | Customize |

### Adding Support for New Platforms

1. Copy `otapulse-dynamic-ab-generic.wks` to your layer
2. Add bootloader section for your platform:

```bash
# i.MX8:
part u-boot --source rawcopy --sourceparams="file=imx-boot" --no-table --align 32

# STM32MP:
part fsbl --source rawcopy --sourceparams="file=tf-a.stm32" --no-table --align 17

# Rockchip:
part idbloader --source rawcopy --sourceparams="file=idbloader.img" --no-table --align 64
```

3. Set in local.conf:
```bitbake
WKS_FILE = "your-platform-ab.wks"
```

### Requirements for All Platforms

- GPT partition table (for partition labels)
- `gptfdisk` package (sgdisk command)
- `e2fsprogs` package (mkfs.ext4)
- Sufficient free space after rootfs_a
