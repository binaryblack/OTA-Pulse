# OTAPulse Troubleshooting Guide

Solutions for common issues when integrating and deploying OTAPulse.

## Diagnostic Commands

### Check Agent Status

```bash
# Service status
systemctl status soc-ota-agent

# View logs
journalctl -u soc-ota-agent -f

# Current artifact info
soc-ota-agent show-artifact

# Test server connectivity
curl -v https://your-server.com/api/devices/v1/authentication/auth_requests
```

### Check Configuration

```bash
# View configuration
cat /etc/otapulse/otapulse.conf

# Check partition layout
lsblk

# View boot environment
fw_printenv
```

## Common Issues

### Agent Won't Start

**Symptom:** Service fails to start, exits immediately

**Check:**
1. Configuration file syntax:
   ```bash
   python3 -m json.tool /etc/otapulse/otapulse.conf
   ```

2. Required fields present:
   ```bash
   grep -E "ServerURL|TenantToken" /etc/otapulse/otapulse.conf
   ```

3. File permissions:
   ```bash
   ls -la /etc/otapulse/
   ```

**Solution:** Fix configuration errors, ensure ServerURL and TenantToken are set.

### Connection Failures

**Symptom:** Agent can't reach server, authentication fails

**Check:**
1. Network connectivity:
   ```bash
   ping your-server.com
   ```

2. DNS resolution:
   ```bash
   nslookup your-server.com
   ```

3. TLS connectivity:
   ```bash
   openssl s_client -connect your-server.com:443
   ```

4. Proxy settings (if applicable):
   ```bash
   echo $HTTPS_PROXY
   ```

**Solution:**
- Verify network configuration
- Check firewall rules (port 443 outbound)
- Configure proxy if behind corporate firewall
- Verify server certificate is trusted

### Authentication Errors

**Symptom:** Device fails to authenticate, 401/403 errors

**Check:**
1. Tenant token validity:
   ```bash
   # Decode JWT (if applicable)
   echo $TENANT_TOKEN | cut -d. -f2 | base64 -d
   ```

2. Device identity:
   ```bash
   /usr/share/otapulse/identity/otapulse-device-identity
   ```

**Solution:**
- Verify tenant token is correct and not expired
- Check device is authorized on server
- Ensure device identity is unique

### Update Download Fails

**Symptom:** Update starts but fails during download

**Check:**
1. Disk space:
   ```bash
   df -h
   ```

2. Download directory permissions:
   ```bash
   ls -la /var/lib/otapulse/
   ```

3. Network stability:
   ```bash
   ping -c 100 your-server.com | grep loss
   ```

**Solution:**
- Free disk space (need 2x artifact size)
- Fix permissions on data directory
- Check network quality, consider longer timeouts

### Installation Failures

**Symptom:** Download completes but installation fails

**Check:**
1. Partition availability:
   ```bash
   lsblk
   mount | grep -E "rootfs|mmcblk"
   ```

2. State script errors:
   ```bash
   ls -la /etc/otapulse/scripts/
   journalctl -u soc-ota-agent | grep -i "state script"
   ```

3. Artifact compatibility:
   ```bash
   soc-ota-agent show-artifact
   # Compare device_type with artifact
   ```

**Solution:**
- Verify A/B partitions are correctly configured
- Check state scripts exit with 0
- Ensure artifact device_type matches

### Signature Verification Fails

**Symptom:** "signature verification failed" error

**Check:**
1. Public key present:
   ```bash
   ls -la /etc/otapulse/artifact-verify-key.pem
   ```

2. Key format:
   ```bash
   openssl rsa -in /etc/otapulse/artifact-verify-key.pem -pubin -text
   ```

3. Artifact is signed:
   ```bash
   mender-artifact read artifact.mender | grep -i signature
   ```

**Solution:**
- Ensure artifact is signed with matching private key
- Verify public key deployed to device matches
- Check key algorithm compatibility (RSA vs ECDSA)

### Boot Loop / Rollback

**Symptom:** Device keeps reverting to old version

**Check:**
1. Boot count:
   ```bash
   fw_printenv bootcount
   ```

2. Commit status:
   ```bash
   fw_printenv upgrade_available
   ```

3. Application health:
   ```bash
   systemctl status myapp
   ```

**Solution:**
- Verify application starts successfully after update
- Add health check to ArtifactCommit_Enter script
- Check boot count limit in U-Boot config

### GPT Partlabel / systemd-boot Slot Issues

**Symptom:** Wrong partition boots after an update, or `switch-boot-slot.sh`
reports success but the slot doesn't actually change (most common on Radxa
CM5 / RK3588S with systemd-boot EFI ABA — see
[integration/rockchip-integration.md](integration/rockchip-integration.md#radxa-cm5-rk3588s-slot-switching)).

**Check:**
1. GPT partlabels resolve as expected:
   ```bash
   lsblk -o NAME,PARTLABEL,PARTUUID
   ls -l /dev/disk/by-partlabel/
   ```

2. `switch-boot-slot.sh` received real partition paths (not synthetic/placeholder ones):
   ```bash
   sudo /usr/sbin/switch-boot-slot.sh status
   ```

3. For systemd-boot EFI ABA specifically, check the active boot entry:
   ```bash
   bootctl status
   ```

**Solution:**
- If `/dev/disk/by-partlabel/rootfs_a` or `rootfs_b` is missing, the WKS/kickstart
  file isn't setting GPT partition labels — check your platform's `.wks` file.
- Confirm you're running a build that includes the partlabel-based partition
  resolution fix (commit `877a401`) rather than one assuming a static
  `mmcblk0pN` mapping.
- On Radxa CM5, confirm `CONFIG_BOOTCOMMAND` is `bootflow scan` (bootstd
  mode) — `run distro_bootcmd` will not use the systemd-boot slot-switch path.

### Stuck in Update State

**Symptom:** Device stuck, not responding to new updates

**Check:**
1. Current state:
   ```bash
   cat /var/lib/otapulse/state
   ```

2. Pending update:
   ```bash
   ls -la /var/lib/otapulse/
   ```

**Solution:**
```bash
# Clear stuck state (use with caution)
systemctl stop soc-ota-agent
rm /var/lib/otapulse/state
rm /var/lib/otapulse/*.mender
systemctl start soc-ota-agent
```

## Yocto Build Issues

### Layer Compatibility

**Symptom:** BitBake parse errors

**Check:**
```bash
bitbake-layers show-layers
```

**Solution:**
- Verify Yocto version compatibility (Kirkstone+)
- Check layer priorities don't conflict
- Ensure all dependencies are present

### Recipe Failures

**Symptom:** Package fails to build

**Check:**
```bash
bitbake soc-ota-agent -c devshell
# Then debug in the devshell
```

**Solution:**
- Check Go toolchain is available
- Verify network access for go module downloads
- Check disk space in build directory

### Image Too Large

**Symptom:** Image doesn't fit in partition

**Solution:**
- Reduce IMAGE_INSTALL packages
- Exclude unnecessary locales
- Strip debug symbols:
  ```bash
  INHIBIT_PACKAGE_STRIP = "0"
  ```

## Log Analysis

### Enable Debug Logging

```bash
# Temporary (via CLI flag)
soc-ota-agent --log-level debug daemon

# Permanent (in /etc/otapulse/otapulse.conf)
{
  "DaemonLogLevel": "debug"
}
```

### Key Log Messages

| Message | Meaning |
|---------|---------|
| "Attempting to authenticate" | Device connecting to server |
| "Received deployment" | Update available |
| "Downloading artifact" | Update downloading |
| "Installing update" | Writing to inactive partition |
| "Update complete, rebooting" | Installation success |
| "Update committed" | Update confirmed successful |
| "Automatic rollback" | Update failed, reverting |

## Getting Help

When reporting issues, include:

1. Agent version: `soc-ota-agent --version`
2. Configuration (sanitized): `cat /etc/otapulse/otapulse.conf`
3. Logs: `journalctl -u soc-ota-agent --no-pager`
4. Device info: `uname -a`, `cat /etc/os-release`
5. Partition layout: `lsblk`
