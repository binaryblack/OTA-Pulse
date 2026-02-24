# OTA-Pulse Buildroot Integration Guide

This guide explains how to integrate OTA-Pulse into your Buildroot-based embedded Linux system.

## Table of Contents

- [Overview](#overview)
- [Quick Start](#quick-start)
- [Prerequisites](#prerequisites)
- [Configuration](#configuration)
- [Security Requirements](#security-requirements)
- [Partition Layout](#partition-layout)
- [Building](#building)
- [Deploying](#deploying)
- [Creating OTA Artifacts](#creating-ota-artifacts)
- [Platform-Specific Notes](#platform-specific-notes)
- [Troubleshooting](#troubleshooting)

## Overview

OTA-Pulse provides robust over-the-air (OTA) firmware updates for embedded Linux devices. Key features:

- **Atomic A/B Updates**: Dual rootfs partitions ensure safe updates with automatic rollback
- **Mandatory Signature Verification**: All firmware artifacts must be cryptographically signed
- **Mender Compatible**: Uses Mender artifact format for broad ecosystem compatibility
- **Build System Agnostic**: Core agent works with Yocto, Buildroot, or any Linux build system

## Quick Start

```bash
# 1. Clone OTA-Pulse repository
git clone https://github.com/yourorg/OTA-Pulse.git

# 2. Clone Buildroot
git clone https://github.com/buildroot/buildroot.git
cd buildroot

# 3. Configure with OTA-Pulse external
make BR2_EXTERNAL=/path/to/OTA-Pulse/buildroot-otapulse menuconfig

# 4. Enable OTA-Pulse and configure required options:
#    - Target packages → OTA-Pulse → otapulse
#    - Set OTA Server URL
#    - Set Artifact Verification Public Key Path (REQUIRED)
#    - Set Device Type

# 5. Build
make
```

## Prerequisites

### Build Host Requirements

- Linux build host (Ubuntu 20.04+ recommended)
- Buildroot 2022.02 or later
- Go 1.18+ (for host tools)
- Standard build tools (gcc, make, etc.)

### Init System Requirement

OTA-Pulse **requires systemd** as the init system (`BR2_INIT_SYSTEMD=y`). The OTA agent
relies on systemd for:

- Service ordering (partition setup → provisioning → OTA agent)
- Automatic restart on failure (`Restart=on-failure`)
- Network-online target dependency for server connectivity
- Journal logging (`journalctl -u otapulse`)

If systemd is not enabled in your defconfig, the `otapulse` package will not appear in
menuconfig, and you will see a comment: *"otapulse requires systemd (BR2_INIT_SYSTEMD)"*.

All provided defconfigs (`otapulse_rpi4_defconfig`, `otapulse_qemu_aarch64_defconfig`,
`otapulse_imx8_defconfig`) already include `BR2_INIT_SYSTEMD=y`.

### Required Buildroot Packages

These are automatically selected when you enable OTA-Pulse:

- `openssl` - Cryptographic operations
- `ca-certificates` - TLS certificate verification
- `util-linux` - Partition tools (blkid, etc.)
- `e2fsprogs` - ext4 filesystem tools
- `dosfstools` - FAT filesystem tools
- `dbus` (optional) - D-Bus API support

## Configuration

### Using BR2_EXTERNAL

OTA-Pulse is provided as a BR2_EXTERNAL tree. Configure it with:

```bash
# Single external
make BR2_EXTERNAL=/path/to/OTA-Pulse/buildroot-otapulse menuconfig

# Multiple externals
make BR2_EXTERNAL="/path/to/OTA-Pulse/buildroot-otapulse:/path/to/other-external" menuconfig
```

### Configuration Options

Navigate to `Target packages → OTA-Pulse` in menuconfig:

| Option | Required | Description |
|--------|----------|-------------|
| `OTA Server URL` | **Yes** | URL of your OTA management server |
| `Artifact Verification Public Key Path` | **Yes** | Path to RSA/ECDSA public key for signature verification |
| `Secondary Verification Key` | No | Optional second key for key rotation |
| `Tenant Token` | No | Multi-tenant server authentication |
| `Device Type Identifier` | Yes | Unique device type (e.g., `rpi4`, `imx8mp-evk`) |
| `Update Poll Interval` | No | How often to check for updates (default: 1800s) |
| `Partition Mode` | No | Dynamic (first-boot) or Static (pre-allocated) |
| `Boot Slot Switching Method` | No | U-Boot env, GRUB, or file-based |

### Example Configuration

```makefile
# In your defconfig or via menuconfig
BR2_PACKAGE_OTAPULSE=y
BR2_PACKAGE_OTAPULSE_SERVER_URL="https://ota.example.com"
BR2_PACKAGE_OTAPULSE_VERIFY_KEY="/path/to/artifact-verify-key.pem"
BR2_PACKAGE_OTAPULSE_DEVICE_TYPE="my-product-v1"
BR2_PACKAGE_OTAPULSE_UPDATE_POLL_INTERVAL=1800
BR2_PACKAGE_OTAPULSE_PARTITION_STATIC=y
BR2_PACKAGE_OTAPULSE_BOOT_UBOOT_ENV=y
```

## Security Requirements

### Signature Verification is MANDATORY

OTA-Pulse enforces cryptographic signature verification for all firmware updates. This cannot be disabled.

**Why?**
- Prevents unauthorized firmware from being installed
- Protects against man-in-the-middle attacks
- Ensures firmware integrity
- Required for production deployments

### Generating Keys

```bash
# Generate RSA-4096 key pair (recommended)
openssl genpkey -algorithm RSA -out private-key.pem -pkeyopt rsa_keygen_bits:4096
openssl rsa -pubout -in private-key.pem -out artifact-verify-key.pem

# Or generate ECDSA P-384 key pair
openssl ecparam -genkey -name secp384r1 -out private-key.pem
openssl ec -in private-key.pem -pubout -out artifact-verify-key.pem
```

### Key Management

| Key | Location | Security |
|-----|----------|----------|
| **Private Key** | CI/CD server only | Never on devices, highly secured |
| **Public Key** | Built into firmware | Safe to distribute |

**Best Practices:**
1. Store private key in secure key management (HSM, Vault, AWS KMS)
2. Use separate keys for development and production
3. Plan for key rotation (configure secondary key)
4. Rotate keys annually or after any compromise

## Partition Layout

### A/B Partition Scheme

```
┌──────────────────────────────────────────────────────────┐
│ Boot (FAT32)  │ rootfs_a (ext4) │ rootfs_b (ext4) │ data │
│    64-128MB   │     1-4GB       │     1-4GB       │ 512M │
└──────────────────────────────────────────────────────────┘
```

### Dynamic vs Static Mode

**Dynamic Mode** (default):
- Minimal initial image size
- rootfs_b and data partitions created on first boot
- Faster flashing, smaller images
- Recommended for most use cases

**Static Mode**:
- All partitions pre-created in image
- Larger initial image
- Required for systems that can't resize partitions
- Use for locked-down production devices

### Platform-Specific Layouts

Use the appropriate genimage configuration:

| Platform | Configuration |
|----------|---------------|
| Generic | `genimage-ab.cfg` |
| QEMU AArch64 | `genimage-qemu-aarch64.cfg` |
| Raspberry Pi | `genimage-rpi.cfg` |
| NXP i.MX | `genimage-imx.cfg` |
| Rockchip | `genimage-rockchip.cfg` |

## Building

### Full Build

```bash
cd buildroot

# Load configuration
make BR2_EXTERNAL=/path/to/OTA-Pulse/buildroot-otapulse your_defconfig

# Or use provided example
make BR2_EXTERNAL=/path/to/OTA-Pulse/buildroot-otapulse otapulse_rpi4_defconfig

# Configure (set server URL, key path, etc.)
make menuconfig

# Build
make
```

### Output Files

After building, find outputs in `output/images/`:

| File | Description |
|------|-------------|
| `sdcard-ab-*.img` | Complete SD card image with A/B partitions |
| `rootfs.ext4` | Root filesystem image |
| `*.mender` | Mender-compatible OTA artifact (if mender-artifact installed) |

## Deploying

### Flashing Initial Image

```bash
# Write to SD card
sudo dd if=output/images/sdcard-ab-static.img of=/dev/sdX bs=4M status=progress
sync

# Or use bmaptool for faster flashing
bmaptool copy output/images/sdcard-ab-static.img /dev/sdX
```

### First Boot (Dynamic Mode)

1. Boot the device
2. OTA-Pulse firstboot script automatically:
   - Creates rootfs_b partition
   - Creates data partition
   - Initializes boot environment
3. Device is ready for OTA updates

### Testing with QEMU

The QEMU AArch64 defconfig produces a full GPT disk image (`sdcard-ab-qemu.img`)
with all four partitions pre-created. Boot it with:

```bash
qemu-system-aarch64 \
    -M virt \
    -cpu cortex-a72 \
    -m 1024 \
    -kernel output/images/Image \
    -drive file=output/images/sdcard-ab-qemu.img,format=raw,if=virtio \
    -append "root=/dev/vda2 rootfstype=ext4 rw console=ttyAMA0" \
    -nographic \
    -netdev user,id=net0,hostfwd=tcp::2222-:22 \
    -device virtio-net-device,netdev=net0
```

Key points:
- `root=/dev/vda2` — virtio disk is `vda`, partition 2 is `rootfs_a`
- All four partitions are visible via `lsblk` (boot, rootfs_a, rootfs_b, data)
- The firstboot script detects pre-existing partitions and skips creation
- SSH is available at `localhost:2222`

### Verifying Installation

```bash
# On the device
otapulse show-artifact
otapulse show-provides

# Check partition layout
lsblk

# Check data partition and OTA state
mount | grep /data
cat /data/ota/mender_boot_part

# Check service status
systemctl status otapulse
systemctl status otapulse-firstboot

# Check configuration
cat /etc/otapulse/otapulse.conf
```

## Creating OTA Artifacts

### Install mender-artifact Tool

```bash
# Using Go
go install github.com/mendersoftware/mender-artifact/cmd/mender-artifact@latest

# Or download binary
wget https://downloads.mender.io/mender-artifact/latest/mender-artifact
chmod +x mender-artifact
sudo mv mender-artifact /usr/local/bin/
```

### Create Signed Artifact

```bash
# From your new rootfs image
mender-artifact write rootfs-image \
    --device-type "my-device" \
    --artifact-name "release-1.2.0" \
    --file output/images/rootfs.ext4 \
    --key /path/to/private-key.pem \
    --compression gzip \
    --output-path release-1.2.0.mender
```

### Verify Artifact

```bash
# Verify signature with public key
mender-artifact validate release-1.2.0.mender \
    --key /path/to/artifact-verify-key.pem

# Show artifact contents
mender-artifact read release-1.2.0.mender
```

### Upload to OTA Server

Upload the `.mender` file to your OTA management server and create a deployment targeting your device type.

## Platform-Specific Notes

### Raspberry Pi

- Use U-Boot for proper A/B boot support
- Configure `fw_env.config` for U-Boot environment location
- Use `genimage-rpi.cfg` for correct partition layout

```bash
# fw_env.config example for RPi with U-Boot
# Device          Offset    Size
/dev/mmcblk0      0x3F8000  0x4000
```

### NXP i.MX

- Bootloader at 33KB offset (i.MX ROM requirement)
- Use `genimage-imx.cfg`
- May need NXP firmware blobs

### Rockchip

- idbloader at 32KB, u-boot.itb at 8MB
- Use `genimage-rockchip.cfg`
- Consider PARTUUID-based boot for reliability

### Generic ARM

- Adjust bootloader offsets for your SoC
- Configure U-Boot environment location
- Test boot slot switching before production

## Troubleshooting

### Build Errors

**"OTA-Pulse requires systemd as the init system"**
- Your defconfig does not have `BR2_INIT_SYSTEMD=y`
- Enable it in menuconfig: `System configuration → Init system → systemd`
- Or add `BR2_INIT_SYSTEMD=y` to your defconfig

**"otapulse" not visible in menuconfig**
- The package is hidden because systemd is not the selected init system
- Look for the comment *"otapulse requires systemd (BR2_INIT_SYSTEMD)"* in the package list

**"BR2_PACKAGE_OTAPULSE_VERIFY_KEY is required"**
- You must provide a verification key path
- Generate keys as described in Security section

**"Verification key file not found"**
- Check the path to your public key file
- Use absolute path or path relative to Buildroot directory

**Go build fails**
- Ensure Go 1.18+ is installed
- Check CGO dependencies (gcc, glibc headers)

### Runtime Errors

**"Signature verification failed"**
- Ensure artifact was signed with matching private key
- Check that public key on device matches
- Verify artifact with `mender-artifact validate`

**"Cannot determine boot partition"**
- Check `/proc/cmdline` for root= parameter
- Verify boot environment is properly configured
- For U-Boot: ensure `fw_env.config` is correct

**Update stuck in "downloading"**
- Check network connectivity
- Verify server URL is correct
- Check TLS certificates

### Logs

```bash
# View OTA agent logs
journalctl -u otapulse -f

# Check firstboot log
journalctl -u otapulse-firstboot

# Manual agent run with debug
otapulse -log-level debug daemon
```

## Support

- GitHub Issues: https://github.com/yourorg/OTA-Pulse/issues
- Documentation: https://github.com/yourorg/OTA-Pulse/docs
