# OTAPulse Debian/Ubuntu Package

Debian packaging for the OTAPulse OTA update agent.

## Supported Architectures

- **amd64** — x86_64 systems
- **arm64** — 64-bit ARM (Raspberry Pi 4/5, i.MX8, Rockchip RK3588, etc.)
- **armhf** — 32-bit ARM (Raspberry Pi 3, BeagleBone, STM32MP, etc.)

## Building the .deb Package

### Prerequisites

```bash
sudo apt install debhelper golang-go libssl-dev liblmdb-dev pkg-config
```

### Build

```bash
cd debian-otapulse
dpkg-buildpackage -us -uc -b
```

The `.deb` file will be created in the parent directory.

### Cross-compilation

For ARM64:
```bash
dpkg-buildpackage -us -uc -b -aarm64
```

For ARMhf:
```bash
dpkg-buildpackage -us -uc -b -aarmhf
```

## Installation

```bash
sudo dpkg -i ../otapulse_1.0.0-1_<arch>.deb
sudo apt-get install -f  # Install dependencies
```

## Post-Install Configuration

The package does NOT embed signing keys or server configuration. After installation:

1. **Generate configuration:**
   ```bash
   otapulse-configure.sh --output /etc/otapulse/otapulse.conf
   ```

2. **Or create manually:**
   ```bash
   cat > /etc/otapulse/otapulse.conf <<EOF
   {
       "ServerURL": "https://your-server.com",
       "TenantToken": "your-tenant-token",
       "RootfsPartA": "/dev/mmcblk0p2",
       "RootfsPartB": "/dev/mmcblk0p3",
       "UpdatePollIntervalSeconds": 1800,
       "InventoryPollIntervalSeconds": 28800,
       "UseFileBasedBootEnv": true
   }
   EOF
   ```

3. **Place your verification key:**
   ```bash
   cp verify-key.pem /etc/otapulse/
   ```

4. **Start the service:**
   ```bash
   sudo systemctl enable --now otapulse-client
   ```

## Verify Installation

```bash
otapulse --version
systemctl status otapulse-client
```

## File Locations

| Path | Description |
|------|-------------|
| `/usr/bin/otapulse` | Agent binary |
| `/usr/bin/mender` | Legacy symlink |
| `/etc/otapulse/otapulse.conf` | Configuration file |
| `/etc/otapulse/device_type` | Device type |
| `/usr/share/otapulse/identity/` | Identity scripts |
| `/usr/share/otapulse/inventory/` | Inventory scripts |
| `/usr/share/otapulse/modules/v3/` | Update modules |
| `/lib/systemd/system/otapulse-client.service` | Systemd service |
| `/var/lib/otapulse/` | Runtime state |
| `/data/ota/` | Boot state files |

## Uninstalling

```bash
sudo apt remove otapulse      # Keep config
sudo apt purge otapulse        # Remove config
```
