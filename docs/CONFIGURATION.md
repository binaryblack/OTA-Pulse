# OTAPulse Configuration Reference

Complete reference for all OTAPulse configuration options.

## Agent Configuration

Configuration file: `/etc/otapulse/otapulse.conf`

### Server Settings

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `ServerURL` | string | - | OTAPulse server URL (required) |
| `TenantToken` | string | - | Organization tenant token |
| `ServerCertificate` | string | - | Path to server CA certificate |

### Poll Intervals

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `UpdatePollIntervalSeconds` | int | 1800 | How often to check for updates |
| `InventoryPollIntervalSeconds` | int | 28800 | How often to send inventory |
| `RetryPollIntervalSeconds` | int | 300 | Retry interval after failures |

### Storage

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `ArtifactVerifyKey` | string | - | Path to artifact verification key (single) |
| `ArtifactVerifyKeys` | []string | - | List of verification key paths (for key rotation) |
| `DaemonLogLevel` | string | - | Log level (debug, info, warning, error) |
| `SkipVerify` | bool | false | Skip TLS certificate validation |
| `RetryPollCount` | int | 0 | Max retry count |
| `StateScriptTimeoutSeconds` | int | 0 | State script timeout |
| `ModuleTimeoutSeconds` | int | 0 | Update module timeout |
| `UseFileBasedBootEnv` | bool | false | Use file-based boot env instead of U-Boot |
| `Servers` | []object | - | Server list for failover (mutually exclusive with ServerURL) |
| `RootfsPartA` | string | auto | Primary rootfs partition |
| `RootfsPartB` | string | auto | Secondary rootfs partition |

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

## Yocto Build Variables

Configure in `conf/local.conf`:

### Required Variables

| Variable | Description |
|----------|-------------|
| `OTA_SERVER_URL` | Server URL |
| `OTAPULSE_PROVISIONING_TOKEN` | Device provisioning token |
| `MENDER_DEVICE_TYPE` | Device type identifier |

### Optional Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `OTAPULSE_UPDATE_POLL_INTERVAL` | 1800 | Update poll interval (seconds) |
| `OTAPULSE_INVENTORY_POLL_INTERVAL` | 28800 | Inventory poll interval |
| `OTAPULSE_RETRY_POLL_INTERVAL` | 300 | Retry poll interval |
| `SOC_OTA_VERIFICATION_KEYS` | - | Public key(s) for signature verification |
| `SOC_OTA_SIGNATURE_VERIFICATION` | 0 | Enable signature verification (1/0) |
| `SOC_OTA_SIGNING_KEY` | - | Private key for build-time signing |
| `OTAPULSE_PARTITION_MODE` | dynamic | Partition mode (dynamic/static) |
| `OTAPULSE_SANITY_LEVEL` | error | Sanity check level (error/warn/off) |

### Example local.conf

```bash
# OTAPulse Configuration
INHERIT += "otapulse"

OTA_SERVER_URL = "https://ota.mycompany.com"
OTAPULSE_PROVISIONING_TOKEN = "your-token-here"
MENDER_DEVICE_TYPE = "${MACHINE}"
OTAPULSE_UPDATE_POLL_INTERVAL = "3600"
```

## Device Identity

Default identity attributes are collected from:

- Network interface MAC address (lowest-ifindex Ethernet interface)
- Custom identity scripts

### Custom Identity Script

Create `/usr/share/otapulse/identity/otapulse-device-identity`:

```bash
#!/bin/sh
# Output key=value pairs, one per line
echo "SerialNumber=$(cat /sys/firmware/devicetree/base/serial-number 2>/dev/null || echo unknown)"
echo "MAC=$(cat /sys/class/net/eth0/address 2>/dev/null || echo unknown)"
```

## Inventory Attributes

The agent collects and reports device inventory:

| Attribute | Source |
|-----------|--------|
| `device_type` | Configuration |
| `artifact_name` | Current installed artifact |
| `kernel` | `uname -r` |
| `os` | `/etc/os-release` |
| `mac_*` | Network interface MACs |
| `rootfs_type` | Filesystem type |

### Custom Inventory Scripts

Add scripts to `/usr/share/otapulse/inventory/`:

```bash
#!/bin/sh
# otapulse-inventory-custom
echo "app_version=$(myapp --version)"
echo "disk_usage=$(df -h / | awk 'NR==2 {print $5}')"
```

## State Scripts

Located in `/etc/otapulse/scripts/`, named by state and priority:

| State | Description |
|-------|-------------|
| `Idle` | Normal operation |
| `Sync` | Checking for updates |
| `Download` | Downloading artifact |
| `ArtifactInstall` | Installing update |
| `ArtifactReboot` | Rebooting |
| `ArtifactCommit` | Committing update |
| `ArtifactRollback` | Rolling back |
| `ArtifactFailure` | Update failed |

### Script Naming

`<State>_<Enter|Leave|Error>_<Priority>`

Examples:
- `Download_Enter_00` - Before download starts
- `ArtifactInstall_Leave_50` - After installation completes
- `ArtifactCommit_Enter_99` - Before committing (last script)

### Script Requirements

- Must be executable
- Exit 0 for success
- Exit non-zero to abort update
- Timeout: 60 seconds default

## Systemd Service

Service file: `/lib/systemd/system/soc-ota-agent.service`

### Status Commands

```bash
# Check status
systemctl status soc-ota-agent

# View logs
journalctl -u soc-ota-agent -f

# Restart agent
systemctl restart soc-ota-agent
```

## Environment Variables

The agent respects these environment variables:

| Variable | Description |
|----------|-------------|
| `OTAPULSE_CONF_DIR` | Configuration directory (default: /etc/otapulse) |
| `OTAPULSE_DATA_DIR` | Data directory (default: /usr/share/otapulse) |
| `OTAPULSE_DATASTORE_DIR` | Runtime datastore (default: /var/lib/otapulse) |
| `HTTPS_PROXY` | HTTPS proxy URL |
| `NO_PROXY` | Proxy bypass list |

## File Locations

| Path | Description |
|------|-------------|
| `/etc/otapulse/otapulse.conf` | Main configuration |
| `/etc/otapulse/scripts/` | State scripts |
| `/var/lib/otapulse/` | Runtime data, state |
| systemd journal | Log output (via `journalctl -u soc-ota-agent`) |
| `/usr/share/otapulse/identity/` | Identity scripts |
| `/usr/share/otapulse/inventory/` | Inventory scripts |
| `/data/otapulse/` | Persistent data partition |
