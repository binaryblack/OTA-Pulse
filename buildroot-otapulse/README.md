# OTA-Pulse Buildroot Integration

This directory contains the Buildroot integration for OTA-Pulse, providing robust over-the-air firmware updates for embedded Linux devices.

## Quick Start

```bash
# 1. Set up Buildroot with OTA-Pulse external
cd /path/to/buildroot
make BR2_EXTERNAL=/path/to/OTA-Pulse/buildroot-otapulse menuconfig

# 2. Navigate to: Target packages → OTA-Pulse → otapulse
#    Configure REQUIRED settings:
#    - OTA Server URL (your server)
#    - Artifact Verification Public Key Path (MANDATORY)
#    - Device Type Identifier

# 3. Build
make
```

## Security Notice

**Signature verification is MANDATORY and cannot be disabled.**

Generate keys before building:

```bash
# Generate RSA-4096 key pair
openssl genpkey -algorithm RSA -out private.pem -pkeyopt rsa_keygen_bits:4096
openssl rsa -pubout -in private.pem -out public.pem

# Keep private.pem secure (CI/CD only)
# Use public.pem as BR2_PACKAGE_OTAPULSE_VERIFY_KEY
```

## Directory Structure

```
buildroot-otapulse/
├── Config.in              # Top-level Kconfig menu
├── external.desc          # BR2_EXTERNAL descriptor
├── external.mk            # Package includes
├── package/
│   └── otapulse/
│       ├── Config.in      # Package configuration options
│       ├── otapulse.mk    # Build rules
│       └── *.sh           # Init scripts
├── board/
│   └── otapulse/
│       ├── genimage-*.cfg # Partition layouts
│       ├── post-build.sh  # Post-build hooks
│       └── post-image.sh  # Image generation
├── configs/
│   ├── otapulse_qemu_aarch64_defconfig
│   ├── otapulse_rpi4_defconfig
│   └── otapulse_imx8_defconfig
└── docs/
    └── BUILDROOT_INTEGRATION.md
```

## Example Configurations

| Config | Platform | Use Case |
|--------|----------|----------|
| `otapulse_qemu_aarch64_defconfig` | QEMU | Testing and development |
| `otapulse_rpi4_defconfig` | Raspberry Pi 4 | Production reference |
| `otapulse_imx8_defconfig` | NXP i.MX8 | Industrial base config |

## Documentation

See [docs/BUILDROOT_INTEGRATION.md](docs/BUILDROOT_INTEGRATION.md) for complete documentation.

## License

Apache-2.0
