# OTA-Pulse Platform Integration Guides

Welcome to the OTA-Pulse integration documentation. This is the canonical entry point for integrating OTA-Pulse into an embedded Linux product, covering Yocto/OpenEmbedded end to end plus per-platform notes.

## Quick Start

1. Choose your platform from the table below
2. Follow the platform-specific guide
3. Build, flash, and test

## Platform Support Matrix

| Platform | Guide | Boot Method | Status | Maintainer |
|----------|-------|-------------|--------|------------|
| **NXP i.MX8** | [imx-integration.md](imx-integration.md) | U-Boot boot.scr | ✅ Tested | OTA-Pulse Team |
| **NXP i.MX6/7** | [imx-integration.md](imx-integration.md) | U-Boot boot.scr | ⚠️ Supported | Community |
| **Rockchip RK3588 / Radxa CM5** | [rockchip-integration.md](rockchip-integration.md) | U-Boot boot.scr + systemd-boot EFI ABA | ✅ Tested | OTA-Pulse Team |
| **Rockchip RK3568/66** | [rockchip-integration.md](rockchip-integration.md) | U-Boot boot.scr | ⚠️ Supported | Community |
| **Raspberry Pi 4/5** | [raspberrypi-integration.md](raspberrypi-integration.md) | config.txt / U-Boot | ⚠️ Supported | Community |
| **STM32MP1** | [generic-arm-integration.md](generic-arm-integration.md) | U-Boot boot.scr | ⚠️ Template | Community |
| **TI AM335x/57xx** | [generic-arm-integration.md](generic-arm-integration.md) | U-Boot boot.scr | ⚠️ Template | Community |
| **Allwinner** | [generic-arm-integration.md](generic-arm-integration.md) | U-Boot boot.scr | ⚠️ Template | Community |
| **Other ARM** | [generic-arm-integration.md](generic-arm-integration.md) | U-Boot boot.scr | ⚠️ Template | Community |

**Legend:**
- ✅ **Tested**: Fully tested and production-ready
- ⚠️ **Supported**: Should work, needs community testing
- ⚠️ **Template**: Requires customization for your specific board

## Other Build Systems

Yocto/OpenEmbedded is the primary supported build system (this document). OTA-Pulse also supports:

| Build System | Integration | Documentation |
|-------------|-------------|---------------|
| **Buildroot** | `buildroot-otapulse/` BR2_EXTERNAL | [Buildroot Integration Guide](../../buildroot-otapulse/docs/BUILDROOT_INTEGRATION.md) |
| OpenWrt / Debian / Alpine / others | Planned | [Roadmap](../../ROADMAP.md) |

The core OTA agent (`soc-ota-agent/`) is build-system agnostic and can be integrated with any Linux distribution.

## What You'll Need

### Prerequisites (All Platforms)

- **Yocto Project**: Scarthgap (5.0) or later recommended (Kirkstone 4.0 LTS also supported)
- **Storage**: Minimum 8GB (16GB+ recommended for comfortable A/B updates)
- **U-Boot**: With FAT filesystem support (most boards)
- **Your BSP layer**: Platform-specific meta layer
- **OTAPulse server access and tenant credentials**

### What OTA-Pulse Provides

| Component | Description |
|-----------|-------------|
| `soc-ota-agent` | OTA client daemon (Go binary) |
| `otapulse-firstboot` | First-boot partition setup |
| `otapulse-boot-script` | U-Boot boot script for A/B switching |
| `socmond` | (Optional) Monitoring and diagnostics |
| `soc-ctl` | Device control CLI (status, provisioning, config) |

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                         Your Image                               │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐  │
│  │ Your App    │  │ Your App    │  │ Your Custom Packages    │  │
│  └─────────────┘  └─────────────┘  └─────────────────────────┘  │
├─────────────────────────────────────────────────────────────────┤
│                      OTA-Pulse Layer                             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐  │
│  │soc-ota-agent│  │ firstboot   │  │ boot-script (optional)  │  │
│  └─────────────┘  └─────────────┘  └─────────────────────────┘  │
├─────────────────────────────────────────────────────────────────┤
│                    Your BSP Layer                                │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐  │
│  │ Kernel      │  │ U-Boot      │  │ Device Tree             │  │
│  └─────────────┘  └─────────────┘  └─────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

## Partition Layout (Dynamic Mode)

OTA-Pulse uses dynamic partitioning by default, creating A/B partitions on first boot:

```
Initial Flash (minimal):          After First Boot:
┌────────────────────┐            ┌────────────────────┐
│ Bootloader         │            │ Bootloader         │
├────────────────────┤            ├────────────────────┤
│ boot (FAT32)       │            │ boot (FAT32)       │
├────────────────────┤            ├────────────────────┤
│ rootfs_a (ext4)    │            │ rootfs_a (ext4)    │
├────────────────────┤            ├────────────────────┤
│                    │            │ rootfs_b (ext4)    │  ← Created
│   Free Space       │            ├────────────────────┤
│                    │            │ data (ext4)        │  ← Created
└────────────────────┘            └────────────────────┘
```

Partition resolution supports both dynamic (`OTAPULSE_PARTITION_MODE = "dynamic"`) and static layouts. When partitions aren't at fixed device-node offsets (e.g. board revisions that renumber `mmcblk*` nodes), OTA-Pulse resolves the active/inactive rootfs by **GPT partlabel** rather than a hardcoded device path — see the platform-specific guides for boards where this matters.

## Yocto Integration Walkthrough

### Step 1: Add the Layer

Clone or copy the `meta-otapulse` layer to your Yocto layers directory:

```bash
cd /path/to/your/yocto/sources
cp -r /path/to/OTA-Pulse/meta-otapulse .
```

Add the layer to your build:

```bash
cd /path/to/your/yocto/build
bitbake-layers add-layer ../sources/meta-otapulse
```

### Step 2: Configure Your Build

Add the following to your `conf/local.conf`:

```bitbake
# Enable OTAPulse (inherit in image recipe, or add here)
INHERIT += "otapulse"

# Required
OTA_SERVER_URL = "https://your-ota-server.com"
OTAPULSE_PROVISIONING_TOKEN = "your-provisioning-token"

# Device Type (used for artifact compatibility)
MENDER_DEVICE_TYPE = "${MACHINE}"

# Partition Mode
OTAPULSE_PARTITION_MODE = "dynamic"  # or "static"

# Boot Script (for U-Boot platforms)
OTAPULSE_BOOT_SCRIPT = "1"

# WKS File (platform-specific)
WKS_FILE = "otapulse-dynamic-ab-<platform>.wks"

# Optional: Customize poll intervals (in seconds)
OTAPULSE_UPDATE_POLL_INTERVAL = "1800"
OTAPULSE_INVENTORY_POLL_INTERVAL = "28800"

# Optional: Security
# SOC_OTA_SIGNATURE_VERIFICATION = "1"
# SOC_OTA_SIGNING_KEY = "/path/to/key.pem"
```

### Step 3: Configure U-Boot

The layer includes U-Boot configuration fragments for A/B support. Ensure your U-Boot is configured to:

1. Read the boot slot from environment
2. Support boot count limiting
3. Fall back to alternate partition on failure

Add to your U-Boot append file:

```bash
SRC_URI:append = " file://ab-update.cfg"
```

### Step 4: Kernel Configuration

The layer provides kernel configuration fragments for:

- Core dump support (crash reporting)
- Hardware watchdog
- System metrics collection

These are automatically included when using the `soc-monitoring-image` recipe.

### Step 5: Build Your Image

For a complete OTAPulse-enabled image:

```bash
bitbake soc-monitoring-image
```

Or add OTAPulse to your existing image by including the package group:

```bash
# In your image recipe
IMAGE_INSTALL:append = " soc-ota-agent memfaultd-bin"
```

### Step 6: Device Provisioning

**Automatic**: devices automatically register with your OTAPulse server on first boot using the configured tenant token.

**Manual**: for manual control, use the provisioning tool on the device:

```bash
soc-ota-provision --server https://your-server.com --token YOUR_TOKEN
```

### Step 7: Verify Integration

After flashing and booting your device:

```bash
# Check partitions
lsblk -o NAME,SIZE,LABEL,PARTLABEL

# Check OTA agent status
systemctl status soc-ota-agent

# View current artifact info
soc-ota-agent show-artifact

# Check connectivity to server
soc-ota-agent check-update

# Configure and test via soc-ctl
soc-ctl config apikey <your-key>
soc-ctl config server <your-server>
soc-ctl restart
soc-ctl status
```

## Creating Your First Update

### 1. Build a New Image

Make your changes and rebuild:

```bash
bitbake soc-monitoring-image
```

### 2. Create the Artifact

```bash
mender-artifact write rootfs-image \
  -t your-device-type \
  -n "release-1.0.1" \
  -f tmp/deploy/images/your-machine/soc-monitoring-image-your-machine.ext4 \
  -o release-1.0.1.mender
```

### 3. Upload and Deploy

Upload the artifact to your OTAPulse server and create a deployment targeting your device fleet.

## OTA Update Flow

```
1. Build artifact    →  mender-artifact write rootfs-image ...
2. Upload to server  →  Web UI or API
3. Deploy            →  Select devices, deploy
4. Device downloads  →  Automatic (soc-ota-agent daemon)
5. Install to slot B →  Writes to inactive partition
6. Update boot slot  →  mender_boot_part file updated
7. Reboot            →  Boots into new rootfs
8. Health check      →  Agent verifies, commits or rollback
```

## Customization

### Custom Device Identity

Override the default device identity script:

```bash
# /usr/share/otapulse/identity/otapulse-device-identity
#!/bin/sh
echo "SerialNumber=$(cat /sys/class/dmi/id/product_serial)"
echo "MAC=$(cat /sys/class/net/eth0/address)"
```

### Custom Inventory

Add custom inventory attributes:

```bash
# /usr/share/otapulse/inventory/otapulse-inventory-custom
#!/bin/sh
echo "firmware_version=$(cat /etc/firmware-version)"
echo "hardware_revision=$(cat /sys/class/hwrev)"
```

### State Scripts

Implement custom logic at update stages:

```bash
# /etc/otapulse/scripts/ArtifactInstall_Enter_00
#!/bin/sh
# Stop application before update
systemctl stop myapp
exit 0
```

## Troubleshooting

### Agent Not Starting

Check configuration:
```bash
cat /etc/otapulse/otapulse.conf
journalctl -u soc-ota-agent
```

### Connection Issues

Verify network and server URL:
```bash
curl -v https://your-server.com/api/devices/v1/authentication/auth_requests
```

### Update Failures

Check deployment log:
```bash
cat /var/lib/otapulse/deployment.log
```

See [TROUBLESHOOTING.md](../TROUBLESHOOTING.md) for more general diagnostics.

## Next Steps

- [Configuration Reference](../CONFIGURATION.md) - All configuration options
- [Security Guide](../SECURITY.md) - Enabling signature verification
- [API Reference](../API.md) - OTA agent commands and D-Bus API

## Getting Help

- **GitHub Issues**: [OTA-Pulse Issues](https://github.com/binaryblack/OTA-Pulse/issues)
- **Platform-Specific**: See individual integration guides
- **Yocto Help**: [Yocto Project Documentation](https://docs.yoctoproject.org/)

## Contributing

We welcome contributions! If you:
- Successfully integrate on a new platform → Share your config
- Find issues → Report on GitHub
- Improve documentation → Submit a PR

See [CONTRIBUTING.md](../../soc-ota-agent/CONTRIBUTING.md) for guidelines.
