# OTAPulse Integration Guide

This guide walks you through integrating OTAPulse into your embedded Linux product.

## Supported Build Systems

OTA-Pulse supports multiple embedded Linux build systems:

| Build System | Integration | Documentation |
|-------------|-------------|---------------|
| **Yocto/OpenEmbedded** | `meta-otapulse/` layer | This document |
| **Buildroot** | `buildroot-otapulse/` BR2_EXTERNAL | [Buildroot Integration Guide](../buildroot-otapulse/docs/BUILDROOT_INTEGRATION.md) |
| OpenWrt | Planned | [Roadmap](TODO_BUILD_SYSTEMS.md) |
| Debian/Ubuntu (.deb) | Planned | [Roadmap](TODO_BUILD_SYSTEMS.md) |
| Alpine Linux | Planned | [Roadmap](TODO_BUILD_SYSTEMS.md) |

The core OTA agent (`soc-ota-agent/`) is build-system agnostic and can be integrated with any Linux distribution.

> **Want another build system?** See the [Build System Roadmap](TODO_BUILD_SYSTEMS.md) for planned integrations or contribute your own!

---

# Yocto Integration

This section covers Yocto/OpenEmbedded integration.

## Prerequisites

- Yocto Project build environment (Kirkstone or later)
- A/B partition layout support on your target hardware
- OTAPulse server access and tenant credentials

## Step 1: Add the Layer

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

## Step 2: Configure Your Build

### local.conf

Add the following to your `conf/local.conf`:

```bash
# Enable OTAPulse (inherit in image recipe, or add here)
INHERIT += "otapulse"

# OTAPulse Server Configuration
OTA_SERVER_URL = "https://your-server.com"
OTAPULSE_PROVISIONING_TOKEN = "your-provisioning-token"

# Device Type (used for artifact compatibility)
MENDER_DEVICE_TYPE = "${MACHINE}"

# Optional: Customize poll intervals (in seconds)
OTAPULSE_UPDATE_POLL_INTERVAL = "1800"
OTAPULSE_INVENTORY_POLL_INTERVAL = "28800"
```

### A/B Partition Setup

Ensure your machine configuration supports A/B partitioning. The layer provides a WIC kickstart file as reference:

```bash
# Reference the provided WIC file or create your own
WKS_FILE = "soc-monitoring.wks.in"
```

## Step 3: Configure U-Boot

The layer includes U-Boot configuration fragments for A/B support. Ensure your U-Boot is configured to:

1. Read the boot slot from environment
2. Support boot count limiting
3. Fall back to alternate partition on failure

Add to your U-Boot append file:

```bash
SRC_URI:append = " file://ab-update.cfg"
```

## Step 4: Kernel Configuration

The layer provides kernel configuration fragments for:

- Core dump support (crash reporting)
- Hardware watchdog
- System metrics collection

These are automatically included when using the `soc-monitoring-image` recipe.

## Step 5: Build Your Image

For a complete OTAPulse-enabled image:

```bash
bitbake soc-monitoring-image
```

Or add OTAPulse to your existing image by including the package group:

```bash
# In your image recipe
IMAGE_INSTALL:append = " soc-ota-agent socmond-bin"
```

## Step 6: Device Provisioning

### Automatic Provisioning

Devices automatically register with your OTAPulse server on first boot using the configured tenant token.

### Manual Provisioning

For manual control, use the provisioning tool:

```bash
# On the device
soc-ota-provision --server https://your-server.com --token YOUR_TOKEN
```

## Step 7: Verify Integration

After flashing and booting your device:

```bash
# Check OTA agent status
systemctl status soc-ota-agent

# View current artifact info
soc-ota-agent show-artifact

# Check connectivity to server
soc-ota-agent check-update
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

## Next Steps

- [Configuration Reference](CONFIGURATION.md) - All configuration options
- [Security Guide](SECURITY.md) - Enabling signature verification
- [API Reference](API.md) - OTA agent commands and D-Bus API
