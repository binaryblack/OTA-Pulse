# SoC Monitoring Daemon (socmond)

Embedded Linux monitoring daemon for the SoC Monitoring platform.

## Features

- **Telemetry Collection** - CPU, memory, disk, temperature metrics
- **Device Registration** - Automatic provisioning with server
- **Crash Capture** - Coredump collection and upload
- **OTA Updates** - Firmware update client
- **Local Dashboard** - Optional web-based device monitor (port 8080)

## Installation

Add to your image recipe:

```bitbake
IMAGE_INSTALL:append = " socmond"
```

## Optional: Local Dashboard

The local web dashboard displays real-time device status directly on the target.

### Enable at Build Time (Yocto)

Add to `local.conf` or your image recipe:

```bitbake
# Enable dashboard feature
PACKAGECONFIG:append:pn-socmond = " dashboard"
```

This adds:
- `soc-dashboard` binary
- `soc-dashboard.service` (systemd)
- `python3-core` dependency (~5MB)

### Disable Dashboard (Default)

Dashboard is disabled by default. No action needed.

```bitbake
# Explicitly disable (optional)
PACKAGECONFIG:remove:pn-socmond = "dashboard"
```

## Usage on Target

### Device Control CLI

```bash
# Show device status
soc-ctl status

# Configure server
soc-ctl config server https://monitoring.example.com

# Set provisioning token
soc-ctl config token <your-token>

# Provision device
soc-ctl provision

# Test server connection
soc-ctl test

# View logs
soc-ctl logs
soc-ctl logs -f

# Restart service
soc-ctl restart
```

### Dashboard Commands (if enabled)

```bash
# Enable and start dashboard
soc-ctl dashboard enable

# Check dashboard status
soc-ctl dashboard status

# Disable dashboard
soc-ctl dashboard disable
```

Access dashboard at: `http://<device-ip>:8080`

### Manual Service Control

```bash
# socmond service
systemctl status socmond
systemctl restart socmond

# Dashboard service (if enabled)
systemctl enable soc-dashboard
systemctl start soc-dashboard
```

## Configuration

Config file: `/etc/socmond/config.json`

```json
{
  "server": {
    "base_url": "https://monitoring.example.com"
  },
  "auth": {
    "provisioning_token": "your-token-here"
  }
}
```

## Dashboard Features

When enabled, the local dashboard shows:

- **System Metrics** - CPU, memory, disk usage with color indicators
- **Temperature** - Current CPU temperature
- **Network Status** - Interface status, IP addresses, RX/TX
- **Service Health** - socmond and watchdog status
- **Server Connection** - Connected/disconnected, last upload time
- **Device Info** - ID, firmware, hardware version
- **Recent Failures** - Error log with timestamps
- **Auto-refresh** - Updates every 5 seconds
