# OTA-Pulse Known Issues

## BUG-001: Boot State File Mismatch After Rollback

### Description
The `/data/ota/mender_boot_part` file can get out of sync with the actual boot partition after an OTA rollback occurs.

### Symptoms
- `mender_boot_part` shows partition X but device is actually running on partition Y
- Agent writes OTA to wrong partition (currently active instead of inactive)
- Subsequent OTA updates may overwrite the running system

### Root Cause
When a rollback occurs (device fails to boot from new partition and reverts to old one), the boot state files are not updated to reflect the actual boot partition. The file-based boot environment relies on these files being accurate.

### Reproduction Steps
1. Deploy OTA update to device
2. Device writes to inactive partition and reboots
3. If boot from new partition fails, device rolls back to previous partition
4. After rollback, `mender_boot_part` still shows the new partition number
5. Device is actually running on old partition

### Impact
- **Severity**: High
- OTA may overwrite currently running partition
- Can cause boot failures or data loss

### Workaround
After a rollback, manually fix the boot state:
```bash
# Check actual partition
mount | grep ' / '  # Shows /dev/mmcblk0pX

# Fix boot state to match (replace X with actual partition number)
echo 'X' > /data/ota/mender_boot_part
echo '0' > /data/ota/upgrade_available
```

### Proposed Fix
Add a boot-time check in the agent or a systemd service that:
1. Detects actual boot partition from mount point
2. Compares with `mender_boot_part` file
3. Updates file if mismatch detected

### Files Involved
- `/data/ota/mender_boot_part` - Boot partition preference
- `/data/ota/upgrade_available` - Pending upgrade flag
- `/usr/sbin/switch-boot-slot.sh` - Boot slot switching script
- Agent boot environment detection code

### Related
- File-based boot environment in `soc-ota-agent/conf/paths.go`
- Rockchip PARTUUID-based boot switching
