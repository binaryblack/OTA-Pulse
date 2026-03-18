# OTAPulse API Reference

This document covers the OTA agent command-line interface and D-Bus API.

## Command-Line Interface

The OTA agent provides a CLI for device management and debugging.

### Basic Commands

#### Show Current Artifact

Display information about the currently installed artifact:

```bash
soc-ota-agent show-artifact
```

Output (prints the current artifact name):
```
release-1.0.0
```

#### Check for Updates

Check if updates are available:

```bash
soc-ota-agent check-update
```

#### Show Provides

Display artifact provides (dependencies satisfied):

```bash
soc-ota-agent show-provides
```

### Installation Commands

#### Install from File

Install an artifact from local file:

```bash
soc-ota-agent install /path/to/artifact.mender
```

Options:
- `--reboot-exit-code`: Return exit code 4 if manual reboot is required
- `--passphrase-file`: Passphrase file for decrypting an encrypted private key

#### Commit Update

After successful boot, commit the update:

```bash
soc-ota-agent commit
```

This marks the current partition as the active boot partition.

#### Rollback

Rollback to the previous version:

```bash
soc-ota-agent rollback
```

### Daemon Commands

#### Run as Daemon

Start the agent in daemon mode:

```bash
soc-ota-agent daemon
```

Or via systemd:
```bash
systemctl start soc-ota-agent
```

#### Bootstrap

Perform initial device setup:

```bash
soc-ota-agent bootstrap
```

### Additional Commands

#### Send Inventory

Force an inventory update:

```bash
soc-ota-agent send-inventory
```

#### Setup

Interactive configuration setup:

```bash
soc-ota-agent setup --device-type my-device --server-url https://ota.example.com
```

#### Snapshot

Create a snapshot of the running rootfs:

```bash
soc-ota-agent snapshot dump > rootfs.img
```

### Global Flags

| Flag | Description |
|------|-------------|
| `--config`, `-c` | Path to configuration file |
| `--fallback-config` | Fallback configuration file |
| `--data` | Data store directory |
| `--log-file` | Path to log file |
| `--log-level` | Log level (debug, info, warning, error) |
| `--trusted-certs` | Path to trusted certificates |
| `--forcebootstrap` | Force bootstrap |
| `--no-syslog` | Disable syslog logging |
| `--skipverify` | Skip TLS verification |
| `--passphrase-file` | Private key passphrase file |

### Utility Commands

#### Version

Display agent version:

```bash
soc-ota-agent --version
```

#### Help

Show help for any command:

```bash
soc-ota-agent --help
soc-ota-agent install --help
```

## D-Bus API

The OTA agent exposes a D-Bus interface for programmatic control.

### Service Information

| Property | Value |
|----------|-------|
| Bus Name | `io.otapulse.UpdateManager` |
| Object Path | `/io/otapulse/UpdateManager` |
| Interface | `io.otapulse.Update1` |

### Methods

#### SetUpdateControlMap

Set an update control map to pause/gate updates:

```
SetUpdateControlMap(s update_control_map) -> (i refresh_timeout)
```

Example:
```bash
busctl call io.otapulse.UpdateManager \
  /io/otapulse/UpdateManager \
  io.otapulse.Update1 SetUpdateControlMap s '{"priority": 1}'
```

### Authentication D-Bus Interface

| Property | Value |
|----------|-------|
| Bus Name | `io.otapulse.AuthenticationManager` |
| Object Path | `/io/otapulse/AuthenticationManager` |
| Interface | `io.otapulse.Authentication1` |

#### GetJwtToken

Get the current JWT token and server URL:

```
GetJwtToken() -> (s token, s server_url)
```

#### FetchJwtToken

Force a JWT token refresh:

```
FetchJwtToken() -> (b success)
```

### Signals

#### JwtTokenStateChange

Emitted when the JWT token changes (on the Authentication1 interface):

```
JwtTokenStateChange(s token, s server_url)
```

### D-Bus Example (Python)

```python
import dbus

bus = dbus.SystemBus()

# Authentication interface
auth_proxy = bus.get_object(
    'io.otapulse.AuthenticationManager',
    '/io/otapulse/AuthenticationManager'
)
auth = dbus.Interface(auth_proxy, 'io.otapulse.Authentication1')

# Get JWT token
token, server_url = auth.GetJwtToken()
print(f"Server: {server_url}")

# Update control interface
update_proxy = bus.get_object(
    'io.otapulse.UpdateManager',
    '/io/otapulse/UpdateManager'
)
update = dbus.Interface(update_proxy, 'io.otapulse.Update1')

# Set update control map
timeout = update.SetUpdateControlMap('{"priority": 1}')
print(f"Refresh timeout: {timeout}")
```

## State Machine

The OTA agent uses a state machine for update management:

```
                    ┌─────────────┐
                    │    Idle     │◄────────────────┐
                    └──────┬──────┘                 │
                           │ check-update           │
                    ┌──────▼──────┐                 │
                    │    Sync     │                 │
                    └──────┬──────┘                 │
                           │ update available       │
                    ┌──────▼──────┐                 │
                    │  Download   │                 │
                    └──────┬──────┘                 │
                           │ complete               │
                    ┌──────▼──────┐                 │
                    │   Install   │                 │
                    └──────┬──────┘                 │
                           │ complete               │
                    ┌──────▼──────┐                 │
                    │   Reboot    │                 │
                    └──────┬──────┘                 │
                           │ success                │
                    ┌──────▼──────┐                 │
                    │   Commit    │─────────────────┘
                    └─────────────┘
                           │ failure
                    ┌──────▼──────┐
                    │  Rollback   │
                    └─────────────┘
```

## Exit Codes

| Code | Description |
|------|-------------|
| 0 | Success |
| 1 | General error |
| 2 | Nothing to commit |
| 4 | Manual reboot required |

## Logging

The agent logs to:
- Stdout (when running interactively)
- Syslog (when running as daemon, disable with `--no-syslog`)
- Custom file via `--log-file` flag
- Deployment log at path configured by `UpdateLogPath` in config

Log levels: `debug`, `info`, `warning`, `error`, `panic`, `fatal`

Set via CLI flag:
```bash
soc-ota-agent --log-level debug daemon
```

Or in config file (`/etc/otapulse/otapulse.conf`):
```json
{
  "DaemonLogLevel": "debug"
}
```
