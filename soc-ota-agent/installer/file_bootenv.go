// Copyright 2026 SoC Monitoring
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/binaryblack/OTA-Pulse/system"
)

const (
	// Default locations for boot state files
	DefaultSlotFile           = "/data/ota/current_slot"
	DefaultBootCountFile      = "/data/ota/boot_count"
	DefaultUpgradeAvailFile   = "/data/ota/upgrade_available"
	DefaultMenderBootPartFile = "/data/ota/mender_boot_part"

	// directBootPrevCmdline is the old-slot cmdline.txt backup written on the FAT
	// boot partition by updateBootCmdline during a direct-boot (no U-Boot env) A/B
	// slot switch. It is the rollback target consumed by the pre-agent
	// otapulse-boot-health.service (GAP-OTA-006): after DefaultMaxBootRetries
	// failed boots the script restores it over cmdline.txt and reboots onto the
	// known-good slot. On a good boot it is cleared by ClearDirectBootBackup (see
	// CommitUpdate). The per-boot retry counter itself lives in
	// DefaultBootCountFile (/data/ota/boot_count) — the SAME file the shell script
	// increments and HandleBootCountFallback reads — so there is no separate FAT
	// counter file.
	directBootPrevCmdline = "cmdline_prev.txt"
)

// FileBasedBootEnv implements BootEnvReadWriter using files instead of U-Boot environment.
// This provides a generic, hardware-agnostic approach that works with any bootloader
// that can read boot partition from kernel cmdline or extlinux.conf.
//
// The file-based approach stores boot state in /data/ota/ which persists across updates:
//   - current_slot: "a" or "b" - which slot to boot
//   - mender_boot_part: partition number (e.g., "3" for /dev/mmcblk0p3)
//   - upgrade_available: "1" or "0"
//   - boot_count: increment on each boot attempt for rollback
type FileBasedBootEnv struct {
	system.Commander
	slotFile           string
	bootCountFile      string
	upgradeAvailFile   string
	menderBootPartFile string
	rootfsPartA        string
	rootfsPartB        string

	// detectFn returns the partition number of the currently-booted rootfs.
	// It defaults to detectCurrentPartitionNumber (reads /proc/self/mounts) and
	// exists as a field only so unit tests can inject a fake for
	// ReconcileToBootedSlot.
	detectFn func() (string, error)
}

// NewFileBasedBootEnv creates a new file-based boot environment handler.
// This is an alternative to U-Boot environment that works with any bootloader.
//
// rootfsPartA/rootfsPartB are resolved via maybeResolveLink before being
// stored, mirroring NewDualRootfsDevice (dual_rootfs_device.go). Without
// this, a board configured with unresolved by-partlabel/by-partuuid paths
// (e.g. /dev/disk/by-partlabel/rootfs_a) stores a value with no trailing
// partition digits; extractPartitionNumber then returns "" and
// partitionNumberToSlot/ReconcileToBootedSlot can never match the running
// partition, permanently breaking this board's boot-slot bookkeeping and
// the uncommitted-update rollback safety net (see ReconcileToBootedSlot's
// "does not match configured rootfs A/B pair" warning). Resolving here,
// once, at construction guarantees every caller (including tests) gets a
// FileBasedBootEnv whose rootfsPartA/B can never hold an unresolvable path.
func NewFileBasedBootEnv(cmd system.Commander, rootfsPartA, rootfsPartB string) *FileBasedBootEnv {
	f := &FileBasedBootEnv{
		Commander:          cmd,
		slotFile:           DefaultSlotFile,
		bootCountFile:      DefaultBootCountFile,
		upgradeAvailFile:   DefaultUpgradeAvailFile,
		menderBootPartFile: DefaultMenderBootPartFile,
		rootfsPartA:        maybeResolveLink(rootfsPartA),
		rootfsPartB:        maybeResolveLink(rootfsPartB),
	}
	f.detectFn = f.detectCurrentPartitionNumber
	return f
}

// ReadEnv reads boot environment variables from files.
// Supports: mender_boot_part, mender_boot_part_hex, upgrade_available, bootcount
func (f *FileBasedBootEnv) ReadEnv(names ...string) (BootVars, error) {
	vars := make(BootVars)

	for _, name := range names {
		value, err := f.readVar(name)
		if err != nil {
			log.Debugf("FileBasedBootEnv: Could not read %s: %v", name, err)
			// Don't fail completely, just skip this variable
			continue
		}
		vars[name] = value
	}

	log.Debugf("FileBasedBootEnv: Read variables: %v", vars)
	return vars, nil
}

// WriteEnv writes boot environment variables to files.
//
// The A/B slot switch on file-based-boot boards (boot.scr reads
// mender_boot_part from the FAT boot partition) requires a DETERMINISTIC
// write order.  Writing mender_boot_part / mender_boot_part_hex triggers
// syncBootSlotToBootPartition(), which mounts the FAT boot partition and
// writes the slot byte U-Boot reads at boot — that can take several seconds
// under disk/IO load.  upgrade_available is a fast file write that signals
// "installed, ready to reboot" to the update orchestrator (and to the
// e2e_ota test, which reboots as soon as it observes upgrade_available=1).
//
// Go randomises map iteration, so the naive `range vars` loop sometimes wrote
// upgrade_available BEFORE the FAT sync had finished.  A reboot in that window
// booted the OLD slot, because the FAT still pointed at it; the agent only
// re-synced the FAT post-boot, too late.  This is a real, load-dependent race
// that intermittently breaks the slot switch on every file-based-boot board.
//
// Fix: always sync the boot partition FIRST (mender_boot_part*) and write
// upgrade_available LAST, so by the time anything can observe
// upgrade_available=1 the FAT already points at the new slot.  writeVar is
// synchronous, so ordering it first guarantees the sync completes first.
func (f *FileBasedBootEnv) WriteEnv(vars BootVars) error {
	log.Debugf("FileBasedBootEnv: Writing variables: %v", vars)

	// Priority order: FAT-syncing vars first, the "ready" flag last.
	writeOrder := []string{
		"mender_boot_part",
		"mender_boot_part_hex",
		"bootcount",
		"upgrade_available",
	}
	written := make(map[string]bool, len(vars))
	writeOne := func(name, value string) error {
		if err := f.writeVar(name, value); err != nil {
			return errors.Wrapf(err, "failed to write %s", name)
		}
		written[name] = true
		return nil
	}

	for _, name := range writeOrder {
		if value, ok := vars[name]; ok {
			if err := writeOne(name, value); err != nil {
				return err
			}
		}
	}
	// Forward-compatible: write any keys not covered by the explicit order.
	for name, value := range vars {
		if written[name] {
			continue
		}
		if err := writeOne(name, value); err != nil {
			return err
		}
	}

	return nil
}

func (f *FileBasedBootEnv) readVar(name string) (string, error) {
	switch name {
	case "mender_boot_part":
		return f.readMenderBootPart()
	case "mender_boot_part_hex":
		part, err := f.readMenderBootPart()
		if err != nil {
			return "", err
		}
		partNum, err := strconv.Atoi(part)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(int64(partNum), 16), nil
	case "upgrade_available":
		return f.readFile(f.upgradeAvailFile, "0")
	case "bootcount":
		return f.readFile(f.bootCountFile, "0")
	case "mender_check_saveenv_canary":
		// File-based env doesn't need canary check
		return "0", nil
	case "mender_saveenv_canary":
		// File-based env doesn't need canary check
		return "1", nil
	default:
		return "", errors.Errorf("unknown variable: %s", name)
	}
}

func (f *FileBasedBootEnv) writeVar(name, value string) error {
	switch name {
	case "mender_boot_part":
		return f.writeMenderBootPart(value)
	case "mender_boot_part_hex":
		// Convert hex to decimal and write
		partNum, err := strconv.ParseInt(value, 16, 64)
		if err != nil {
			return err
		}
		return f.writeMenderBootPart(strconv.FormatInt(partNum, 10))
	case "upgrade_available":
		return f.writeFile(f.upgradeAvailFile, value)
	case "bootcount":
		return f.writeFile(f.bootCountFile, value)
	default:
		log.Debugf("FileBasedBootEnv: Ignoring write of unknown variable: %s", name)
		return nil
	}
}

// readMenderBootPart reads the boot partition number.
// First tries the mender_boot_part file, then falls back to detecting from current_slot.
func (f *FileBasedBootEnv) readMenderBootPart() (string, error) {
	// Try reading from mender_boot_part file first
	if content, err := f.readFile(f.menderBootPartFile, ""); err == nil && content != "" {
		return content, nil
	}

	// Fall back to current_slot file
	slot, err := f.readFile(f.slotFile, "")
	if err == nil && slot != "" {
		return f.slotToPartitionNumber(slot)
	}

	// Final fallback: detect from currently mounted partition
	return f.detectCurrentPartitionNumber()
}

// writeMenderBootPart writes the boot partition number and also updates current_slot.
func (f *FileBasedBootEnv) writeMenderBootPart(partNum string) error {
	// Write to mender_boot_part file
	if err := f.writeFile(f.menderBootPartFile, partNum); err != nil {
		return err
	}

	// Also update current_slot for compatibility with cm5-ota
	slot := f.partitionNumberToSlot(partNum)
	if slot != "" {
		if err := f.writeFile(f.slotFile, slot); err != nil {
			log.Warnf("FileBasedBootEnv: Could not update slot file: %v", err)
		}
	}

	// Sync boot slot to boot partition for U-Boot access
	// This is critical for platforms where U-Boot reads from FAT32 boot partition.
	// A silent failure here would leave the FAT boot-partition byte (or
	// cmdline.txt on direct-boot boards) stale while WriteEnv still went on to
	// write upgrade_available=1 right after, making the caller believe the
	// slot switch succeeded when the device would actually reboot into the
	// OLD slot. Propagate the failure so WriteEnv aborts before
	// upgrade_available is written (BUG-106 follow-up, GAP-OTA-004).
	if err := f.syncBootSlotToBootPartition(partNum); err != nil {
		return errors.Wrap(err, "failed to sync boot slot to boot partition")
	}

	return nil
}

// deviceAlreadyMounted reports whether dev is already mounted anywhere on the
// system, per the given /proc/self/mounts lines. Resolves symlinks first
// (e.g. /dev/disk/by-partlabel/boot) so a by-partlabel/by-label alias for an
// already-mounted device is recognised as a collision even though its own
// path never appears literally in /proc/self/mounts (BUG-239).
func deviceAlreadyMounted(dev string, mountLines []string) bool {
	real, err := filepath.EvalSymlinks(dev)
	if err != nil {
		real = dev
	}
	for _, line := range mountLines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mountedReal, err := filepath.EvalSymlinks(fields[0])
		if err != nil {
			mountedReal = fields[0]
		}
		if mountedReal == real {
			return true
		}
	}
	return false
}

// syncBootSlotToBootPartition copies the mender_boot_part file to the boot partition
// so that U-Boot can read it during boot. U-Boot cannot read from ext4 data partition.
// Supports multiple platforms: i.MX, Raspberry Pi, Rockchip, generic ARM boards.
func (f *FileBasedBootEnv) syncBootSlotToBootPartition(partNum string) error {
	// Common boot partition mount points across different platforms
	// /boot/firmware - Raspberry Pi (Ubuntu/Debian)
	// /boot - Generic Linux, some Yocto builds
	// /mnt/boot - OTAPulse default mount point
	bootMountPoints := []string{"/mnt/boot", "/boot/firmware", "/boot"}
	bootFile := ""

	// Read mounts once before the loop for efficiency
	mountData, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		log.Warnf("FileBasedBootEnv: Failed to read /proc/self/mounts: %v", err)
		// Continue anyway - we'll try to mount the boot partition
	}
	mountLines := strings.Split(string(mountData), "\n")

	// Find where boot partition is mounted (must be FAT32/vfat for U-Boot access)
	for _, mount := range bootMountPoints {
		if _, err := os.Stat(mount); err == nil {
			// Check if it's a mounted vfat filesystem
			for _, line := range mountLines {
				fields := strings.Fields(line)
				if len(fields) >= 3 && fields[1] == mount && fields[2] == "vfat" {
					bootFile = filepath.Join(mount, "mender_boot_part")
					log.Debugf("FileBasedBootEnv: Found boot partition at %s", mount)
					break
				}
			}
		}
		if bootFile != "" {
			break
		}
	}

	// If boot partition not mounted, try to mount it
	if bootFile == "" {
		log.Debug("FileBasedBootEnv: Boot partition not mounted, attempting to mount")
		// Common boot partition devices across platforms
		bootDevices := []string{
			"/dev/disk/by-partlabel/boot", // GPT label (OTAPulse standard)
			"/dev/disk/by-label/boot",     // Filesystem label
			"/dev/disk/by-label/BOOT",     // Filesystem label (uppercase)
			"/dev/mmcblk1p1",              // eMMC (i.MX8, etc.)
			"/dev/mmcblk0p1",              // SD card
			"/dev/sda1",                   // USB/SATA
		}
		triedCandidate := false
		for _, dev := range bootDevices {
			if _, err := os.Stat(dev); err != nil {
				continue
			}
			// BUG-239: skip candidates already mounted elsewhere — most
			// notably the root device itself. Some boards (e.g. Jetson/
			// Tegra, whose A/B redundancy is entirely rootfs-level with no
			// separate FAT boot partition) have their lowest device-node
			// candidate (/dev/mmcblk0p1) BE the currently-mounted root
			// filesystem. util-linux's mount refuses to mount an
			// already-mounted block device a second time ("already mounted
			// on /"), and that refusal used to be treated identically to a
			// genuine boot-partition access failure — forcing an
			// unconditional ArtifactInstall rollback on every OTA on such
			// boards, even though there was never a boot partition to sync
			// to in the first place.
			if deviceAlreadyMounted(dev, mountLines) {
				log.Debugf("FileBasedBootEnv: Skipping boot-device candidate %s, already mounted elsewhere", dev)
				continue
			}
			triedCandidate = true
			mountPoint := "/mnt/boot"
			if err := os.MkdirAll(mountPoint, 0755); err != nil {
				log.Warnf("FileBasedBootEnv: Failed to create mount point: %v", err)
				continue
			}
			cmd := f.Commander.Command("mount", dev, mountPoint)
			if err := cmd.Run(); err == nil {
				bootFile = filepath.Join(mountPoint, "mender_boot_part")
				log.Infof("FileBasedBootEnv: Mounted boot partition %s at %s", dev, mountPoint)
				break
			} else {
				log.Warnf("FileBasedBootEnv: Failed to mount candidate boot device %s: %v", dev, err)
			}
		}

		if bootFile == "" && !triedCandidate {
			// No boot-partition candidate exists on this board at all once
			// already-mounted devices (typically root) are excluded — e.g.
			// Jetson/Tegra, which has no separate FAT boot device and whose
			// real A/B authority is handled entirely elsewhere (nvbootctrl
			// rootfs slots, driven by switch-boot-slot.sh as a state
			// script). FileBasedBootEnv's FAT sync is simply not applicable
			// here; treat it as a soft no-op rather than a fatal
			// ArtifactInstall error. This intentionally does NOT relax
			// BUG-106/GAP-OTA-004's protection: a board that DOES have a
			// real boot-partition candidate but fails to mount or write it
			// still falls through to the fatal path below.
			log.Warn("FileBasedBootEnv: No FAT boot-partition candidate present on this board (candidates exist only as already-mounted devices, e.g. root) — skipping FAT sync; boot slot must be managed by another mechanism on this board")
			return nil
		}
	}

	if bootFile == "" {
		log.Warn("FileBasedBootEnv: Could not find or mount boot partition for boot slot sync")
		return errors.New("could not find or mount boot partition for boot slot sync")
	}

	// Persist the OLD (pre-switch) slot value as mender_boot_part_prev, next to
	// mender_boot_part on the SAME FAT boot partition, BEFORE it is overwritten
	// below. This is the file-based-boot analogue of the direct-boot
	// cmdline_prev.txt backup (see updateBootCmdline) and is the rollback target
	// consumed by otapulse-boot-health.service's file-based-boot branch
	// (GAP-OTA-006 follow-up): a board like Orange Pi Zero 2W has NO working
	// U-Boot env (has_uboot_env: false) and NO cmdline.txt (not VideoCore
	// direct-boot), so without this record a slot that never boots the agent is
	// unrecoverable — there is nothing to revert TO.
	//
	// Deliberately written on the FAT boot partition, NOT /data: /data is
	// exactly the thing a disk-fill scenario (large_artifact.py LA-002) can
	// leave full or (BUG-235) unmounted, and it is also where the OLD
	// (pre-this-fix) bookkeeping lived — losing it to an ENOSPC-truncated write
	// during the very install that needs the rollback net is the root cause
	// this closes.
	//
	// "Write once per cycle" — same doctrine as updateBootCmdline's
	// cmdline_prev.txt: never clobber a backup already written earlier in this
	// OTA cycle (e.g. a retried WriteEnv call), and never write a value equal to
	// the new target (that would record "previous == current", making a later
	// revert a no-op). Best-effort: a failure to read/write the prev marker must
	// not abort the slot switch itself — it only means a subsequent revert has
	// nothing to restore, no worse than before this fix existed.
	prevFile := filepath.Join(filepath.Dir(bootFile), "mender_boot_part_prev")
	if oldData, statErr := os.ReadFile(bootFile); statErr == nil {
		oldPart := strings.TrimSpace(string(oldData))
		if oldPart != "" && oldPart != partNum {
			_, prevStatErr := os.Stat(prevFile)
			// A prev marker already on disk is only a "do not clobber" case
			// when it belongs to THIS in-flight cycle (a retried WriteEnv
			// call, or Rollback's own WriteEnv layered on top of Install's —
			// see dual_rootfs_device.go Rollback(), which writes
			// mender_boot_part again while upgrade_available is still "1").
			// upgrade_available is the ground truth for "mid-cycle": WriteEnv
			// always writes mender_boot_part BEFORE upgrade_available (see the
			// write-order doctrine above), so at this point its on-disk value
			// still reflects the state from BEFORE this call — "1" only when
			// a cycle is already genuinely in flight. Anything left over from
			// an EARLIER, already-resolved cycle (upgrade_available != "1")
			// must be overwritten with the current old/new pair, or it
			// silently poisons every future revert with stale data (BUG-311:
			// this existence-only check let a marker left behind by a
			// U-Boot-level revert on orange-pi-zero2w — which never clears
			// its own trigger file — suppress every subsequent real OTA's own
			// correct backup write, reported live 2026-08-19).
			ua, _ := f.readFile(f.upgradeAvailFile, "0")
			if os.IsNotExist(prevStatErr) || ua != "1" {
				// Best-effort, direct write (not the generic f.writeFile retry
				// helper below — that helper's remount-rw fallback targets
				// /data specifically, not this FAT boot mount). A failure here
				// only means a later revert has nothing to restore; it must
				// never abort the slot switch itself.
				if werr := os.WriteFile(prevFile, []byte(oldPart+"\n"), 0644); werr != nil {
					log.Warnf("FileBasedBootEnv: Failed to write mender_boot_part_prev rollback backup: %v", werr)
				} else {
					syscall.Sync()
					log.Infof("FileBasedBootEnv: Recorded mender_boot_part_prev=%s (rollback target) before switching to %s", oldPart, partNum)
				}
			}
		}
	}

	// Write to boot partition
	if err := os.WriteFile(bootFile, []byte(partNum+"\n"), 0644); err != nil {
		log.Warnf("FileBasedBootEnv: Failed to sync boot slot to boot partition: %v", err)
		return errors.Wrapf(err, "failed to write boot slot to %s", bootFile)
	}

	// Sync filesystem
	syscall.Sync()
	log.Infof("FileBasedBootEnv: Synced boot slot %s to %s", partNum, bootFile)

	// On direct-boot platforms (no U-Boot) the firmware reads root= straight
	// from cmdline.txt on the FAT boot partition, so there is no bootloader
	// env to switch. How the next boot is pointed at the new slot depends on
	// the board class — see applyDirectBootSlot: Raspberry Pi / VideoCore
	// boards use a firmware tryboot one-shot owned by the state scripts and
	// this agent's Reboot(), and the permanent cmdline.txt is deliberately
	// NOT touched here (BUG-245/BUG-353); any other direct-boot board still
	// gets the permanent rewrite.
	//
	// Only do this when U-Boot env tools are absent — on U-Boot platforms the
	// bootloader manages root= itself and cmdline.txt changes are unnecessary.
	// A board is "direct boot" (no U-Boot) when it has no WORKING U-Boot
	// environment. Detecting this by binary presence (exec.LookPath) is wrong:
	// the image ships u-boot-fw-utils (RRECOMMENDS) even on boards without a
	// U-Boot env — e.g. RPi4/VideoCore — where fw_printenv is present but
	// non-functional. That false-negative left isDirectBoot=false and SKIPPED
	// the cmdline.txt rewrite below, so the A/B slot switch never took on RPi4
	// (BUG-074). Probe whether fw_printenv can actually read an environment.
	isDirectBoot := !uBootEnvWorks()
	if isDirectBoot {
		bootDir := filepath.Dir(bootFile)
		newRootDev := f.getDeviceForPartNum(partNum)
		if newRootDev != "" {
			f.applyDirectBootSlot(bootDir, newRootDev)
		} else {
			log.Debugf("FileBasedBootEnv: Could not determine root device for partition %s, skipping cmdline.txt update", partNum)
		}
	}

	return nil
}

// uBootEnvWorks reports whether a WORKING U-Boot environment is present: the
// fw_printenv binary exists AND can actually read the environment. Boards may
// ship u-boot-fw-utils without having a real U-Boot env (RPi4/VideoCore direct
// boot), where fw_printenv exits non-zero ("Cannot read environment ..."). Those
// must be treated as direct boot so cmdline.txt is rewritten for the A/B switch
// (BUG-074). Keying off binary presence alone misclassified them as U-Boot.
func uBootEnvWorks() bool {
	path, err := exec.LookPath("fw_printenv")
	if err != nil {
		return false
	}
	// fw_printenv with no args dumps the whole environment; a board with no
	// U-Boot env returns a non-zero exit. Output is discarded — we only need
	// the exit status. Read-only, so safe to run during an OTA install.
	if err := exec.Command(path).Run(); err != nil {
		return false
	}
	return true
}

// getDeviceForPartNum returns the full block device path for the given partition number.
func (f *FileBasedBootEnv) getDeviceForPartNum(partNum string) string {
	if extractPartitionNumber(f.rootfsPartA) == partNum {
		return f.rootfsPartA
	}
	if extractPartitionNumber(f.rootfsPartB) == partNum {
		return f.rootfsPartB
	}
	return ""
}

// applyDirectBootSlot decides how the next boot is pointed at newRootDev on a
// direct-boot (no working U-Boot env) platform:
//
//   - Raspberry Pi / VideoCore (config.txt + cmdline.txt side by side on the
//     FAT boot partition): DO NOTHING HERE. The slot switch on this board
//     class is a firmware "tryboot" one-shot owned by the artifact's state
//     scripts: ArtifactReboot_Enter_01 builds tryboot.txt + cmdline_tryboot.txt
//     (root= -> new slot, plus TRY-only rootdelay/panic/init= edits) and this
//     agent's Reboot() then issues `reboot "0 tryboot"` (see
//     dualRootfsDeviceImpl.Reboot / TrybootRebootArgument), so the firmware
//     boots the TRY config exactly once; ArtifactCommit_Enter_01 rewrites the
//     permanent cmdline.txt ONLY after the new slot has booted and the agent
//     has verified it. A failed try (panic, watchdog, power-cut) resets, the
//     reset clears the one-shot, and the firmware boots the untouched
//     permanent cmdline.txt = the OLD slot, where the agent finds an
//     uncommitted update and rolls back — the board never loses its
//     known-good slot. Permanently rewriting cmdline.txt here, before the new
//     slot has ever booted, silently defeats all of that. BUG-353 was
//     reproduced live on a real RPi4: this code rewrote cmdline.txt to the
//     new slot two seconds BEFORE ArtifactReboot_Enter_01 even ran, so the
//     install-triggered reboot booted a deliberately-broken image from the
//     PERMANENT config with no fallback, and a power cycle could not recover
//     it (tryboot never engaged either — the agent's plain reboot carried no
//     argument; see system.SystemRebootCmd.RebootWithArgument).
//
//   - Any other direct-boot board (cmdline.txt but no config.txt — none in
//     the current fleet; kept for completeness): the pre-BUG-245 permanent
//     rewrite with a cmdline_prev.txt backup consumed by the pre-agent
//     otapulse-boot-health.service (GAP-OTA-006) — see updateDirectBootSlot.
func (f *FileBasedBootEnv) applyDirectBootSlot(bootDir, newRootDev string) {
	if isVideoCoreTrybootPlatform(bootDir) {
		log.Infof("FileBasedBootEnv: %s is a VideoCore/RPi tryboot platform (config.txt + cmdline.txt) — leaving the permanent cmdline.txt untouched; the slot switch is a firmware tryboot one-shot armed by ArtifactReboot_Enter_01 and committed by ArtifactCommit_Enter_01 after a verified boot (BUG-245/BUG-353)", bootDir)
		return
	}
	f.updateDirectBootSlot(bootDir, newRootDev)
}

// updateDirectBootSlot points the next boot at newRootDev on a NON-VideoCore
// direct-boot platform by PERMANENTLY rewriting root= in cmdline.txt.
// History: this was the BUG-074 fix and, until BUG-353, it also ran on RPi4 —
// see applyDirectBootSlot for why it must not. updateBootCmdline keeps a
// cmdline_prev.txt backup as the rollback target, consumed by the pre-agent
// otapulse-boot-health.service (meta-otapulse/recipes-core/otapulse-firstboot):
// it counts boot attempts and, once boot_count reaches DefaultMaxBootRetries
// with upgrade_available still set, restores cmdline_prev.txt over cmdline.txt
// and reboots onto the known-good slot (GAP-OTA-006). On a GOOD boot the
// backup is cleared by CommitUpdate (see dual_rootfs_device.go) and,
// defensively, by otapulse-boot-health on any later upgrade_available=0 boot.
func (f *FileBasedBootEnv) updateDirectBootSlot(bootDir, newRootDev string) {
	f.updateBootCmdline(bootDir, newRootDev)
}

// isVideoCoreTrybootPlatform reports whether bootDir (a mounted FAT boot
// partition) carries the Raspberry Pi VideoCore direct-boot signature:
// config.txt AND cmdline.txt side by side. This is the same predicate
// switch-boot-slot.sh's Method 8 gate and the ArtifactReboot_Enter_01 /
// ArtifactCommit_Enter_01 state scripts use to recognise this board class, so
// all of them agree on which boards own their slot switch via tryboot.
func isVideoCoreTrybootPlatform(bootDir string) bool {
	if _, err := os.Stat(filepath.Join(bootDir, "config.txt")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(bootDir, "cmdline.txt")); err != nil {
		return false
	}
	return true
}

// trybootRebootArgument is the reboot(2) RESTART2 string the Raspberry Pi
// firmware recognises (via the downstream kernel's rpi_firmware_notify_reboot,
// which matches " tryboot" WITH the leading space) as "boot tryboot.txt
// instead of config.txt exactly once". Identical to Raspberry Pi OS's
// documented `sudo reboot "0 tryboot"`.
const trybootRebootArgument = "0 tryboot"

// trybootRebootArgumentFor returns the reboot argument this agent must pass on
// its post-install reboot for the boot partition at bootDir: "0 tryboot" when
// bootDir is a VideoCore tryboot platform AND a try is armed (tryboot.txt
// present — written by ArtifactReboot_Enter_01, removed again by
// ArtifactCommit_Enter_01 / ArtifactRollbackReboot_Enter_01), "" (plain
// reboot) otherwise. Keyed on the armed TRY file rather than on the board
// class alone, so a reboot with nothing armed never asks the firmware to try a
// config that does not exist.
func trybootRebootArgumentFor(bootDir string) string {
	if !isVideoCoreTrybootPlatform(bootDir) {
		return ""
	}
	if _, err := os.Stat(filepath.Join(bootDir, "tryboot.txt")); err != nil {
		return ""
	}
	return trybootRebootArgument
}

// TrybootRebootArgument locates the FAT boot partition (same discovery as
// syncBootSlotToBootPartition / ClearDirectBootBackup) and returns the reboot
// argument the post-install reboot must carry — see trybootRebootArgumentFor.
// Best-effort and hardware-independent: boards with no FAT boot partition, no
// VideoCore signature, or no armed try get "" (plain reboot). Consumed by
// dualRootfsDeviceImpl.Reboot through an optional interface, exactly like
// ClearDirectBootBackup.
func (f *FileBasedBootEnv) TrybootRebootArgument() string {
	bootDir, mountedAt := f.findBootDir()
	if bootDir == "" {
		return ""
	}
	if mountedAt != "" {
		defer func() {
			if cmd := f.Commander.Command("umount", mountedAt); cmd != nil {
				_ = cmd.Run()
			}
		}()
	}
	return trybootRebootArgumentFor(bootDir)
}

// updateBootCmdline permanently rewrites the root= parameter in /boot/cmdline.txt to
// point to newRootDev, after saving the current (old-slot) cmdline to cmdline_prev.txt
// as a manual rollback target. Used for all direct-boot (no U-Boot) platforms.
func (f *FileBasedBootEnv) updateBootCmdline(bootDir, newRootDev string) {
	cmdlinePath := filepath.Join(bootDir, "cmdline.txt")
	data, err := os.ReadFile(cmdlinePath)
	if err != nil {
		log.Debugf("FileBasedBootEnv: No cmdline.txt at %s: %v", cmdlinePath, err)
		return
	}

	// Split into tokens and update root= in-place, preserving all other parameters
	parts := strings.Fields(strings.TrimSpace(string(data)))
	changed := false
	for i, p := range parts {
		if strings.HasPrefix(p, "root=") {
			parts[i] = "root=" + newRootDev
			changed = true
		}
	}
	if !changed {
		log.Debugf("FileBasedBootEnv: No root= in cmdline.txt at %s, skipping", cmdlinePath)
		return
	}

	// Preserve the original (old-slot) cmdline as the rollback target, ONCE — do
	// not clobber a backup written earlier in this same OTA cycle (e.g. by an earlier
	// SetUpdatedPartition call). This backup is consumed by the pre-agent
	// otapulse-boot-health.service, which restores it over cmdline.txt after
	// DefaultMaxBootRetries failed boots (revert-on-max-retries). On a good boot it
	// is instead removed by CommitUpdate (clearDirectBootBackup) once
	// upgrade_available is cleared, so a stale backup from this OTA cycle cannot
	// mislead the NEXT cycle's revert into restoring an already-current slot.
	prevPath := filepath.Join(bootDir, directBootPrevCmdline)
	if _, statErr := os.Stat(prevPath); os.IsNotExist(statErr) {
		if werr := os.WriteFile(prevPath, data, 0644); werr != nil {
			log.Warnf("FileBasedBootEnv: Failed to write cmdline_prev.txt rollback backup: %v", werr)
		}
	}

	updated := strings.Join(parts, " ") + "\n"
	if err := os.WriteFile(cmdlinePath, []byte(updated), 0644); err != nil {
		log.Warnf("FileBasedBootEnv: Failed to update cmdline.txt: %v", err)
		return
	}
	syscall.Sync()
	log.Infof("FileBasedBootEnv: Updated %s: root → %s", cmdlinePath, newRootDev)
}

// ClearDirectBootBackup removes the rollback-backup files written during this
// OTA cycle so a leftover cannot mislead the NEXT cycle's revert:
// cmdline_prev.txt on direct-boot (VideoCore cmdline.txt) boards, and
// mender_boot_part_prev (+ its FAT-resident uboot_boot_count) on
// file-based-boot (boot.scr, no working U-Boot env) boards such as Orange Pi
// Zero 2W. It is a no-op on U-Boot-env boards.
//
// Called from CommitUpdate after a confirmed-good boot: at that point the new
// slot is accepted, so any backup from getting there is stale. Leaving
// cmdline_prev.txt in place would make otapulse-boot-health restore an
// already-current slot instead of the real previous one (GAP-OTA-006).
// Leaving mender_boot_part_prev in place is worse on boot.scr boards: BOTH
// the U-Boot-level bootcount safety net (boot-orange-pi-zero2w.cmd) and
// otapulse-boot-health's file-based-boot branch treat its mere PRESENCE as
// "a switch is pending", with no way (from inside U-Boot) to tell a
// genuinely in-flight cycle from a stale leftover — so an uncleared marker
// makes every ordinary boot from then on look like an unconfirmed OTA, arms
// a panic-triggered hard reset on each one, and re-triggers a no-op revert
// every few boots, forever (BUG-311, reproduced live on orange-pi-zero2w
// dev-5b5a24ae 2026-08-19). Clearing it here, at the earliest point the OTA
// is known-good, closes that window as early as possible; otapulse-boot-
// health's own hygiene sweep on a later boot remains the backstop for the
// case where the agent never gets to commit at all.
//
// Best-effort and hardware-independent: the FAT boot partition is located by
// the same mount-point / by-label probing used by syncBootSlotToBootPartition;
// a failure to find or mount it, or to remove either file, is logged and
// ignored — the next boot's hygiene sweep is the backstop.
func (f *FileBasedBootEnv) ClearDirectBootBackup() {
	if uBootEnvWorks() {
		// U-Boot board: neither backup scheme applies here.
		return
	}

	bootDir, mountedAt := f.findBootDir()
	if bootDir == "" {
		log.Debug("FileBasedBootEnv: ClearDirectBootBackup: no boot partition found, skipping")
		return
	}
	if mountedAt != "" {
		defer func() {
			if cmd := f.Commander.Command("umount", mountedAt); cmd != nil {
				_ = cmd.Run()
			}
		}()
	}

	prevPath := filepath.Join(bootDir, directBootPrevCmdline)
	if _, err := os.Stat(prevPath); err == nil {
		if rerr := os.Remove(prevPath); rerr != nil {
			log.Warnf("FileBasedBootEnv: ClearDirectBootBackup: failed to remove %s: %v", prevPath, rerr)
		} else {
			syscall.Sync()
			log.Infof("FileBasedBootEnv: ClearDirectBootBackup: removed stale %s after good boot", prevPath)
		}
	}

	prevPartPath := filepath.Join(bootDir, "mender_boot_part_prev")
	if _, err := os.Stat(prevPartPath); err == nil {
		if rerr := os.Remove(prevPartPath); rerr != nil {
			log.Warnf("FileBasedBootEnv: ClearDirectBootBackup: failed to remove %s: %v", prevPartPath, rerr)
		} else {
			syscall.Sync()
			log.Infof("FileBasedBootEnv: ClearDirectBootBackup: removed stale %s after good boot", prevPartPath)
		}
		// Best-effort: also reset the FAT-resident boot-loader-level counter
		// (boot-orange-pi-zero2w.cmd's uboot_boot_count) so a good commit
		// leaves no residual count either. Harmless if this fails or the file
		// is absent — the counter only matters while mender_boot_part_prev
		// (just removed above) is present.
		bootCountPath := filepath.Join(bootDir, "uboot_boot_count")
		if _, err := os.Stat(bootCountPath); err == nil {
			if werr := os.WriteFile(bootCountPath, []byte("0"), 0644); werr != nil {
				log.Warnf("FileBasedBootEnv: ClearDirectBootBackup: failed to reset %s: %v", bootCountPath, werr)
			} else {
				syscall.Sync()
			}
		}
	}
}

// ReconcileToBootedSlot brings the agent's own /data/ota bookkeeping
// (mender_boot_part + current_slot) back in line with the slot the device is
// ACTUALLY running, on systemd-boot / loader.conf boards where the boot slot is
// owned by /boot (loader.conf + /boot/mender_boot_part), not by /data/ota.
//
// After a successful OTA slot switch on such boards, /boot and the live mount
// agree on the new slot, but /data/ota can stay pinned to the old slot forever
// because nothing ever reconciles it. That stale record only affects the
// agent's self-reported slot in the common case, but partitions.go's active
// partition fallback and dual_rootfs_device.go's Rollback can consult it on
// some paths, so realigning it once per boot removes that (rare) risk (BUG-110).
//
// SAFETY: this must NEVER run mid-OTA. When upgrade_available=1, mender_boot_part
// intentionally points at the pending TARGET slot (not the running one), and
// rewriting /data/ota to the running slot would corrupt the in-flight update.
// The upgrade_available gate below is therefore the first, unconditional check.
//
// It writes ONLY the two /data/ota files directly (writeFile), never
// writeMenderBootPart — the latter triggers a FAT sync / cmdline.txt rewrite
// that is boot-authoritative on direct-boot boards and must not be touched by a
// bookkeeping reconcile.
func (f *FileBasedBootEnv) ReconcileToBootedSlot() {
	// Step 1 — SAFETY GATE (must be first and unconditional): never touch
	// /data/ota while an OTA is pending. mender_boot_part points at the target
	// slot in that window; reconciling to the running slot would corrupt it.
	ua, _ := f.readFile(f.upgradeAvailFile, "0")
	if ua == "1" {
		log.Debug("FileBasedBootEnv: ReconcileToBootedSlot: upgrade_available=1, OTA pending — skipping reconcile")
		return
	}

	// Step 2 — detect the actually-booted partition number. Never write on a
	// detection failure.
	actualPart, err := f.detectFn()
	if err != nil {
		log.Warnf("FileBasedBootEnv: ReconcileToBootedSlot: could not detect current partition: %v", err)
		return
	}

	// Step 3 — map it to a slot label. Empty means the running partition is not
	// part of the configured A/B pair; never write a slot outside that pair.
	slot := f.partitionNumberToSlot(actualPart)
	if slot == "" {
		log.Warnf("FileBasedBootEnv: ReconcileToBootedSlot: running partition %q does not match configured rootfs A/B pair — skipping reconcile", actualPart)
		return
	}

	// Step 4 — no-op fast path: if /data/ota already agrees with reality (the
	// common case on every normal boot), do nothing and touch nothing.
	curPart, _ := f.readFile(f.menderBootPartFile, "")
	curSlot, _ := f.readFile(f.slotFile, "")
	if curPart == actualPart && curSlot == slot {
		return
	}

	// Step 5 — realign the bookkeeping. Write ONLY the two /data/ota files;
	// deliberately avoid writeMenderBootPart (no FAT sync / cmdline.txt rewrite).
	log.Infof("FileBasedBootEnv: ReconcileToBootedSlot: realigning /data/ota to booted slot: mender_boot_part %q→%q, current_slot %q→%q",
		curPart, actualPart, curSlot, slot)
	if werr := f.writeFile(f.menderBootPartFile, actualPart); werr != nil {
		log.Warnf("FileBasedBootEnv: ReconcileToBootedSlot: failed to write mender_boot_part: %v", werr)
		return
	}
	if werr := f.writeFile(f.slotFile, slot); werr != nil {
		log.Warnf("FileBasedBootEnv: ReconcileToBootedSlot: failed to write current_slot: %v", werr)
	}
}

// findBootDir returns the directory of the mounted FAT boot partition, mounting
// it under /mnt/boot if necessary. The second return value is the mount point we
// created (non-empty only when this call performed the mount, so the caller can
// unmount it). Mirrors the boot-partition discovery in syncBootSlotToBootPartition.
func (f *FileBasedBootEnv) findBootDir() (bootDir string, mountedAt string) {
	bootMountPoints := []string{"/mnt/boot", "/boot/firmware", "/boot"}

	mountData, _ := os.ReadFile("/proc/self/mounts")
	mountLines := strings.Split(string(mountData), "\n")

	for _, mount := range bootMountPoints {
		if _, err := os.Stat(mount); err != nil {
			continue
		}
		for _, line := range mountLines {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[1] == mount && fields[2] == "vfat" {
				return mount, ""
			}
		}
	}

	// Not mounted — try to mount a known boot device (by-label first).
	bootDevices := []string{
		"/dev/disk/by-partlabel/boot",
		"/dev/disk/by-label/boot",
		"/dev/disk/by-label/BOOT",
		"/dev/mmcblk1p1",
		"/dev/mmcblk0p1",
		"/dev/sda1",
	}
	for _, dev := range bootDevices {
		if _, err := os.Stat(dev); err != nil {
			continue
		}
		// BUG-239: same already-mounted skip as syncBootSlotToBootPartition
		// — avoids a doomed "mount <root device> /mnt/boot" attempt (and its
		// stderr noise) on boards with no separate FAT boot partition.
		if deviceAlreadyMounted(dev, mountLines) {
			continue
		}
		mountPoint := "/mnt/boot"
		if err := os.MkdirAll(mountPoint, 0755); err != nil {
			continue
		}
		if cmd := f.Commander.Command("mount", dev, mountPoint); cmd != nil {
			if err := cmd.Run(); err == nil {
				return mountPoint, mountPoint
			}
		}
	}
	return "", ""
}

// slotToPartitionNumber converts slot letter (a/b) to partition number.
func (f *FileBasedBootEnv) slotToPartitionNumber(slot string) (string, error) {
	slot = strings.ToLower(strings.TrimSpace(slot))

	var partition string
	switch slot {
	case "a":
		partition = f.rootfsPartA
	case "b":
		partition = f.rootfsPartB
	default:
		return "", errors.Errorf("unknown slot: %s", slot)
	}

	return extractPartitionNumber(partition), nil
}

// partitionNumberToSlot converts partition number to slot letter.
func (f *FileBasedBootEnv) partitionNumberToSlot(partNum string) string {
	partANum := extractPartitionNumber(f.rootfsPartA)
	partBNum := extractPartitionNumber(f.rootfsPartB)

	if partNum == partANum {
		return "a"
	} else if partNum == partBNum {
		return "b"
	}
	return ""
}

// detectCurrentPartitionNumber detects the current root partition number.
func (f *FileBasedBootEnv) detectCurrentPartitionNumber() (string, error) {
	// Read /proc/self/mounts to find the root partition
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return "", errors.Wrap(err, "failed to read /proc/self/mounts")
	}

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "/" {
			device := fields[0]
			// Handle /dev/root symlink
			if device == "/dev/root" {
				resolved, err := filepath.EvalSymlinks(device)
				if err == nil {
					device = resolved
				}
			}
			return extractPartitionNumber(device), nil
		}
	}

	return "", errors.New("could not detect current root partition")
}

// readFile reads content from a file, returning defaultVal if file doesn't exist.
func (f *FileBasedBootEnv) readFile(path, defaultVal string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultVal, nil
		}
		return defaultVal, err
	}
	return strings.TrimSpace(string(data)), nil
}

// writeFile writes content to a file, creating parent directories if needed.
// Includes retry logic for transient filesystem issues (e.g., after large OTA writes).
//
// Writes ATOMICALLY via writeFileAtomic: content lands in a temp file in the
// same directory, is fsync'd, then rename(2)'d over the target. This closes a
// real data-loss hole: the previous implementation called os.WriteFile
// (O_TRUNC) directly against the target, so a write that failed PARTWAY
// (classically ENOSPC on a near-full /data — e.g. large_artifact.py's LA-002,
// which deliberately fills /data to ~10 MiB free) left the file TRUNCATED TO
// 0 BYTES: the target was truncated by O_TRUNC before the failing write ever
// completed. A state script or boot-health check that later reads that file
// (e.g. /data/ota/mender_boot_part) then sees an empty value instead of
// either the old or the new slot. rename(2) within one filesystem is atomic,
// so the target now either keeps its OLD content or gets the FULLY-WRITTEN
// new content — never a partial one.
func (f *FileBasedBootEnv) writeFile(path, content string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return errors.Wrapf(err, "failed to create directory %s", dir)
	}

	// Retry logic for transient filesystem issues
	// After large writes (6GB OTA image), the filesystem may temporarily
	// appear read-only OR out of space due to buffer pressure or pending
	// syncs (or a deliberately near-full /data — see the doc comment above).
	// The eMMC controller may need significant time to flush all writes.
	maxRetries := 15
	retryDelay := 2 * time.Second

	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			log.Warnf("FileBasedBootEnv: Retrying write to %s (attempt %d/%d) after error: %v",
				path, i+1, maxRetries, lastErr)
			// Sync filesystem before retry to ensure pending writes are flushed
			syscall.Sync()
			// Try to remount /data as rw if it became read-only
			if i%3 == 0 {
				log.Infof("FileBasedBootEnv: Attempting to remount /data as read-write")
				f.Commander.Command("mount", "-o", "remount,rw", "/data").Run()
			}
			time.Sleep(retryDelay)
			// Cap the delay at 5 seconds
			if retryDelay < 5*time.Second {
				retryDelay += 1 * time.Second
			}
		}

		lastErr = writeFileAtomic(path, dir, content+"\n", 0644)
		if lastErr == nil {
			if i > 0 {
				log.Infof("FileBasedBootEnv: Successfully wrote to %s on attempt %d", path, i+1)
			}
			// Sync to ensure the write (and its rename) is persisted
			syscall.Sync()
			return nil
		}

		// Retry on EROFS (read-only filesystem) or ENOSPC (out of space) — both
		// are the transient conditions this loop exists for. Previously only
		// EROFS was retried: an ENOSPC write returned immediately on the FIRST
		// attempt even though the surrounding comment already documented the
		// buffer-pressure/large-OTA-write scenario as exactly what this loop
		// should ride out, and even though ENOSPC is the realistic failure mode
		// on a near-full /data (e.g. LA-002), not EROFS.
		if !errors.Is(lastErr, syscall.EROFS) && !errors.Is(lastErr, syscall.ENOSPC) {
			// Not a retryable error, don't retry
			break
		}
	}

	return lastErr
}

// writeFileAtomic writes content to a temp file inside dir, fsyncs it, then
// rename(2)s it over path. Never partially overwrites an existing path: the
// rename either lands in full (path now holds the complete new content) or it
// doesn't happen at all (path is untouched) — unlike os.WriteFile's O_TRUNC,
// which can leave path truncated to zero bytes if the write fails partway.
// dir MUST be on the same filesystem as path (every caller passes
// filepath.Dir(path)) — rename(2) is only atomic within one filesystem.
func writeFileAtomic(path, dir, content string, perm os.FileMode) error {
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-")
	if err != nil {
		return errors.Wrapf(err, "failed to create temp file in %s", dir)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup of the temp file on any path that returns before a
	// successful rename (a successful rename moves the temp file away, so this
	// Remove then simply no-ops with ENOENT).
	defer os.Remove(tmpPath)

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return errors.Wrapf(err, "failed to write temp file %s", tmpPath)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return errors.Wrapf(err, "failed to chmod temp file %s", tmpPath)
	}
	// fsync the temp file's data BEFORE rename, so an unclean
	// shutdown/power-cut between the rename and the next full sync can never
	// leave the renamed target holding unflushed (i.e. possibly incomplete)
	// content.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return errors.Wrapf(err, "failed to fsync temp file %s", tmpPath)
	}
	if err := tmp.Close(); err != nil {
		return errors.Wrapf(err, "failed to close temp file %s", tmpPath)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return errors.Wrapf(err, "failed to rename %s to %s", tmpPath, path)
	}
	return nil
}
