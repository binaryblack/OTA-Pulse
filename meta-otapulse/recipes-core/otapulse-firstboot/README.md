# OTAPulse Dynamic Partitioning

Creates A/B partitions dynamically on first boot, enabling minimal image sizes.

## Problem

Traditional A/B OTA implementations require pre-allocated partitions in the image:
- **Static A/B**: 9-17GB images (boot + rootfs_a + rootfs_b + data)
- **Flashing issues**: Large images fail with Rufus, RPi Imager (stuck at 99.9%)
- **Platform specific**: Each platform needs custom WKS files with correct sizes

## Solution

Dynamic partitioning creates A/B partitions at runtime:
- **Minimal image**: Only boot + rootfs_a (~4-6GB for Qt images)
- **First boot**: Creates rootfs_b and data partitions using free space
- **Platform agnostic**: Works on any storage size (16GB, 32GB, 64GB)
- **Fast flashing**: Small images flash reliably with any tool

## Image Size Comparison

| Mode | Uncompressed | Description |
|------|--------------|-------------|
| Base (no A/B) | 3.5 GB | Single rootfs |
| Static A/B | 9-17 GB | Pre-allocated partitions |
| **Dynamic A/B** | **4-6 GB** | Minimal, A/B created at runtime |

## Usage

### 1. Add to local.conf

```bitbake
# Enable dynamic partitioning (default)
OTAPULSE_PARTITION_MODE = "dynamic"

# Use dynamic WKS template
WKS_FILE = "otapulse-dynamic-ab-imx.wks"
```

### 2. Add to image recipe

```bitbake
inherit otapulse  # Automatically includes otapulse-firstboot in dynamic mode
```

Or explicitly:
```bitbake
IMAGE_INSTALL:append = " otapulse-firstboot"
```

## How It Works

### Initial Image Layout
```
+----------+--------+------------+-------------+
| imx-boot |  boot  |  rootfs_a  | free space  |
+----------+--------+------------+-------------+
   32KB      64MB     ~4-5GB     (remaining)
```

### After First Boot
```
+----------+--------+------------+------------+--------+
| imx-boot |  boot  |  rootfs_a  |  rootfs_b  |  data  |
+----------+--------+------------+------------+--------+
   32KB      64MB     ~4-5GB      ~4-5GB      ~2GB+
```

## First Boot Process

1. `otapulse-partition-setup.service` runs before OTA agent
2. Detects boot device and rootfs_a size
3. Creates rootfs_b partition (same size as rootfs_a)
4. Creates data partition (remaining space, minimum 1GB)
5. Formats partitions with ext4 and sets GPT labels
6. Creates marker file to skip on subsequent boots

## Partition Labels

The script sets GPT partition labels for OTA agent:
- `/dev/disk/by-partlabel/boot`
- `/dev/disk/by-partlabel/rootfs_a`
- `/dev/disk/by-partlabel/rootfs_b`
- `/dev/disk/by-partlabel/data`

## Requirements

- GPT partition table (set in WKS)
- `gptfdisk` package (sgdisk command)
- `e2fsprogs` package (mkfs.ext4)
- Sufficient free space on storage device

## Troubleshooting

### Check partition setup status
```bash
systemctl status otapulse-partition-setup
journalctl -u otapulse-partition-setup
```

### Verify partitions were created
```bash
ls -la /dev/disk/by-partlabel/
lsblk -o NAME,SIZE,LABEL,PARTLABEL,FSTYPE
```

### Re-run partition setup (if needed)
```bash
rm /var/lib/otapulse/.partitions-created
systemctl start otapulse-partition-setup
```

## Static Mode (Alternative)

If you prefer pre-allocated partitions:

```bitbake
OTAPULSE_PARTITION_MODE = "static"
WKS_FILE = "your-static-ab.wks"  # With fixed partition sizes
```

Static mode does not include `otapulse-firstboot`.

## Bootloader Compatibility Mode

With `OTAPULSE_BOOT_MODE = "bootloader"` (see `classes/otapulse-full.bbclass`) the
board support owns the partition layout, the `/data` mount and the rollback net, so
this package installs only the parts that do not depend on the native layout:

| Installed | Not installed |
|-----------|---------------|
| `otapulse-auto-provision` (credentials, tunnel credential) | `otapulse-partition-setup` |
| `otapulse-machine-id` (persistent machine-id and NIC MAC on `/data`) | `otapulse-boot-health` |
| `otapulse-data-seed` (`/data/otapulse`, `device_type`; fails loudly on a device-type mismatch) | `otapulse-boot-deadline` (replaced by `otapulse-bl-boot-deadline`, below) |
| `otapulse-bl-guard` + `otapulse-bl-boot-deadline.timer/.service`, `otapulse-bl-seed-failure.service`, `otapulse-bl-clock-wait.service` (below) | the native udev partition-label aliases |

`otapulse-growfs-data` (a separate recipe, pulled in by `soc-ota-agent`) grows the
`/data` partition and file system to the storage medium. The native files are removed
by `do_install:append:otapulse-bootloader`; the native task bodies are untouched.

### Userspace half of the bootloader rollback net (`otapulse-bl-guard`)

The bootloader counts boots while `upgrade_available=1` and rolls back at its limit, but only a
device that RESETS gets counted. These bootloader-mode-only units close the userspace gaps. They
only ever READ the bootloader environment (`fw_printenv` on U-Boot, `grub-editenv` on GRUB, with
the same copy-selection rule as the test framework) and never write it.

| Unit | What it does |
|------|--------------|
| `otapulse-bl-boot-deadline.timer` (20 min after boot) | Reboots when the environment still says `upgrade_available=1` on the TRIAL boot of an update: a wedged userspace that never committed. It never acts on a device that has only been told to reboot into an update (`upgrade_available=1` with `bootcount=0`, written by the agent before its own reboot), and only on a boot that `otapulse-bl-clock-wait` marked as a trial boot (boot id and active slot recorded in `/run`). |
| `otapulse-bl-seed-failure.service` (`OnFailure=` of `otapulse-data-seed`) | The agent cannot start after a failed seed. On an uncommitted update: reboot at once. On a committed slot: loud log, no reboot. |
| `otapulse-bl-clock-wait.service` (`Before=soc-ota-agent.service`) | Bounded (60 s) wait for an NTP-synced clock while an update is pending, so the agent's pre-commit TLS report does not hit a not-yet-valid certificate on boards without an RTC. Never blocks the agent. |

An unreadable environment never reboots (it is not evidence of a pending update); it logs at error
level and the deadline unit fails visibly. A budget of 5 consecutive guard reboots, kept in
`/data/otapulse/bl-guard-reboots` and cleared on the first `upgrade_available=0`, stops a device
whose rollback is impossible from rebooting forever. The decision table is in the header of
`files/otapulse-bl-guard`.

Known gaps (also listed in that header):

- A `/data` mount failure is NOT covered by the deadline. On U-Boot the environment config points
  into `/data/u-boot`, so the environment is unreadable (fail-safe: no reboot). `/data` is a plain
  fstab mount without `nofail`, so the system most likely drops to `emergency.target`, where timers
  do not run. Only the kernel-panic path and a hardware watchdog help there.
- With rollback impossible and `/data` not writable there is no persistent budget: the deadline
  then reboots once per boot, about every 20 minutes, indefinitely.
- `pause_before_committing`: an update control map that pauses the agent before the commit for longer
  than the deadline is turned into a reboot (and eventually a rollback). No server emits one today;
  raise `OnBootSec` or disable the timer before introducing one.
- A trial boot whose environment is unreadable at boot gets no marker, so the deadline does not act on it.

## Cross-Platform Support

### Included WKS Templates

| Template | Platform | Bootloader |
|----------|----------|------------|
| `otapulse-dynamic-ab-imx.wks` | NXP i.MX8 | imx-boot @ 32KB |
| `otapulse-dynamic-ab-rpi.wks` | Raspberry Pi | Boot partition |
| `otapulse-dynamic-ab-generic.wks` | Template | Customize |

### Adding Support for New Platforms

1. Copy `otapulse-dynamic-ab-generic.wks` to your layer
2. Add bootloader section for your platform:

```bash
# i.MX8:
part u-boot --source rawcopy --sourceparams="file=imx-boot" --no-table --align 32

# STM32MP:
part fsbl --source rawcopy --sourceparams="file=tf-a.stm32" --no-table --align 17

# Rockchip:
part idbloader --source rawcopy --sourceparams="file=idbloader.img" --no-table --align 64
```

3. Set in local.conf:
```bitbake
WKS_FILE = "your-platform-ab.wks"
```

### Requirements for All Platforms

- GPT partition table (for partition labels)
- `gptfdisk` package (sgdisk command)
- `e2fsprogs` package (mkfs.ext4)
- Sufficient free space after rootfs_a
