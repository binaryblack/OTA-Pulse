# OTAPulse OpenWrt Package

OpenWrt package for the OTAPulse OTA update agent.

## Features

- **Procd integration** — managed by OpenWrt's process supervisor (not systemd)
- **UCI configuration** — native OpenWrt config system (`/etc/config/otapulse`)
- **Auto-detect device type** — from OpenWrt board info on first boot
- **Flash-friendly** — config in UCI, runtime state in `/tmp/`

## Building

### Using the OpenWrt SDK

```bash
# Download the SDK for your target
tar xf openwrt-sdk-*.tar.xz
cd openwrt-sdk-*

# Add the OTAPulse package feed
echo "src-link otapulse /path/to/OTA-Pulse/openwrt-otapulse" >> feeds.conf

./scripts/feeds update otapulse
./scripts/feeds install otapulse

make package/otapulse/compile V=s
```

### Using a full OpenWrt build

```bash
# In your OpenWrt build tree
cp -r /path/to/OTA-Pulse/openwrt-otapulse/Makefile package/otapulse/Makefile
cp -r /path/to/OTA-Pulse/openwrt-otapulse/files package/otapulse/files

make menuconfig  # Select Utilities -> otapulse
make package/otapulse/compile V=s
```

## Installation

```bash
opkg update
opkg install otapulse_1.0.0-1_<arch>.ipk
```

## Configuration

OTAPulse uses the UCI configuration system:

```bash
# Set server URL (required)
uci set otapulse.server.url='https://your-server.com'

# Set tenant token (for auto-provisioning)
uci set otapulse.server.tenant_token='your-token'

# Set device type (auto-detected if not set)
uci set otapulse.device.type='my-router'

# Set partition layout
uci set otapulse.device.rootfs_part_a='/dev/mmcblk0p3'
uci set otapulse.device.rootfs_part_b='/dev/mmcblk0p4'

# Adjust poll intervals (seconds)
uci set otapulse.timing.update_poll='3600'

# Set verification key
uci set otapulse.security.verify_key='/etc/otapulse/verify-key.pem'

# Apply changes
uci commit otapulse

# Start/restart the service
/etc/init.d/otapulse restart
```

## How It Works

1. The procd init script reads UCI config (`/etc/config/otapulse`)
2. Generates a JSON config file at `/tmp/otapulse.conf`
3. Starts the OTA agent daemon under procd supervision
4. Procd automatically restarts the agent if it crashes

No changes to the agent Go code are required — the init script handles
the UCI-to-JSON translation.

## File Locations

| Path | Description |
|------|-------------|
| `/usr/bin/otapulse` | Agent binary |
| `/etc/config/otapulse` | UCI configuration |
| `/etc/init.d/otapulse` | Procd init script |
| `/etc/otapulse/device_type` | Device type (generated) |
| `/tmp/otapulse.conf` | Runtime JSON config (generated) |
| `/data/ota/` | Boot state files |
| `/usr/share/otapulse/` | Scripts and modules |

## Supported Targets

Tested on:
- `mipsel_24kc` (MT7621, common for routers)
- `aarch64_cortex-a53` (RPi, Rockchip)
- `arm_cortex-a7` (MT7620, various SoCs)
- `x86_64` (virtual machines)

## LuCI Web Interface

A LuCI configuration page is planned for a future release. For now, use UCI commands or edit `/etc/config/otapulse` directly.
