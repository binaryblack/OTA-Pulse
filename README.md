# OTAPulse

[![Test](https://github.com/binaryblack/OTA-Pulse/actions/workflows/test.yml/badge.svg)](https://github.com/binaryblack/OTA-Pulse/actions/workflows/test.yml)
[![Build Agent](https://github.com/binaryblack/OTA-Pulse/actions/workflows/build-agent.yml/badge.svg)](https://github.com/binaryblack/OTA-Pulse/actions/workflows/build-agent.yml)

**Secure Over-The-Air (OTA) Update Solution for Embedded Linux Devices**

OTAPulse is a complete OTA update solution designed for embedded Linux systems. It provides reliable, atomic A/B partition updates with automatic rollback capabilities, ensuring your devices always remain operational.

## Features

- **Atomic A/B Updates**: Dual partition scheme ensures safe updates with automatic fallback
- **Secure Boot Chain**: RSA/ECDSA firmware signature verification
- **Robust Recovery**: Automatic rollback on failed updates
- **Bandwidth Efficient**: Resume interrupted downloads, delta updates support
- **Hardware Watchdog**: System health monitoring with automatic recovery
- **Fleet Telemetry**: Device metrics, logs, and crash reporting
- **Yocto Integration**: Complete OpenEmbedded/Yocto layer for easy integration

## Repository Structure

```
OTA-Pulse/
├── meta-otapulse/          # Yocto/OpenEmbedded layer
├── buildroot-otapulse/     # Buildroot BR2_EXTERNAL
├── debian-otapulse/        # Debian/Ubuntu .deb packaging
├── openwrt-otapulse/       # OpenWrt package (procd + UCI)
├── generic-installer/      # Generic Linux installer
│
├── soc-ota-agent/          # OTA Client Agent (Go)
│   ├── app/                # Core application logic
│   ├── cli/                # Command-line interface
│   ├── client/             # HTTP client implementation
│   ├── installer/          # Firmware installation
│   ├── examples/           # Configuration examples
│   └── support/            # Utilities and scripts
│
├── scripts/                # Build and configuration tools
├── docker/                 # Reproducible build environment
├── tests/                  # Integration test suite
├── docs/                   # Documentation
└── .github/workflows/      # CI/CD pipelines
```

## Quick Start

> **New to OTAPulse?** Start with the [Quickstart Guide](docs/QUICKSTART.md) — get from zero to your first OTA update in 30 minutes using QEMU (no hardware needed).

### 1. Add the Yocto Layer

Add `meta-otapulse` to your Yocto build:

```bash
# In your build directory
bitbake-layers add-layer /path/to/OTA-Pulse/meta-otapulse
```

Update `conf/local.conf`:

```bash
# Enable OTAPulse features
DISTRO_FEATURES:append = " otapulse"

# Set your OTAPulse server URL
OTAPULSE_SERVER_URL = "https://your-otapulse-server.com"

# Set your organization credentials
OTAPULSE_TENANT_TOKEN = "your-tenant-token"
```

### 2. Build the Image

```bash
bitbake soc-monitoring-image
```

### 3. Deploy and Provision

Flash the image to your device. On first boot, the device will automatically provision with your OTAPulse server.

## Supported Platforms

| Architecture | Status |
|-------------|--------|
| ARM (32-bit) | Supported |
| ARM64 | Supported |
| x86_64 | Supported |
| RISC-V | Experimental |

### Installation Options

| Method | Target | Command |
|--------|--------|---------|
| Yocto layer | Embedded builds | `bitbake-layers add-layer meta-otapulse` |
| Buildroot | Embedded builds | `make BR2_EXTERNAL=buildroot-otapulse menuconfig` |
| Debian package | Debian/Ubuntu | `sudo dpkg -i otapulse_*.deb` |
| OpenWrt package | Routers/gateways | `opkg install otapulse_*.ipk` |
| Generic installer | Any Linux | `sudo bash generic-installer/install.sh` |
| Docker build | CI/CD | `docker/build.sh all` |

### Tested Yocto Versions

- Kirkstone (LTS) - Recommended
- Langdale
- Mickledore
- Nanbield
- Scarthgap

## Documentation

| Document | Description |
|----------|-------------|
| [Quickstart Guide](docs/QUICKSTART.md) | Zero-to-first-OTA in 30 minutes |
| [Integration Guide](docs/INTEGRATION.md) | Build system integration walkthrough |
| [Adding to Existing Project](docs/INTEGRATING_EXISTING_PROJECT.md) | Add OTA to your existing build |
| [Configuration Reference](docs/CONFIGURATION.md) | All configuration options |
| [API Reference](docs/API.md) | OTA agent API documentation |
| [Security Guide](docs/SECURITY.md) | Security best practices |
| [Key Rotation](docs/KEY_ROTATION.md) | Rotating signing keys safely |
| [State Scripts](docs/STATE_SCRIPTS_GUIDE.md) | Custom update lifecycle hooks |
| [Docker Build Env](docs/DOCKER_BUILD_ENV.md) | Reproducible builds |
| [Troubleshooting](docs/TROUBLESHOOTING.md) | Common issues and solutions |
| [Build System Roadmap](docs/TODO_BUILD_SYSTEMS.md) | Planned integrations |

## OTA Agent Commands

```bash
# Check device status
soc-ota-agent show-artifact

# Check for updates
soc-ota-agent check-update

# Install update from file
soc-ota-agent install /path/to/artifact.mender

# View pending deployment
soc-ota-agent show-provides

# Commit current update (after verification)
soc-ota-agent commit

# Rollback to previous version
soc-ota-agent rollback
```

## Configuration

### Device Configuration

The OTA agent configuration is located at `/etc/otapulse/otapulse.conf`:

```json
{
  "ServerURL": "https://your-server.com",
  "TenantToken": "your-tenant-token",
  "UpdatePollIntervalSeconds": 1800,
  "InventoryPollIntervalSeconds": 28800,
  "RetryPollIntervalSeconds": 300
}
```

### State Scripts

OTAPulse supports custom scripts that run at various stages of the update process:

```
/etc/otapulse/scripts/
├── Download_Enter_00     # Before download starts
├── Download_Leave_00     # After download completes
├── ArtifactInstall_Enter_00
├── ArtifactReboot_Enter_00
├── ArtifactCommit_Enter_00
└── ...
```

See [examples/state-scripts/](soc-ota-agent/examples/state-scripts/) for reference implementations.

## Building the OTA Agent

If you need to build the OTA agent separately:

```bash
cd soc-ota-agent
make build
```

For cross-compilation (CGO requires a cross-compiler):

```bash
CC=aarch64-linux-gnu-gcc GOOS=linux GOARCH=arm64 make build
CC=arm-linux-gnueabihf-gcc GOOS=linux GOARCH=arm GOARM=7 make build
```

## Creating Update Artifacts

Use the artifact generation tools to create OTA packages:

```bash
# Full rootfs update
./support/modules-artifact-gen/single-file-artifact-gen \
  --artifact-name release-1.2.0 \
  --device-type your-device \
  --file rootfs.ext4 \
  --output-path release-1.2.0.mender

# Application update
./support/modules-artifact-gen/directory-artifact-gen \
  --artifact-name app-update-1.0 \
  --device-type your-device \
  --dest-dir /opt/myapp \
  --source-dir ./app-files \
  --output-path app-update-1.0.mender
```

## Security

### Firmware Signing

All firmware artifacts should be signed before deployment:

1. Generate signing keys (keep private key secure)
2. Sign artifacts during CI/CD pipeline
3. Deploy public key to devices via the Yocto layer
4. OTA agent verifies signatures before installation

See [docs/SECURITY.md](docs/SECURITY.md) for detailed security configuration.

## Support

For integration support and questions:

- Documentation: [docs/](docs/)
- Examples: [soc-ota-agent/examples/](soc-ota-agent/examples/)

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](soc-ota-agent/LICENSE) file for details.

---

**OTAPulse** - Reliable OTA Updates for Embedded Linux
