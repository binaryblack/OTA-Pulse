# OTAPulse Agent

The OTAPulse Agent is the device-side component that manages over-the-air (OTA) software updates on embedded Linux devices. It provides atomic A/B partition updates with automatic rollback capabilities.

## Features

- **Atomic Updates**: A/B partition scheme ensures devices never brick during updates
- **Automatic Rollback**: Failed updates automatically revert to the previous working version
- **Resume Support**: Interrupted downloads resume from where they left off
- **Signature Verification**: RSA/ECDSA verification of firmware artifacts
- **State Scripts**: Hook into update lifecycle for custom logic
- **Standalone Mode**: Perform updates without a server connection
- **D-Bus API**: Programmatic control from other applications

## Quick Start

### Check Current Version

```bash
soc-ota-agent show-artifact
```

### Check for Updates

```bash
soc-ota-agent check-update
```

### Install from File

```bash
soc-ota-agent install /path/to/artifact.mender
```

### Commit Update

After successful boot on new version:

```bash
soc-ota-agent commit
```

### Rollback

If issues are detected:

```bash
soc-ota-agent rollback
```

## Configuration

Configuration file: `/etc/otapulse/otapulse.conf`

```json
{
  "ServerURL": "https://your-server.com",
  "TenantToken": "your-tenant-token",
  "UpdatePollIntervalSeconds": 1800,
  "InventoryPollIntervalSeconds": 28800,
  "RetryPollIntervalSeconds": 300
}
```

### Configuration Options

| Option | Description | Default |
|--------|-------------|---------|
| `ServerURL` | OTAPulse server URL | Required |
| `TenantToken` | Organization authentication token | Required |
| `UpdatePollIntervalSeconds` | How often to check for updates | 1800 |
| `InventoryPollIntervalSeconds` | How often to report inventory | 28800 |
| `RetryPollIntervalSeconds` | Retry interval after failures | 300 |
| `ServerCertificate` | Path to server CA certificate | System CA |
| `ArtifactVerifyKey` | Path to artifact verification key | None |

## Building from Source

### Requirements

- Go 1.21+
- C compiler (GCC)
- liblzma-dev
- libssl-dev
- libglib2.0-dev (for D-Bus support)

### Build

```bash
make build
```

### Cross-Compile for ARM64

```bash
GOOS=linux GOARCH=arm64 make build
```

### Install

```bash
sudo make install
```

## State Scripts

State scripts allow custom logic at various points during the update process.

### Script Location

Place scripts in `/etc/otapulse/scripts/`

### Naming Convention

`<State>_<Action>_<Priority>`

- **State**: `Idle`, `Sync`, `Download`, `ArtifactInstall`, `ArtifactReboot`, `ArtifactCommit`, `ArtifactRollback`
- **Action**: `Enter`, `Leave`, `Error`
- **Priority**: `00`-`99` (lower runs first)

### Examples

```bash
# Stop application before installation
# /etc/otapulse/scripts/ArtifactInstall_Enter_00
#!/bin/sh
systemctl stop myapp
exit 0
```

```bash
# Start application after commit
# /etc/otapulse/scripts/ArtifactCommit_Leave_00
#!/bin/sh
systemctl start myapp
exit 0
```

See `examples/state-scripts/` for more examples.

## D-Bus API

The agent exposes a D-Bus interface for its authentication manager, so other applications
can obtain the device's JWT for talking to the OTAPulse server. It is enabled by default
when the agent is built with D-Bus support (`DBus.Enabled` in the agent config).

### Service Details

- **Bus Name**: `io.otapulse.AuthenticationManager`
- **Object Path**: `/io/otapulse/AuthenticationManager`
- **Interface**: `io.otapulse.Authentication1`

| Member | Kind | Signature |
|--------|------|-----------|
| `GetJwtToken` | method | `() → (s token, s server_url)` |
| `FetchJwtToken` | method | `() → (b success)` |
| `JwtTokenStateChange` | signal | `(s token, s server_url)` |

### Example (Python)

```python
import dbus

bus = dbus.SystemBus()
proxy = bus.get_object('io.otapulse.AuthenticationManager',
                       '/io/otapulse/AuthenticationManager')
interface = dbus.Interface(proxy, 'io.otapulse.Authentication1')

token, server_url = interface.GetJwtToken()
print(f"Server: {server_url}")
```

## Systemd Service

```bash
# Enable and start
systemctl enable soc-ota-agent
systemctl start soc-ota-agent

# Check status
systemctl status soc-ota-agent

# View logs
journalctl -u soc-ota-agent -f
```

## Troubleshooting

### Agent Won't Start

```bash
# Check configuration
cat /etc/otapulse/otapulse.conf | python3 -m json.tool

# Check logs
journalctl -u soc-ota-agent --no-pager
```

### Connection Issues

```bash
# Test connectivity
curl -v https://your-server.com/api/devices/v1/authentication/auth_requests
```

### Update Failures

```bash
# Check state
cat /var/lib/otapulse/state

# View deployment log
cat /var/lib/otapulse/deployment.log
```

## Command Reference

| Command | Description |
|---------|-------------|
| `show-artifact` | Display current artifact information |
| `check-update` | Check for available updates |
| `install <file>` | Install artifact from file |
| `commit` | Commit current update |
| `rollback` | Rollback to previous version |
| `show-provides` | Show artifact provides |
| `daemon` | Run as daemon |
| `--version` | Show version |
| `--help` | Show help |

## File Locations

| Path | Description |
|------|-------------|
| `/etc/otapulse/otapulse.conf` | Main configuration |
| `/etc/otapulse/scripts/` | State scripts |
| `/var/lib/otapulse/` | Runtime state and data |
| `/usr/share/otapulse/identity/` | Device identity scripts |
| `/usr/share/otapulse/inventory/` | Inventory collection scripts |

## License

Apache License 2.0 - See [LICENSE](LICENSE) for details.
