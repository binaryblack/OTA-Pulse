# OTAPulse Quickstart Guide

Get from zero to your first OTA update in under 30 minutes — no hardware required.

This guide uses QEMU to emulate an ARM64 device, deploy an OTA artifact, and verify the update lands successfully.

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| QEMU | 6.0+ | `sudo apt install qemu-system-arm` |
| Go | 1.20+ | [golang.org/dl](https://golang.org/dl/) |
| mender-artifact | 3.10+ | [docs.mender.io/downloads](https://docs.mender.io/downloads) |
| openssl | 1.1+ | `sudo apt install openssl` |
| Docker (optional) | 20+ | For reproducible builds |

**Quick dependency check:**
```bash
qemu-system-aarch64 --version
go version
mender-artifact --version
openssl version
```

## Step 1: Clone and Build the Agent

```bash
git clone https://github.com/binaryblack/OTA-Pulse.git
cd OTA-Pulse/soc-ota-agent

# Build for the host (for testing)
make build

# Verify
./otapulse --version
```

## Step 2: Generate Configuration

Use the interactive configuration wizard:

```bash
cd ../scripts

# Interactive mode — follow the prompts
bash otapulse-configure.sh \
  --output ../test-config/otapulse.conf \
  --generate-keys

# Or non-interactive for CI:
OTAPULSE_SERVER_URL="https://your-server.example.com" \
OTAPULSE_TENANT_TOKEN="your-token" \
OTAPULSE_DEVICE_TYPE="qemu-arm64" \
  bash otapulse-configure.sh \
    --non-interactive \
    --output ../test-config/otapulse.conf \
    --generate-keys
```

## Step 3: Validate Configuration

```bash
bash otapulse-precheck.sh --config ../test-config/otapulse.conf
```

You should see all checks passing. Fix any failures before proceeding.

## Step 4: Run with QEMU (Automated)

The quickstart QEMU script handles everything:

```bash
bash quickstart-qemu.sh
```

This will:
1. Build the OTA agent for ARM64
2. Create a minimal rootfs with the agent installed
3. Boot it in QEMU
4. Create a test OTA artifact
5. Install the artifact and verify the update

## Step 5: Manual QEMU Walkthrough

If you prefer to understand each step:

### 5a. Cross-compile for ARM64

```bash
cd ../soc-ota-agent
CC=aarch64-linux-gnu-gcc GOOS=linux GOARCH=arm64 make build
```

### 5b. Create a Test Artifact

```bash
# Create a simple file to serve as our "firmware update"
echo "firmware-v2.0" > /tmp/test-rootfs.txt

# Create a .mender artifact
mender-artifact write module-image \
  -T single-file \
  -t qemu-arm64 \
  -n "test-release-2.0" \
  -f /tmp/test-rootfs.txt \
  -o /tmp/test-release-2.0.mender

# Verify the artifact
mender-artifact validate /tmp/test-release-2.0.mender
mender-artifact read /tmp/test-release-2.0.mender
```

### 5c. Install the Artifact Locally

```bash
# Install the update (standalone mode, no server needed)
sudo ./otapulse install /tmp/test-release-2.0.mender

# Check the result
sudo ./otapulse show-artifact
```

### 5d. Commit or Rollback

```bash
# If the update looks good:
sudo ./otapulse commit

# If something went wrong:
sudo ./otapulse rollback
```

## Step 6: Connect to a Server

Once you've verified the agent works locally, connect it to your OTAPulse server:

1. Edit `/etc/otapulse/otapulse.conf` with your server URL and tenant token
2. Start the daemon: `sudo systemctl start otapulse-client`
3. Check status: `sudo systemctl status otapulse-client`
4. The device will appear in your server dashboard within the update poll interval

## What's Next?

| Goal | Guide |
|------|-------|
| Integrate with Yocto | [Integration Guide](INTEGRATION.md) |
| Integrate with Buildroot | [Buildroot Guide](../buildroot-otapulse/docs/BUILDROOT_INTEGRATION.md) |
| Add to existing project | [Integrating Existing Project](INTEGRATING_EXISTING_PROJECT.md) |
| Set up signing | [Security Guide](SECURITY.md) |
| Key rotation | [Key Rotation Guide](KEY_ROTATION.md) |
| Write state scripts | [State Scripts Guide](STATE_SCRIPTS_GUIDE.md) |
| All configuration options | [Configuration Reference](CONFIGURATION.md) |
| Debian/Ubuntu install | [Debian Package](../debian-otapulse/README.md) |
| OpenWrt install | [OpenWrt Package](../openwrt-otapulse/README.md) |
| Generic Linux install | [Generic Installer](../generic-installer/README.md) |

## Troubleshooting

### Agent won't start
```bash
journalctl -u otapulse-client -n 50
cat /etc/otapulse/otapulse.conf | python3 -m json.tool  # Validate JSON
```

### "Server URL is placeholder" error
Your config still has the default URL. Run `otapulse-configure.sh` to set a real server URL.

### Cross-compilation fails
Ensure you have the cross-compiler installed:
```bash
sudo apt install gcc-aarch64-linux-gnu   # ARM64
sudo apt install gcc-arm-linux-gnueabihf  # ARM32
```

### QEMU won't boot
Check that `qemu-system-aarch64` is installed and your kernel image path is correct.
