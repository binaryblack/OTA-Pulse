# meta-otapulse

Yocto/OpenEmbedded layer for OTAPulse device integration. Provides OTA updates, device telemetry, crash capture, and system monitoring for embedded Linux devices.

## Features

- **OTA Updates**: Atomic A/B partition updates with automatic rollback
- **Device Telemetry**: CPU, memory, disk, network, and temperature monitoring
- **Crash Capture**: Automatic coredump collection with compression and upload
- **Watchdog Integration**: Hardware and software watchdog support
- **Reboot Tracking**: Automatic detection and reporting of reboot reasons
- **Signature Verification**: RSA/ECDSA firmware signature support

## Layer Dependencies

This layer depends on:

- **poky** (or **openembedded-core**)
- **meta-oe** (openembedded layers for additional packages)

## Supported Yocto Versions

- Kirkstone (LTS) - Recommended
- Langdale
- Mickledore
- Nanbield
- Scarthgap

## Quick Start

### 1. Add the Layer

```bash
cd /path/to/your/yocto/build
bitbake-layers add-layer /path/to/meta-otapulse
```

### 2. Configure local.conf

```bash
# Set your target machine
MACHINE = "your-machine"

# Enable systemd (required)
DISTRO_FEATURES:append = " systemd usrmerge"
INIT_MANAGER = "systemd"

# OTAPulse Configuration
OTAPULSE_SERVER_URL = "https://your-otapulse-server.com"
OTAPULSE_TENANT_TOKEN = "your-tenant-token"
OTAPULSE_DEVICE_TYPE = "${MACHINE}"

# Optional: Customize poll intervals (seconds)
OTAPULSE_UPDATE_POLL_INTERVAL = "1800"
OTAPULSE_INVENTORY_POLL_INTERVAL = "28800"
```

### 3. Build the Image

```bash
bitbake soc-monitoring-image
```

### 4. Flash and Run

```bash
# Flash the WIC image to your device
sudo dd if=soc-monitoring-image-<MACHINE>.wic of=/dev/sdX bs=4M status=progress
```

## Integration into Existing Yocto Builds

If you have an existing Yocto build (e.g., NXP i.MX BSP, TI SDK, or other vendor BSP), follow these steps:

### Step 1: Clone the Repository

```bash
cd /path/to/yocto/sources
git clone https://github.com/binaryblack/OTA-Pulse.git ota-pulse
```

### Step 2: Add the Layer

Add to your `conf/bblayers.conf`:

```bitbake
BBLAYERS += "${BSPDIR}/sources/ota-pulse/meta-otapulse"
```

### Step 3: Configure in local.conf

Add to your `conf/local.conf`:

```bitbake
# Required: Set your OTA server URL
OTA_SERVER_URL = "https://your-ota-server.com"

# Optional: Enable signature verification for production
# SOC_OTA_SIGNATURE_VERIFICATION = "1"

# Optional: Custom signing keys (if not using the keys in files/ directory)
# SOC_OTA_VERIFICATION_KEYS = "/path/to/your/production-rsa-public.pem"
```

### Step 4: Signing Keys Setup

For **development/testing**: The layer includes example placeholder keys that allow builds to succeed.

For **production**: You MUST provide your own signing keys:

**Option A** - Replace example keys:
```bash
# Copy your production keys to the files directory
cp your-production-rsa-public.pem \
   meta-otapulse/recipes-core/signing-keys/files/example-rsa-public.pem
```

**Option B** - Use custom key paths in `local.conf`:
```bitbake
SOC_OTA_VERIFICATION_KEYS = "/absolute/path/to/your/production-rsa-public.pem"
```

### Step 5: Add to Your Image

Add to your image recipe or `local.conf`:

```bitbake
IMAGE_INSTALL:append = " soc-ota-agent"
```

### Troubleshooting Common Issues

#### SPDX Generation Errors

If you see errors like:
```
ERROR: Unable to find SPDX provider for 'soc-ota-agent'
```

The `soc-ota-agent` recipe uses `externalsrc` which has limited SPDX compatibility.
The recipe already includes workarounds, but if issues persist, add to `local.conf`:

```bitbake
# Disable SPDX generation (if needed for externalsrc recipes)
INHERIT:remove = "create-spdx"
```

#### NXP i.MX BSP Specific Notes

For NXP i.MX platforms (i.MX6, i.MX8, etc.):

1. Ensure your BSP has Go cross-compilation support:
   ```bitbake
   # In local.conf, if go-cross is not available:
   PREFERRED_PROVIDER_go-native = "go-native"
   ```

2. The layer is tested with:
   - Kirkstone (LTS)
   - Scarthgap
   - NXP BSP releases (meta-imx)

#### Build Failures with Missing Dependencies

Ensure `meta-oe` is included in your `bblayers.conf`:

```bitbake
BBLAYERS += "${BSPDIR}/sources/meta-openembedded/meta-oe"
```

## Large Images and Storage Configuration

For images larger than 1GB (e.g., Qt applications, multimedia stacks), you need to configure partition sizes appropriately.

### Partition Size Configuration

Add to `local.conf`:

```bitbake
# For large images (Qt, multimedia, etc.)
# Requires 16GB+ eMMC/SD card
SOC_WKS_BOOT_SIZE = "256M"
SOC_WKS_ROOTFS_SIZE = "6G"
SOC_WKS_DATA_SIZE = "2G"

# Or use the pre-configured large WKS file
WKS_FILE = "soc-monitoring-large.wks"
```

### Storage Requirements

| Image Type | Rootfs Size | Min Storage | Recommended |
|------------|-------------|-------------|-------------|
| Minimal    | < 500MB     | 4GB         | 8GB         |
| Standard   | 500MB - 1GB | 4GB         | 8GB         |
| Large (Qt) | 1GB - 5GB   | 16GB        | 32GB        |
| Full (ML)  | 5GB+        | 32GB        | 64GB        |

**Formula**: `Total = boot + (2 × rootfs) + data + 500MB overhead`

### Optimizing Update Size

For bandwidth/storage constrained deployments with large images:

#### 1. Enable Compression

The default is gzip. For better compression (slower):

```bitbake
# In local.conf
MENDER_ARTIFACT_COMPRESSION = "lzma"
```

#### 2. Delta Updates (Recommended for Large Images)

Full 5GB+ updates are impractical for many deployments. Consider:

**Mender Delta Updates** (requires Mender Enterprise):
- Binary delta between old and new rootfs
- Typically 10-50x smaller than full updates
- Requires Mender server with delta support

**Alternative: Module Updates**
Instead of full rootfs updates, use targeted update modules:
- Application-only updates (update just your app, not entire rootfs)
- File-based updates for specific directories
- Script-based updates for configuration changes

Example application update module in `/etc/otapulse/scripts/`:

```bash
#!/bin/sh
# ArtifactInstall_Leave_00_update-app
# Update just the application binary

APP_PATH="/opt/myapp/bin/myapp"
NEW_APP="$1/myapp"

if [ -f "$NEW_APP" ]; then
    systemctl stop myapp
    cp "$NEW_APP" "$APP_PATH"
    systemctl start myapp
fi
```

#### 3. Streaming Updates

For devices with limited storage, enable streaming mode:

```bitbake
# Download and install simultaneously (reduces temp storage needs)
MENDER_FEATURES:append = " mender-client-install-streaming"
```

#### 4. Image Size Reduction Tips

```bitbake
# Remove debug symbols
DEBUG_BUILD = "0"
INHIBIT_PACKAGE_DEBUG_SPLIT = "1"

# Use minimal package selection
IMAGE_INSTALL:remove = "packagegroup-core-full-cmdline"

# Strip locales
IMAGE_LINGUAS = ""

# Remove man pages and documentation
DISTRO_FEATURES:remove = "doc"
```

## Directory Structure

```
meta-otapulse/
├── conf/
│   ├── layer.conf                    # Layer configuration
│   ├── distro/soc-monitoring.conf    # Distribution settings
│   └── samples/                      # Example configurations
├── recipes-bsp/
│   ├── linux/                        # Kernel configuration
│   │   └── files/
│   │       ├── monitoring.cfg        # Core dump, metrics
│   │       ├── watchdog.cfg          # Hardware watchdog
│   │       └── debug.cfg             # Debug symbols
│   └── u-boot/                       # Bootloader config
│       └── files/
│           ├── ab-update.cfg         # A/B partition support
│           └── fw_env.config         # Environment config
├── recipes-core/
│   ├── images/
│   │   └── soc-monitoring-image.bb   # Complete image recipe
│   ├── memfault/                     # Telemetry daemon
│   │   ├── memfaultd_1.0.0.bb        # Rust implementation
│   │   └── files/
│   │       ├── memfaultd-rs/         # Rust source
│   │       ├── config.json           # Default configuration
│   │       └── *.service             # Systemd units
│   ├── signing-keys/                 # Firmware signing keys
│   │   └── files/
│   │       └── *-public.pem          # Public verification keys
│   └── soc-ota-agent/                # OTA agent recipe
│       └── files/
│           ├── otapulse.conf.in      # Agent config template
│           └── soc-ota-agent.service # Systemd unit
├── recipes-support/
│   └── watchdog/                     # Watchdog package
└── wic/
    └── soc-monitoring.wks            # Disk image layout
```

## Configuration

### Device Configuration

The OTA agent reads configuration from `/etc/otapulse/otapulse.conf`:

```json
{
  "ServerURL": "https://your-server.com",
  "TenantToken": "your-tenant-token",
  "UpdatePollIntervalSeconds": 1800,
  "InventoryPollIntervalSeconds": 28800,
  "RetryPollIntervalSeconds": 300
}
```

### Kernel Configuration

The layer provides kernel configuration fragments:

| Fragment | Purpose |
|----------|---------|
| `monitoring.cfg` | Core dump and system metrics support |
| `watchdog.cfg` | Hardware watchdog support |
| `debug.cfg` | Debug symbols and tracing |

### A/B Partition Layout

The included WIC file (`wic/soc-monitoring.wks`) defines a dual-partition layout for safe OTA updates.

## Customization

### Adding Custom Inventory

Create inventory scripts in `/usr/share/otapulse/inventory/`:

```bash
#!/bin/sh
# otapulse-inventory-custom
echo "app_version=$(myapp --version)"
echo "custom_attribute=value"
```

### State Scripts

Add update lifecycle scripts to `/etc/otapulse/scripts/`:

```bash
# Download_Enter_00 - Run before download starts
# ArtifactInstall_Leave_00 - Run after installation
# ArtifactCommit_Enter_00 - Run before committing update
```

### Firmware Signing

1. Add your public verification key to `recipes-core/signing-keys/files/`
2. Rebuild the image
3. Sign artifacts with the corresponding private key

## Platform Support

| Architecture | Status |
|-------------|--------|
| ARM (32-bit) | Supported |
| ARM64 | Supported |
| x86_64 | Supported |
| RISC-V | Experimental |

## Verification

After flashing, verify the installation:

```bash
# Check OTA agent
systemctl status soc-ota-agent
soc-ota-agent show-artifact

# Check telemetry daemon
systemctl status memfaultd
journalctl -u memfaultd -f
```

## License

Apache-2.0
