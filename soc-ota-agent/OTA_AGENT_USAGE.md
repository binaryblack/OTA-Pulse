# SoC OTA Agent - Usage Guide

## Overview

The SoC OTA Agent is a customized Mender-based client that enables secure over-the-air firmware updates for embedded devices. It integrates with the SoC Monitoring backend and supports A/B partition updates with automatic rollback on failure.

## Table of Contents

1. [System Requirements](#system-requirements)
2. [Installation](#installation)
3. [Configuration](#configuration)
4. [Creating Firmware Updates](#creating-firmware-updates)
5. [Deploying Updates](#deploying-updates)
6. [Monitoring Updates](#monitoring-updates)
7. [Troubleshooting](#troubleshooting)

## System Requirements

### Hardware Requirements
- ARM or x86_64 processor
- Minimum 512MB RAM
- Dual partition layout (A/B partitions)
- Persistent data partition (e.g., `/data`)

### Partition Layout
```
/dev/mmcblk0p1  - U-Boot bootloader
/dev/mmcblk0p2  - Boot partition (kernel, device tree)
/dev/mmcblk0p3  - Root filesystem A (rootfs_a)
/dev/mmcblk0p4  - Root filesystem B (rootfs_b)
/dev/mmcblk0p5  - Data partition (persistent storage)
```

### Software Requirements
- Linux kernel 4.x or later
- systemd (for service management)
- Network connectivity (WiFi or Ethernet)
- Switch-boot-slot script (for bootloader integration)

## Installation

### Via Yocto Build System

The OTA agent is integrated into the Yocto image recipe:

```bash
# Add to local.conf or image recipe
IMAGE_INSTALL:append = " soc-ota-agent"

# Build the image
bitbake soc-monitoring-image
```

### Manual Installation

1. Build the agent:
```bash
cd soc-ota-agent
make build
```

2. Install the binary:
```bash
sudo install -m 0755 mender /usr/bin/soc-ota-agent
sudo ln -sf soc-ota-agent /usr/bin/mender
```

3. Install configuration:
```bash
sudo mkdir -p /etc/mender
sudo cp mender.conf /etc/mender/
```

4. Install systemd service:
```bash
sudo cp soc-ota-agent.service /etc/systemd/system/
sudo systemctl enable soc-ota-agent
sudo systemctl start soc-ota-agent
```

## Configuration

### Primary Configuration File

Edit `/etc/mender/mender.conf`:

```json
{
    "ServerURL": "http://your-server:8000",
    "RootfsPartA": "/dev/disk/by-partlabel/rootfs_a",
    "RootfsPartB": "/dev/disk/by-partlabel/rootfs_b",
    "UseFileBasedBootEnv": true,
    "UpdatePollIntervalSeconds": 1800,
    "InventoryPollIntervalSeconds": 28800,
    "RetryPollIntervalSeconds": 300,
    "RetryPollCount": 10,
    "SkipVerify": false,
    "UseSoCMonitoring": true,
    "UpdateLogPath": "/var/lib/mender/update.log"
}
```

### Configuration Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `ServerURL` | SoC Monitoring backend URL | Required |
| `RootfsPartA` | Path to partition A | `/dev/disk/by-partlabel/rootfs_a` |
| `RootfsPartB` | Path to partition B | `/dev/disk/by-partlabel/rootfs_b` |
| `UseFileBasedBootEnv` | Use file-based boot environment | `true` |
| `UpdatePollIntervalSeconds` | Check for updates interval | `1800` (30 min) |
| `SkipVerify` | Disable SSL verification | `false` |
| `UseSoCMonitoring` | Use SoC Monitoring API | `true` |

### Device Provisioning

Provision the device with API credentials:

```bash
sudo /usr/bin/soc-ota-provision
```

This creates:
- `/etc/mender/device_type` - Device type identifier
- `/var/lib/mender/device_type` - Device type copy
- Device API key registration with the server

## Creating Firmware Updates

### Step 1: Build Your Firmware Image

Build a complete root filesystem image:

```bash
# Using Yocto
bitbake soc-monitoring-image

# Output: tmp/deploy/images/your-machine/soc-monitoring-image-your-machine.ext4
```

### Step 2: Create Mender Artifact

Use the `mender-artifact` CLI tool to package your firmware:

```bash
mender-artifact write rootfs-image \
    -t radxa-cm5-io \
    -n release-v1.0.0 \
    -f soc-monitoring-image.ext4 \
    -o firmware-v1.0.0.mender
```

**Parameters:**
- `-t` : Device type (must match `/etc/mender/device_type`)
- `-n` : Artifact name (use format: `release-v{version}`)
- `-f` : Input filesystem image
- `-o` : Output Mender artifact file

### Step 3: Upload to Server

Upload via the web interface:

1. Navigate to Firmware page
2. Click "Upload Firmware"
3. Select the `.mender` file
4. Enter version (e.g., `1.0.0`)
5. Add release notes (optional)
6. Click "Upload"

## Deploying Updates

### Via Web Interface

1. Go to Firmware page
2. Find your uploaded firmware
3. Click "Deploy" button
4. Select target devices
5. Optionally configure rollout:
   - Percentage-based (e.g., 25% of devices first)
   - All devices at once
6. Click "Deploy"

### Monitoring Deployment

The agent will:
1. **Poll** for updates every 30 minutes (configurable)
2. **Download** firmware to `/data/mender/`
3. **Verify** checksum and signature (if enabled)
4. **Write** to inactive partition (A→B or B→A)
5. **Update** boot environment files in `/data/ota/`
6. **Call** `switch-boot-slot.sh` to update bootloader
7. **Reboot** to the new firmware

### Boot Environment Files

The agent manages these files in `/data/ota/`:

| File | Purpose | Values |
|------|---------|--------|
| `current_slot` | Active slot indicator | `a` or `b` |
| `mender_boot_part` | Boot partition number | `3` or `4` |
| `upgrade_available` | Update pending flag | `0` or `1` |
| `boot_count` | Boot attempt counter | `0` to `3` |

## Monitoring Updates

### Check Agent Status

```bash
# View service status
sudo systemctl status soc-ota-agent

# View agent logs
sudo journalctl -u soc-ota-agent -f

# Check update log
sudo tail -f /var/lib/mender/update.log
```

### View Current Firmware

```bash
# Check artifact info
cat /etc/mender/artifact_info

# Check boot partition
mount | grep ' / '
cat /data/ota/mender_boot_part
```

### Common Log Messages

**Normal operation:**
```
INFO: Checking for updates
INFO: No updates available
```

**Update in progress:**
```
INFO: Update available: release-v1.0.0
INFO: Downloading artifact
INFO: Installing update to /dev/mmcblk0p4
INFO: Writing boot environment
INFO: Calling switch-boot-slot.sh
INFO: Rebooting to apply update
```

**After successful reboot:**
```
INFO: Committing update
INFO: Update successfully installed
```

## Troubleshooting

### Agent Not Starting

**Check service status:**
```bash
sudo systemctl status soc-ota-agent
sudo journalctl -u soc-ota-agent --no-pager
```

**Common issues:**
- Missing configuration file: Install `/etc/mender/mender.conf`
- Invalid server URL: Check `ServerURL` in config
- Network issues: Verify connectivity to server

### Update Download Fails

**Check network and server:**
```bash
# Test server connectivity
curl http://your-server:8000/health

# Check agent logs
sudo journalctl -u soc-ota-agent | grep -i download
```

**Common issues:**
- Network timeout: Increase `RetryPollIntervalSeconds`
- Disk space: Ensure `/data` has sufficient space
- Server unreachable: Check firewall and routing

### Update Fails to Boot

The agent includes automatic rollback:

1. **Boot attempt counter** tracks failed boots
2. After **3 failed attempts**, bootloader reverts to previous partition
3. Agent logs rollback reason

**Check rollback:**
```bash
# Check boot count
cat /data/ota/boot_count

# View boot logs
dmesg | grep -i mender
```

### Read-Only Filesystem Error

The agent automatically remounts `/data` as read-write when needed.

**Manual remount:**
```bash
sudo mount -o remount,rw /data
```

**Check mount status:**
```bash
mount | grep /data
```

### Bootloader Not Switching Partitions

**Verify boot environment:**
```bash
# Check partition UUIDs (Rockchip)
sudo lsblk -o NAME,PARTUUID /dev/mmcblk0

# Active should be 614e0000-xxxx
# Inactive should be 614e0001-xxxx

# Manually switch (for testing)
sudo /usr/sbin/switch-boot-slot.sh B
```

**Check boot slot script:**
```bash
ls -l /usr/sbin/switch-boot-slot.sh
# Should exist and be executable

# Test execution
sudo /usr/sbin/switch-boot-slot.sh A
```

### Signature Verification Fails

**Disable for testing:**
```json
{
    "SkipVerify": true
}
```

**Enable with keys:**
```json
{
    "SkipVerify": false,
    "ArtifactVerifyKeys": [
        "/etc/soc-monitoring/signing-keys/active/production-rsa-public.pem"
    ]
}
```

### Agent State Issues

**Clear agent state:**
```bash
# Stop agent
sudo systemctl stop soc-ota-agent

# Clear state files
sudo rm -f /var/lib/mender/mender-store
sudo rm -f /var/lib/mender/mender-store-lock

# Restart agent
sudo systemctl start soc-ota-agent
```

## Advanced Usage

### Manual Update Trigger

```bash
# Force immediate update check
sudo systemctl restart soc-ota-agent
```

### Custom Update Scripts

Place state scripts in `/etc/mender/scripts/`:

- `ArtifactInstall_Enter_00_custom.sh` - Before installation
- `ArtifactInstall_Leave_00_custom.sh` - After installation
- `ArtifactReboot_Enter_00_custom.sh` - Before reboot
- `ArtifactCommit_Enter_00_custom.sh` - After successful boot

Example script:
```bash
#!/bin/bash
# /etc/mender/scripts/ArtifactInstall_Leave_00_backup.sh

echo "Backing up configuration..."
cp -r /etc/app-config /data/backup/
exit 0
```

Make executable:
```bash
sudo chmod +x /etc/mender/scripts/*.sh
```

### Device Inventory

The agent reports device information to the server:

```bash
# View inventory scripts
ls -l /usr/share/mender/inventory/

# Scripts include:
# - mender-inventory-hostinfo (CPU, memory)
# - mender-inventory-network (IP, MAC)
# - mender-inventory-os (kernel, distro)
```

## API Integration

### Device API Endpoints

The agent uses these endpoints:

```
GET  /api/devices/v1/deployments/device/deployments/next
POST /api/devices/v1/deployments/device/deployments/{id}/status
PUT  /api/devices/v1/inventory/device/attributes
```

### Authentication

Uses X-API-Key header with device-specific key:
```
X-API-Key: smk_xxxxxxxxxxxxx
```

Key stored in:
- `/etc/mender/mender.conf` → `SoCAPIKey`
- `/var/lib/mender/device_id` → Device UUID

## Best Practices

1. **Test updates** on a single device before mass deployment
2. **Use versioning** consistently (semantic versioning recommended)
3. **Monitor rollout** progress via the web interface
4. **Keep backups** of critical configuration in `/data`
5. **Use signature verification** in production environments
6. **Set appropriate polling intervals** to balance freshness and network usage
7. **Document changes** in release notes for each firmware version

## Support

For issues or questions:
- Check logs: `journalctl -u soc-ota-agent`
- Review update log: `/var/lib/mender/update.log`
- Contact your system administrator
- Report bugs: [GitHub Issues](https://github.com/intronix/socMonitoring/issues)

## References

- Mender Documentation: https://docs.mender.io/
- SoC Monitoring Documentation: `/home/krishna/SoCMonitoring/docs/`
- Yocto Recipe: `meta-soc-monitoring/recipes-core/soc-ota-agent/`
