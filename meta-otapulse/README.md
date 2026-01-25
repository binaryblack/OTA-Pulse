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
