# OTAPulse — Customer Integration Guide

> Last updated: 2026-03-18

## Overview

OTAPulse is a production-grade Over-The-Air (OTA) update platform for embedded Linux devices. It delivers reliable, atomic A/B partition updates with automatic rollback, ensuring devices in the field always remain operational. OTAPulse handles the full update lifecycle — from artifact creation and signing through secure delivery, installation, verification, and fleet-wide monitoring.

## Key Features

- **Atomic A/B updates** with automatic rollback on failure
- **Secure artifact signing** (RSA-4096 and ECDSA-P384)
- **Bandwidth-efficient updates** with download resume and compression
- **Fleet telemetry** — device health, crash reports, reboot reasons, and inventory tracking
- **State scripts** for custom update lifecycle hooks (pre-install checks, post-update validation, database migrations)
- **D-Bus API** for programmatic update control from your application
- **Multi-server failover** for high-availability deployments
- **Standalone (offline) update mode** — install from local `.mender` files without a server
- **Hardware watchdog integration** with automatic recovery
- **Dynamic partitioning** — A/B partitions created automatically on first boot
- **Key rotation** — zero-downtime signing key updates across your fleet

## Supported Platforms

### Architectures

| Architecture | Status |
|-------------|--------|
| ARM 32-bit (ARMv7) | Supported |
| ARM64 (ARMv8) | Supported |
| x86_64 | Supported |
| RISC-V | Experimental |

### Build Systems

| Build System | Integration Method | Documentation |
|-------------|-------------------|---------------|
| Yocto / OpenEmbedded | `meta-otapulse` layer | [Yocto Integration](#yocto--openembedded) |
| Buildroot | `buildroot-otapulse` BR2_EXTERNAL | [Buildroot Integration](#buildroot) |
| Debian / Ubuntu | `.deb` package | [Debian Integration](#debian--ubuntu) |
| OpenWrt | `.ipk` package (procd + UCI) | [OpenWrt Integration](#openwrt) |
| Generic Linux | Installer script | [Generic Integration](#generic-linux) |

### Hardware Platform Guides

| Platform | Boot Method | Status |
|----------|-------------|--------|
| NXP i.MX8M Plus/Mini/Nano | U-Boot boot.scr | Tested |
| NXP i.MX6/7 | U-Boot boot.scr | Supported |
| Rockchip RK3588/3568/3566 | U-Boot boot.scr | Supported |
| Raspberry Pi 3/4/5 | config.txt or U-Boot | Supported |
| STM32MP1 | U-Boot boot.scr | Template |
| TI AM335x/AM57xx | U-Boot boot.scr | Template |
| Allwinner (sunxi) | U-Boot boot.scr | Template |

### Tested Yocto Versions

- Kirkstone (LTS) — Recommended
- Langdale
- Mickledore
- Nanbield
- Scarthgap

## Getting Started

### Prerequisites

- **Hardware**: Target device with at least 512 MB RAM and 8 GB storage (16 GB+ recommended for A/B updates), dual-partition (A/B) layout support
- **Build environment**: Yocto Project (Kirkstone or later), Buildroot, or a Debian/Ubuntu host
- **Network**: Device must have network connectivity (Ethernet or Wi-Fi) for server-managed updates
- **Tools**: `mender-artifact` CLI (v3.10+), `openssl` (v1.1+), Go 1.20+ (if building the agent from source)

### Quick Start (QEMU)

You can evaluate OTAPulse in under 30 minutes using QEMU with no physical hardware:

1. Clone the repository and build the agent
2. Generate a configuration using the interactive wizard
3. Boot a virtual ARM64 device in QEMU
4. Create a test artifact and install it
5. Verify the update and commit

For the full walkthrough, see the [Quickstart Guide](QUICKSTART.md).

## Build System Integration

### Yocto / OpenEmbedded

**Step 1: Add the layer**

```bash
cd /path/to/your/yocto/sources
git clone https://github.com/binaryblack/OTA-Pulse.git ota-pulse

cd /path/to/your/yocto/build
bitbake-layers add-layer ../sources/ota-pulse/meta-otapulse
```

**Step 2: Configure `conf/local.conf`**

```bash
# Enable OTAPulse
INHERIT += "otapulse"

# Server configuration
OTA_SERVER_URL = "https://your-ota-server.com"
OTAPULSE_PROVISIONING_TOKEN = "your-provisioning-token"

# Device type (used for artifact compatibility checks)
MENDER_DEVICE_TYPE = "${MACHINE}"

# Partition mode (dynamic recommended — creates A/B on first boot)
OTAPULSE_PARTITION_MODE = "dynamic"

# Enable A/B boot script
OTAPULSE_BOOT_SCRIPT = "1"

# Platform-specific WKS file (choose one for your platform)
# WKS_FILE = "otapulse-dynamic-ab-imx.wks"
# WKS_FILE = "otapulse-dynamic-ab-rockchip.wks"
# WKS_FILE = "otapulse-dynamic-ab-rpi.wks"

# Optional: Poll intervals (seconds)
OTAPULSE_UPDATE_POLL_INTERVAL = "1800"
OTAPULSE_INVENTORY_POLL_INTERVAL = "28800"
```

**Step 3: Add packages to your image recipe**

```bash
IMAGE_INSTALL:append = " soc-ota-agent otapulse-firstboot"
```

**Step 4: Build**

```bash
bitbake your-image
```

On first boot, the device will automatically set up A/B partitions (in dynamic mode) and register with your OTAPulse server.

### Buildroot

```bash
# Add as BR2_EXTERNAL
make BR2_EXTERNAL=/path/to/OTA-Pulse/buildroot-otapulse menuconfig

# Enable the OTAPulse package
# Navigate to: External options → OTAPulse OTA Agent

# Build
make
```

Key Buildroot configuration options:

| Variable | Description |
|----------|-------------|
| `BR2_PACKAGE_OTAPULSE_SERVER_URL` | OTA server URL |
| `BR2_PACKAGE_OTAPULSE_VERIFY_KEY` | Primary artifact verification key |
| `BR2_PACKAGE_OTAPULSE_VERIFY_KEY_2` | Secondary verification key (for rotation) |

### Debian / Ubuntu

```bash
# Install the package
sudo dpkg -i otapulse_*.deb

# Configure
sudo nano /etc/otapulse/otapulse.conf

# Start the service
sudo systemctl enable otapulse-client
sudo systemctl start otapulse-client
```

### OpenWrt

```bash
# Install the package
opkg install otapulse_*.ipk

# Configure via UCI
uci set otapulse.config.server_url='https://your-server.com'
uci set otapulse.config.tenant_token='your-token'
uci commit otapulse

# Restart
/etc/init.d/otapulse restart
```

### Generic Linux

```bash
# Run the installer
sudo bash generic-installer/install.sh

# Follow the prompts to configure server URL, device type, and keys
```

## Configuration Reference

The main configuration file is located at `/etc/otapulse/otapulse.conf` (JSON format).

### Server Settings

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `ServerURL` | string | — | OTAPulse server URL (required unless using `Servers`) |
| `Servers` | array | — | Server list for failover (mutually exclusive with `ServerURL`) |
| `TenantToken` | string | — | Organization tenant token |
| `ServerCertificate` | string | — | Path to server CA certificate for TLS verification |

### Poll Intervals

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `UpdatePollIntervalSeconds` | int | 1800 | How often to check for updates (seconds) |
| `InventoryPollIntervalSeconds` | int | 28800 | How often to report device inventory |
| `RetryPollIntervalSeconds` | int | 300 | Retry interval after communication failures |
| `RetryPollCount` | int | 0 | Maximum retry count (0 = unlimited) |

### Security

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `ArtifactVerifyKey` | string | — | Path to artifact verification public key |
| `ArtifactVerifyKeys` | array | — | List of verification key paths (for key rotation) |
| `SkipVerify` | bool | false | Skip TLS certificate validation (development only) |

### Partition & Boot

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `RootfsPartA` | string | auto | Primary rootfs partition device path |
| `RootfsPartB` | string | auto | Secondary rootfs partition device path |
| `UseFileBasedBootEnv` | bool | false | Use file-based boot environment instead of U-Boot env |

### Agent Behavior

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `DaemonLogLevel` | string | info | Log level: debug, info, warning, error |
| `StateScriptTimeoutSeconds` | int | 3600 | Maximum time for a state script to execute |
| `ModuleTimeoutSeconds` | int | 0 | Update module timeout |

### Example Configuration

```json
{
  "ServerURL": "https://otapulse.example.com",
  "TenantToken": "eyJhbGciOiJSUzI1NiIs...",
  "UpdatePollIntervalSeconds": 1800,
  "InventoryPollIntervalSeconds": 28800,
  "RetryPollIntervalSeconds": 300,
  "ServerCertificate": "/etc/otapulse/server.crt",
  "ArtifactVerifyKey": "/etc/otapulse/artifact-verify-key.pem"
}
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `OTAPULSE_CONF_DIR` | Configuration directory (default: `/etc/otapulse`) |
| `OTAPULSE_DATA_DIR` | Data directory (default: `/usr/share/otapulse`) |
| `OTAPULSE_DATASTORE_DIR` | Runtime datastore (default: `/var/lib/otapulse`) |
| `HTTPS_PROXY` | HTTPS proxy URL |
| `NO_PROXY` | Proxy bypass list |

## Artifact Management

### Creating an Artifact

Use the `mender-artifact` CLI to package firmware images into `.mender` artifacts:

```bash
# Full rootfs update
mender-artifact write rootfs-image \
  -t your-device-type \
  -n "release-1.2.0" \
  -f your-rootfs.ext4 \
  -o release-1.2.0.mender

# Application / single-file update
mender-artifact write module-image \
  -T single-file \
  -t your-device-type \
  -n "app-update-1.0" \
  -f /path/to/file \
  -o app-update-1.0.mender
```

**Required fields:**
- `-t` — Device type (must match the target device's configured type)
- `-n` — Artifact name (use semantic versioning, e.g., `release-v1.2.0`)
- `-f` — Input file (rootfs image or application file)
- `-o` — Output `.mender` artifact path

### Signing an Artifact

```bash
mender-artifact write rootfs-image \
  -t your-device-type \
  -n "release-1.2.0" \
  -f your-rootfs.ext4 \
  --key artifact-signing-private.pem \
  -o release-1.2.0-signed.mender
```

Or sign an existing artifact:

```bash
mender-artifact sign release-1.2.0.mender -k artifact-signing-private.pem
```

### Verifying an Artifact

```bash
mender-artifact validate release-1.2.0.mender
mender-artifact read release-1.2.0.mender
```

## Deploying Updates

From the operator's perspective, the update flow is:

1. **Build and sign** — Create a `.mender` artifact from your new firmware image and sign it with your private key.
2. **Upload** — Upload the artifact to your OTAPulse server via the web dashboard or API.
3. **Deploy** — Select target devices (individual, group, or fleet-wide) and initiate the deployment.
4. **Device downloads** — Devices poll the server at the configured interval, discover the pending update, and download the artifact.
5. **Atomic installation** — The agent writes the new image to the inactive partition and updates the boot configuration.
6. **Reboot and verify** — The device reboots into the new firmware. State scripts run health checks.
7. **Commit or rollback** — If verification passes, the update is committed. If the device fails to boot or health checks fail, it automatically rolls back to the previous working version.

### Standalone (Offline) Updates

For devices without network access, install directly from a local file:

```bash
sudo soc-ota-agent install /path/to/artifact.mender
# After reboot and verification:
sudo soc-ota-agent commit
# Or to revert:
sudo soc-ota-agent rollback
```

## State Scripts

State scripts let you run custom logic at specific points in the update lifecycle. Use them to stop applications before updates, validate hardware state after rebooting, run database migrations, or notify external services.

### Lifecycle Stages

Scripts execute at `_Enter` (before) and `_Leave` (after) each stage:

| Stage | When it runs |
|-------|-------------|
| `Download` | Artifact is being downloaded |
| `ArtifactInstall` | Image is being written to the inactive partition |
| `ArtifactReboot` | Device is rebooting into the new firmware |
| `ArtifactCommit` | Update is being committed as permanent |
| `ArtifactRollback` | Device is reverting to the previous version |
| `ArtifactFailure` | Update has failed |

### Script Location and Naming

Place scripts in `/etc/otapulse/scripts/` using the naming convention:

```
<State>_<Enter|Leave|Error>_<Priority>
```

Priority is `00`–`99` (lower runs first). Scripts must be executable and exit with code `0` for success.

### Example: Validate After Reboot

```bash
#!/bin/sh
# /etc/otapulse/scripts/ArtifactCommit_Enter_00
# Return non-zero to trigger automatic rollback.

if ! systemctl is-active --quiet myapp; then
    echo "ERROR: myapp failed to start" >&2
    exit 1
fi

echo "Health checks passed"
exit 0
```

### Example: Stop Application Before Update

```bash
#!/bin/sh
# /etc/otapulse/scripts/ArtifactInstall_Enter_00
systemctl stop myapp
exit 0
```

Scripts can also be embedded directly in `.mender` artifacts using the `-s` flag with `mender-artifact write`.

For the full state scripts reference, see the [State Scripts Guide](STATE_SCRIPTS_GUIDE.md).

## D-Bus API

OTAPulse exposes a D-Bus interface for programmatic control from your application.

### Update Control

| Property | Value |
|----------|-------|
| Bus Name | `io.otapulse.UpdateManager` |
| Object Path | `/io/otapulse/UpdateManager` |
| Interface | `io.otapulse.Update1` |

**SetUpdateControlMap** — Pause or gate updates from your application:

```bash
busctl call io.otapulse.UpdateManager \
  /io/otapulse/UpdateManager \
  io.otapulse.Update1 SetUpdateControlMap s '{"priority": 1}'
```

### Authentication

| Property | Value |
|----------|-------|
| Bus Name | `io.otapulse.AuthenticationManager` |
| Object Path | `/io/otapulse/AuthenticationManager` |
| Interface | `io.otapulse.Authentication1` |

**GetJwtToken** — Retrieve the current authentication token and server URL:

```python
import dbus

bus = dbus.SystemBus()
auth_proxy = bus.get_object(
    'io.otapulse.AuthenticationManager',
    '/io/otapulse/AuthenticationManager'
)
auth = dbus.Interface(auth_proxy, 'io.otapulse.Authentication1')
token, server_url = auth.GetJwtToken()
```

**FetchJwtToken** — Force a token refresh.

**JwtTokenStateChange** — Signal emitted when the authentication token changes.

## CLI Reference

| Command | Description | Example |
|---------|-------------|---------|
| `show-artifact` | Display currently installed artifact name | `soc-ota-agent show-artifact` |
| `show-provides` | Display artifact provides (dependencies) | `soc-ota-agent show-provides` |
| `check-update` | Check if an update is available | `soc-ota-agent check-update` |
| `install <path>` | Install artifact from local file | `soc-ota-agent install update.mender` |
| `commit` | Commit the current update | `soc-ota-agent commit` |
| `rollback` | Rollback to previous version | `soc-ota-agent rollback` |
| `daemon` | Run the agent in daemon mode | `soc-ota-agent daemon` |
| `bootstrap` | Perform initial device registration | `soc-ota-agent bootstrap` |
| `send-inventory` | Force an inventory report | `soc-ota-agent send-inventory` |
| `setup` | Interactive configuration | `soc-ota-agent setup --device-type my-device --server-url https://...` |
| `snapshot dump` | Create snapshot of running rootfs | `soc-ota-agent snapshot dump > rootfs.img` |
| `--version` | Display agent version | `soc-ota-agent --version` |

### Global Flags

| Flag | Description |
|------|-------------|
| `--config`, `-c` | Path to configuration file |
| `--fallback-config` | Fallback configuration file |
| `--data` | Data store directory |
| `--log-file` | Path to log file |
| `--log-level` | Log level (debug, info, warning, error) |
| `--trusted-certs` | Path to trusted certificates |
| `--forcebootstrap` | Force device re-registration |
| `--no-syslog` | Disable syslog logging |

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Nothing to commit |
| 4 | Manual reboot required |

## Security

### Artifact Signing

Always sign artifacts before deployment. OTAPulse supports RSA (4096-bit recommended) and ECDSA (P-384 recommended) signing.

**Generate signing keys:**

```bash
# RSA 4096-bit
openssl genpkey -algorithm RSA -out signing-private.pem -pkeyopt rsa_keygen_bits:4096
openssl rsa -in signing-private.pem -pubout -out signing-public.pem

# ECDSA P-384
openssl ecparam -name secp384r1 -genkey -noout -out signing-private.pem
openssl ec -in signing-private.pem -pubout -out signing-public.pem
```

Deploy the **public** key to your devices. Keep the **private** key in an HSM or secure vault — never embed it in device images.

### Key Management Recommendations

| Key Type | Storage | Rotation Frequency |
|----------|---------|-------------------|
| Signing private key | HSM or secure vault | Annually |
| Signing public key | Embedded in device image | Updated with private key |
| Tenant token | Build system secrets | On compromise |
| Server TLS certificate | Device image | Before expiry |

### Key Rotation

OTAPulse supports zero-downtime key rotation via the `ArtifactVerifyKeys` array:

1. Generate a new key pair
2. Deploy the new public key to all devices (signed with the **old** key)
3. Once all devices have the new key, start signing with the new private key
4. Remove the old key from device configurations
5. Securely destroy the old private key

For the complete procedure, see the [Key Rotation Guide](KEY_ROTATION.md).

### Network Security

- All device-to-server communication uses **TLS 1.2+**
- Configure `ServerCertificate` to pin the server CA for enhanced security
- Use `HTTPS_PROXY` for devices behind corporate firewalls
- Disable `SkipVerify` in production (it should only be used during development)

### Production Security Checklist

- Generate unique signing keys for production
- Store private signing key in HSM or secure vault
- Enable artifact signature verification on all devices
- Use TLS with certificate verification
- Implement secure device identity (hardware serial, secure element, or TPM)
- Disable debug interfaces (UART, JTAG) on production hardware
- Remove development credentials from production images
- Set appropriate poll intervals for your fleet size

## Telemetry & Fleet Monitoring

OTAPulse reports device health metrics and events to your server for fleet-wide visibility.

### Reported Data

| Category | Metrics |
|----------|---------|
| **Device health** | CPU usage, memory usage, disk space, temperature |
| **Crash reports** | Core dumps with stack traces, crash frequency |
| **Reboot reasons** | Watchdog reset, kernel panic, user-initiated, OTA update |
| **Inventory** | Device type, kernel version, OS version, installed artifact, MAC addresses, custom attributes |
| **Update status** | Deployment progress, success/failure, rollback events |

### Custom Inventory Attributes

Add custom scripts to `/usr/share/otapulse/inventory/` to report application-specific data:

```bash
#!/bin/sh
# /usr/share/otapulse/inventory/otapulse-inventory-custom
echo "app_version=$(myapp --version)"
echo "disk_usage=$(df -h / | awk 'NR==2 {print $5}')"
```

### Device Identity

Customize how your device identifies itself by creating a script at `/usr/share/otapulse/identity/otapulse-device-identity`:

```bash
#!/bin/sh
echo "SerialNumber=$(cat /sys/firmware/devicetree/base/serial-number 2>/dev/null || echo unknown)"
echo "MAC=$(cat /sys/class/net/eth0/address 2>/dev/null || echo unknown)"
```

## Troubleshooting

### Agent Won't Start

**Check configuration syntax:**
```bash
python3 -m json.tool /etc/otapulse/otapulse.conf
```

**Verify required fields are set** (`ServerURL` and `TenantToken`) and that file permissions allow the agent to read the config directory.

**View service logs:**
```bash
systemctl status soc-ota-agent
journalctl -u soc-ota-agent -n 50
```

### Device Can't Connect to Server

- Verify network connectivity: `ping your-server.com`
- Check DNS resolution: `nslookup your-server.com`
- Test TLS: `openssl s_client -connect your-server.com:443`
- Verify firewall allows outbound port 443
- Check proxy settings if applicable (`HTTPS_PROXY`)

### Update Download Fails

- Ensure sufficient disk space (requires ~2x the artifact size)
- Check network stability for intermittent connection issues
- Verify the data directory (`/var/lib/otapulse/`) has correct permissions

### Signature Verification Fails

- Confirm the public key on the device matches the private key used to sign the artifact
- Check key file format (should start with `-----BEGIN PUBLIC KEY-----`)
- Verify the artifact is actually signed: `mender-artifact read artifact.mender`

### Device Keeps Rolling Back

- Check that your application starts successfully after update
- Add a health-check state script at `ArtifactCommit_Enter` to diagnose failures
- Verify boot count limits in the bootloader configuration

### Device Stuck in Update State

```bash
# Stop the agent, clear state, and restart
systemctl stop soc-ota-agent
rm /var/lib/otapulse/state
rm /var/lib/otapulse/*.mender
systemctl start soc-ota-agent
```

### Yocto Build Fails

- Verify layer compatibility (Kirkstone or later)
- Run `bitbake-layers show-layers` to check for conflicts
- Ensure Go toolchain is available for agent compilation
- Check disk space in the build directory

### Getting Diagnostic Information

When reporting issues, collect:
1. Agent version: `soc-ota-agent --version`
2. Configuration (remove tokens): `cat /etc/otapulse/otapulse.conf`
3. Logs: `journalctl -u soc-ota-agent --no-pager`
4. System info: `uname -a` and `cat /etc/os-release`
5. Partition layout: `lsblk`

## FAQ

**Q: Does OTAPulse require a network connection?**
A: For server-managed deployments, yes. However, OTAPulse also supports standalone (offline) mode where you install artifacts directly from a local file using `soc-ota-agent install`.

**Q: What happens if the device loses power during an update?**
A: The A/B partition scheme ensures safety. The update is written to the inactive partition, so the currently running system is never modified. If power is lost before the update completes, the device continues booting from the original partition.

**Q: How large can an update artifact be?**
A: The artifact size is limited by the available disk space on the device. You need approximately 2x the artifact size in free space (for download + inactive partition). There is no hard limit from OTAPulse itself.

**Q: Can I update individual applications without a full rootfs update?**
A: Yes. OTAPulse supports module-based updates using `mender-artifact write module-image`. You can update single files or directories without replacing the entire root filesystem.

**Q: How does automatic rollback work?**
A: After an update, the bootloader tracks boot attempts. If the device fails to boot successfully (or a health-check state script fails) within the configured number of attempts, the bootloader automatically switches back to the previous partition.

**Q: Can I use OTAPulse with my own update server?**
A: OTAPulse is compatible with Mender-protocol servers. Configure the `ServerURL` in your device configuration to point to your server instance.

**Q: How do I test updates without affecting production devices?**
A: Use the QEMU-based quickstart to test the full update flow in a virtual environment, or deploy to a staging device group before rolling out to production.

**Q: What is the minimum hardware requirement?**
A: OTAPulse requires an ARM or x86_64 processor, 512 MB RAM minimum, dual-partition storage (8 GB minimum, 16 GB+ recommended), and a Linux kernel 4.x or later with systemd.

---

*OTAPulse Documentation — Last updated 2026-03-18*
