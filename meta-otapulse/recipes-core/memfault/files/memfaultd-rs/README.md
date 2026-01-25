# memfaultd - SoC Monitoring Daemon

A comprehensive monitoring daemon for embedded Linux devices written in Rust.

## Features

- **System Metrics Collection**: CPU, memory, disk, network, and temperature monitoring
- **Coredump Handling**: Automatic capture, compression, and upload of application crashes
- **OTA Updates**: Secure firmware update checking, downloading, and installation
- **Watchdog Integration**: Hardware and software watchdog support
- **Reboot Tracking**: Automatic detection and reporting of reboot reasons
- **IPC Interface**: Unix socket for custom metrics from applications

## Building

### Prerequisites

- Rust 1.70 or later
- OpenSSL development libraries

### Native Build

```bash
cargo build --release
```

### Cross-compilation for embedded targets

```bash
# For ARM64
cargo build --release --target aarch64-unknown-linux-gnu

# For ARMv7
cargo build --release --target armv7-unknown-linux-gnueabihf
```

### Yocto Build

The project includes a BitBake recipe for Yocto/OpenEmbedded builds. The recipe inherits from the `cargo` class and handles cross-compilation automatically.

## Binaries

- `memfaultd` - Main monitoring daemon
- `memfault-watchdog` - System health monitor
- `memfault-core-handler` - Kernel coredump handler

## Configuration

Configuration is stored in JSON format at `/etc/memfault/config.json`.

### Minimal Configuration

```json
{
    "server": {
        "base_url": "https://api.your-server.com",
        "api_version": "v1"
    },
    "auth": {
        "api_key": "your-api-key"
    }
}
```

### Full Configuration Options

| Section | Option | Default | Description |
|---------|--------|---------|-------------|
| server | base_url | - | Server API endpoint |
| server | api_version | v1 | API version |
| device | device_id | auto | Device identifier (auto-generated from machine-id) |
| metrics | enabled | true | Enable metrics collection |
| metrics | upload_interval_seconds | 300 | Upload interval |
| metrics | collection_interval_seconds | 60 | Collection interval |
| crash | enabled | true | Enable crash capture |
| crash | coredump_max_size_bytes | 50MB | Max coredump size |
| ota | enabled | true | Enable OTA updates |
| ota | check_interval_seconds | 3600 | OTA check interval |
| watchdog | enabled | true | Enable watchdog |
| watchdog | timeout_seconds | 30 | Watchdog timeout |

## API Endpoints

The daemon communicates with the server using the following endpoints:

- `POST /api/v1/devices/{device_id}/metrics` - Upload metrics
- `POST /api/v1/devices/{device_id}/crash` - Upload coredumps
- `POST /api/v1/devices/{device_id}/ota/check` - Check for updates
- `POST /api/v1/devices/{device_id}/reboot` - Report reboot
- `POST /api/v1/devices/{device_id}/heartbeat` - Device heartbeat

## IPC Interface

Applications can send custom metrics via Unix socket at `/run/memfault/metrics.sock`.

### Python Example

```python
import socket
import json

sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
sock.connect('/run/memfault/metrics.sock')

message = {
    "type": "metric",
    "name": "my_app.requests",
    "value": 42.0,
    "unit": "count"
}

sock.send(json.dumps(message).encode() + b'\n')
response = sock.recv(1024)
sock.close()
```

### Rust Example

```rust
use memfaultd::{send_metric, send_event};

#[tokio::main]
async fn main() {
    // Send a metric
    send_metric("my_app.latency", 123.5, Some("ms")).await.ok();

    // Send an event
    send_event("user_login", serde_json::json!({
        "user_id": "123"
    })).await.ok();
}
```

## Systemd Integration

The daemon supports systemd service notifications:

- `READY=1` - Service is ready
- `WATCHDOG=1` - Watchdog keep-alive
- `STOPPING=1` - Service is stopping
- `STATUS=...` - Status updates

## Directory Structure

```
/etc/memfault/
├── config.json          # Configuration file

/var/lib/memfault/
├── metrics/             # Buffered metrics
├── coredumps/           # Stored coredumps
├── ota/                 # OTA downloads
├── last_reboot_reason   # Reboot reason tracking
└── reboot_history.json  # Reboot history

/run/memfault/
└── metrics.sock         # IPC socket

/var/log/memfault/
├── memfaultd.log        # Daemon log (if not using journald)
├── watchdog.log         # Watchdog log
└── coredump.log         # Coredump handler log
```

## Coredump Setup

The daemon configures the kernel to route coredumps through the handler:

```
kernel.core_pattern=|/usr/bin/memfault-core-handler %p %u %g %s %t %c %h %e
```

## Security

- TLS 1.3 for all server communication
- Certificate verification enabled by default
- Systemd security hardening (NoNewPrivileges, ProtectSystem, etc.)
- Limited capabilities (CAP_NET_BIND_SERVICE, CAP_SYS_PTRACE)

## License

Apache-2.0
