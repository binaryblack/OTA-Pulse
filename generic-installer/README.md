# OTAPulse Generic Installer

Install OTAPulse on any Linux distribution without a package manager.

## Supported Systems

- **Architectures:** x86_64, ARM64, ARMv7, ARMv6
- **Init systems:** systemd, OpenRC, SysV init
- **C libraries:** glibc, musl
- **Distributions:** Debian, Ubuntu, Fedora, Alpine, Arch, and any other Linux

## Quick Install

```bash
# Build the agent first
cd soc-ota-agent && make build && cd ..

# Install
sudo bash generic-installer/install.sh
```

## Install Options

```bash
# Install to custom prefix (for testing or containers)
bash install.sh --prefix /opt/otapulse

# Skip service installation
sudo bash install.sh --skip-service

# Skip interactive configuration
sudo bash install.sh --skip-configure
```

## What Gets Installed

| Path | Description |
|------|-------------|
| `/usr/bin/otapulse` | Agent binary |
| `/usr/bin/mender` | Legacy symlink |
| `/usr/bin/otapulse-configure.sh` | Configuration wizard |
| `/usr/bin/otapulse-precheck.sh` | Configuration validator |
| `/etc/otapulse/` | Configuration files |
| `/usr/share/otapulse/` | Identity, inventory, and module scripts |
| `/lib/systemd/system/otapulse-client.service` | Systemd service (if applicable) |
| `/etc/init.d/otapulse` | OpenRC/SysV init script (if applicable) |
| `/var/lib/otapulse/` | Runtime state |
| `/data/ota/` | Boot state files |

## Uninstall

```bash
sudo bash generic-installer/uninstall.sh          # Keep config
sudo bash generic-installer/uninstall.sh --purge   # Remove everything
```

## Using with Docker

```bash
# Test in a clean container
docker run -it --rm -v $(pwd):/src debian:bookworm bash
cd /src/soc-ota-agent && make build && cd ..
bash generic-installer/install.sh --skip-service --skip-configure
otapulse --version
```
