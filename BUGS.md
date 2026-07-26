# OTA-Pulse Known Issues

> This file tracks issues significant enough to need a written root-cause /
> workaround record. Day-to-day fixes (including many also tagged `BUG-NNN`
> in commit messages, e.g. BUG-074, BUG-095, BUG-106, BUG-113, BUG-151,
> BUG-153, BUG-155, BUG-157, BUG-158) are tracked and closed via git history —
> see `git log --oneline --all | grep 'BUG-'` for the full list. Note: bug
> numbers here are **not** guaranteed unique across the two systems — e.g.
> the BUG-001 below (rollback boot-state desync) is a different issue from
> the commit tagged `BUG-001` (SSH host key handling).

## Open Issues

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

### Status
**Still open.** `Rollback()` in `soc-ota-agent/installer/dual_rootfs_device.go`
only corrects `mender_boot_part` when the *agent itself* drives the rollback;
the scenario this bug describes — a bootloader-level auto-fallback with no
agent involvement — isn't covered by that code path. The GPT-partlabel
partition resolution fix (commit `877a401`) improves partition
*identification* generally but doesn't address this specific desync scenario.

### Related
- File-based boot environment in `soc-ota-agent/conf/paths.go`
- Rockchip PARTUUID-based boot switching

## Resolved Issues (selected)

Full history: `git log --oneline --all | grep 'BUG-'`. Highlights relevant to
partition resolution and Radxa CM5 slot switching (also referenced from
[docs/integration/rockchip-integration.md](docs/integration/rockchip-integration.md)):

- **BUG-106** — deterministic `WriteEnv` order: FAT slot sync now happens
  before `upgrade_available` is set, closing a window where a crash mid-write
  could leave the two out of sync (commit `a460570`).
- **BUG-153** — signing-keys recipe re-synced to the rotated engine key after
  a key-rotation event (commit `960a624`).
- **BUG-155** — primary-NIC MAC address was churning across reboots on Radxa
  CM5 (device identity instability); fixed by persisting the MAC (commit `d10c467`).
- **BUG-157** — `switch-boot-slot.sh` was being called with synthetic
  partition paths instead of the real resolved ones on Radxa CM5; fixed to
  pass real paths, with a follow-up closing remaining false-success paths
  (commits `1dcbd97`, `8b5817f`).
- **BUG-158** — added a systemd-boot EFI ABA slot-switch method to
  `switch-boot-slot.sh`, now the primary mechanism on Radxa CM5 (commit `6c79cae`).
