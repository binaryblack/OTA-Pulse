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

Output:
```
artifact_name=release-1.0.0
device_type=radxa-cm5
artifact_group=production
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
- `-f, --force`: Force installation even if already installed

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

#### GetVersion

Get the current artifact version:

```
GetVersion() -> (s artifact_name)
```

Example:
```bash
busctl call io.otapulse.UpdateManager \
  /io/otapulse/UpdateManager \
  io.otapulse.Update1 GetVersion
```

#### CheckUpdate

Check for available updates:

```
CheckUpdate() -> (b update_available, s artifact_name)
```

#### FetchUpdate

Download an available update:

```
FetchUpdate() -> (b success)
```

#### InstallUpdate

Install the downloaded update:

```
InstallUpdate() -> (b success)
```

#### Commit

Commit the current update:

```
Commit() -> (b success)
```

#### Rollback

Rollback to previous version:

```
Rollback() -> (b success)
```

### Signals

#### UpdateStateChanged

Emitted when update state changes:

```
UpdateStateChanged(s state, s artifact_name)
```

States:
- `idle` - No update in progress
- `downloading` - Downloading artifact
- `downloaded` - Download complete
- `installing` - Installing update
- `installed` - Installation complete, pending reboot
- `committed` - Update committed
- `failed` - Update failed

### D-Bus Example (Python)

```python
import dbus

bus = dbus.SystemBus()
proxy = bus.get_object(
    'io.otapulse.UpdateManager',
    '/io/otapulse/UpdateManager'
)
interface = dbus.Interface(proxy, 'io.otapulse.Update1')

# Get current version
version = interface.GetVersion()
print(f"Current artifact: {version}")

# Check for updates
available, artifact = interface.CheckUpdate()
if available:
    print(f"Update available: {artifact}")
```

### D-Bus Example (C)

```c
#include <gio/gio.h>

GDBusProxy *proxy = g_dbus_proxy_new_for_bus_sync(
    G_BUS_TYPE_SYSTEM,
    G_DBUS_PROXY_FLAGS_NONE,
    NULL,
    "io.otapulse.UpdateManager",
    "/io/otapulse/UpdateManager",
    "io.otapulse.Update1",
    NULL, NULL
);

GVariant *result = g_dbus_proxy_call_sync(
    proxy, "GetVersion", NULL,
    G_DBUS_CALL_FLAGS_NONE, -1, NULL, NULL
);

const gchar *version;
g_variant_get(result, "(s)", &version);
g_print("Current artifact: %s\n", version);
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
| 2 | No update available |
| 3 | Update already installed |
| 4 | Signature verification failed |
| 5 | Installation failed |
| 6 | Commit failed |
| 7 | Rollback failed |

## Logging

The agent logs to:
- Stdout (when running interactively)
- Syslog (when running as daemon)
- `/var/log/otapulse/update.log`

Log levels: `debug`, `info`, `warn`, `error`

Set via environment:
```bash
OTAPULSE_LOG_LEVEL=debug soc-ota-agent daemon
```
