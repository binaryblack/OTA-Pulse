# OTA-Pulse Platform Integration Guides

Welcome to the OTA-Pulse integration documentation. This directory contains step-by-step guides for integrating OTA-Pulse into your Yocto-based embedded Linux project.

## Quick Start

1. Choose your platform from the table below
2. Follow the platform-specific guide
3. Build, flash, and test

## Platform Support Matrix

| Platform | Guide | Boot Method | Status | Maintainer |
|----------|-------|-------------|--------|------------|
| **NXP i.MX8** | [imx-integration.md](imx-integration.md) | U-Boot boot.scr | ✅ Tested | OTA-Pulse Team |
| **NXP i.MX6/7** | [imx-integration.md](imx-integration.md) | U-Boot boot.scr | ⚠️ Supported | Community |
| **Rockchip RK3588** | [rockchip-integration.md](rockchip-integration.md) | U-Boot boot.scr | ⚠️ Supported | Community |
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

## What You'll Need

### Prerequisites (All Platforms)

- **Yocto Project**: Scarthgap (4.0) or later recommended
- **Storage**: Minimum 8GB (16GB+ recommended for comfortable A/B updates)
- **U-Boot**: With FAT filesystem support (most boards)
- **Your BSP layer**: Platform-specific meta layer

### What OTA-Pulse Provides

| Component | Description |
|-----------|-------------|
| `soc-ota-agent` | OTA client daemon (Go binary) |
| `otapulse-firstboot` | First-boot partition setup |
| `otapulse-boot-script` | U-Boot boot script for A/B switching |
| `memfaultd` | (Optional) Monitoring and diagnostics |

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

## Common Configuration Options

Add these to your `local.conf`:

```bitbake
# Required
OTA_SERVER_URL = "https://your-ota-server.com"

# Partition Mode
OTAPULSE_PARTITION_MODE = "dynamic"  # or "static"

# Boot Script (for U-Boot platforms)
OTAPULSE_BOOT_SCRIPT = "1"

# WKS File (platform-specific)
WKS_FILE = "otapulse-dynamic-ab-<platform>.wks"

# Optional: Security
# SOC_OTA_SIGNATURE_VERIFICATION = "1"
# SOC_OTA_SIGNING_KEY = "/path/to/key.pem"
```

## OTA Update Flow

```
1. Build artifact    →  soc-ota-agent write-artifact ...
2. Upload to server  →  Web UI or API
3. Deploy            →  Select devices, deploy
4. Device downloads  →  Automatic (soc-ota-agent daemon)
5. Install to slot B →  Writes to inactive partition
6. Update boot slot  →  mender_boot_part = 3
7. Reboot            →  Boots into new rootfs
8. Health check      →  Agent verifies, commits or rollback
```

## Quick Verification

After booting your OTA-Pulse enabled image:

```bash
# Check partitions
lsblk -o NAME,SIZE,LABEL,PARTLABEL

# Check OTA agent
systemctl status soc-ota-agent

# Configure and test
soc-ctl config apikey <your-key>
soc-ctl config server <your-server>
soc-ctl restart
soc-ctl status
```

## Getting Help

- **GitHub Issues**: [OTA-Pulse Issues](https://github.com/binaryblack/OTA-Pulse/issues)
- **Platform-Specific**: See individual integration guides
- **Yocto Help**: [Yocto Project Documentation](https://docs.yoctoproject.org/)

## Contributing

We welcome contributions! If you:
- Successfully integrate on a new platform → Share your config
- Find issues → Report on GitHub
- Improve documentation → Submit a PR

See [CONTRIBUTING.md](../CONTRIBUTING.md) for guidelines.
