# Adding OTAPulse to an Existing Project

This guide covers adding OTA update capability to an existing Yocto or Buildroot project that is already building and running on hardware.

## Prerequisites

- A working build that produces a bootable image
- A/B partition layout (or willingness to add one)
- Network connectivity on the target device

## Yocto: Adding to an Existing Build

### Step 1: Add the Layer

```bash
cd your-yocto-dir/sources
git clone https://github.com/binaryblack/OTA-Pulse.git
bitbake-layers add-layer ../sources/OTA-Pulse/meta-otapulse
```

### Step 2: Verify Layer Compatibility

```bash
bitbake-layers show-layers
# Confirm meta-otapulse appears and has no conflicts
```

### Step 3: Minimal local.conf Changes

Add to your existing `conf/local.conf`:

```bash
# ── OTAPulse (append to existing local.conf) ──

# Required: Server and credentials
OTA_SERVER_URL = "https://your-server.com"
OTAPULSE_PROVISIONING_TOKEN = "your-token"

# Required: Device type (usually matches MACHINE)
MENDER_DEVICE_TYPE = "${MACHINE}"

# Required: Partition devices for your board
MENDER_ROOTFS_PART_A = "/dev/mmcblk0p3"
MENDER_ROOTFS_PART_B = "/dev/mmcblk0p4"

# Add OTA agent to your image
IMAGE_INSTALL:append = " soc-ota-agent"

# Ensure ext4 in image types (for .mender artifact generation)
IMAGE_FSTYPES:append = " ext4"
```

### Step 4: Partition Layout

If your image doesn't already have A/B partitions, update your WKS file:

```bash
# Use the OTAPulse reference WKS as a starting point
WKS_FILE = "soc-monitoring.wks.in"

# Or create your own based on your existing layout:
# Partition 1: boot (FAT32, ~64MB)
# Partition 2: reserved (bootloader, ~16MB, if needed)
# Partition 3: rootfs A (ext4, ~1GB)
# Partition 4: rootfs B (ext4, ~1GB)
# Partition 5: data (ext4, remaining space)
```

### Step 5: Build and Verify

```bash
bitbake your-image-recipe

# Verify the build includes OTAPulse
bash OTA-Pulse/scripts/otapulse-verify.sh build your-image-recipe
```

### Common Issues When Adding to Existing Projects

**"Package conflicts with existing mender package"**
If you previously used upstream mender, remove it first:
```bash
IMAGE_INSTALL:remove = " mender-client"
```

**"No space for A/B partitions"**
You need to resize your rootfs or use a larger storage device. Each rootfs partition needs enough space for your full image.

**"U-Boot doesn't support partition switching"**
Enable file-based boot environment (no U-Boot env tools needed):
```bash
# In local.conf
OTAPULSE_USE_FILE_BOOTENV = "1"
```

---

## Buildroot: Adding to an Existing Build

### Step 1: Add as BR2_EXTERNAL

```bash
# In your Buildroot directory
make BR2_EXTERNAL=/path/to/OTA-Pulse/buildroot-otapulse menuconfig
```

### Step 2: Enable OTAPulse

In `menuconfig`, navigate to:
```
External options → OTAPulse OTA Update Agent
```

Set the required values:
- Server URL
- Device type
- Verification key path
- Rootfs partitions A and B

### Step 3: Enable Dependencies

OTAPulse requires systemd:
```
System configuration → Init system → systemd
```

And these packages:
```
Target packages → Libraries → Crypto → openssl
Target packages → System tools → util-linux
```

### Step 4: Build

```bash
make
```

### Step 5: Verify

```bash
bash OTA-Pulse/scripts/otapulse-precheck.sh /path/to/buildroot/output
bash OTA-Pulse/scripts/otapulse-verify.sh /path/to/buildroot/output
```

---

## Debian/Ubuntu: Adding to an Existing System

For systems already running Debian or Ubuntu:

```bash
# Install the .deb package
sudo dpkg -i otapulse_1.0.0-1_arm64.deb
sudo apt-get install -f

# Configure
sudo otapulse-configure.sh --output /etc/otapulse/otapulse.conf

# Start
sudo systemctl enable --now otapulse-client
```

See [debian-otapulse/README.md](../debian-otapulse/README.md) for details.

---

## Generic Linux: Adding to Any System

Use the generic installer for any Linux distribution:

```bash
sudo bash generic-installer/install.sh
```

See [generic-installer/README.md](../generic-installer/README.md) for details.

---

## Post-Integration Checklist

After adding OTAPulse to your project:

- [ ] Agent binary present in rootfs (`/usr/bin/otapulse`)
- [ ] Configuration file created (`/etc/otapulse/otapulse.conf`)
- [ ] Device type file set (`/etc/otapulse/device_type`)
- [ ] Server URL is not a placeholder
- [ ] Verification key installed (for production)
- [ ] Service enabled to start on boot
- [ ] A/B partition layout configured
- [ ] Test OTA update on a development device
- [ ] Test rollback scenario
- [ ] Run `otapulse-verify.sh` on the final build
