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
}

// NewFileBasedBootEnv creates a new file-based boot environment handler.
// This is an alternative to U-Boot environment that works with any bootloader.
func NewFileBasedBootEnv(cmd system.Commander, rootfsPartA, rootfsPartB string) *FileBasedBootEnv {
	return &FileBasedBootEnv{
		Commander:          cmd,
		slotFile:           DefaultSlotFile,
		bootCountFile:      DefaultBootCountFile,
		upgradeAvailFile:   DefaultUpgradeAvailFile,
		menderBootPartFile: DefaultMenderBootPartFile,
		rootfsPartA:        rootfsPartA,
		rootfsPartB:        rootfsPartB,
	}
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
func (f *FileBasedBootEnv) WriteEnv(vars BootVars) error {
	log.Debugf("FileBasedBootEnv: Writing variables: %v", vars)

	for name, value := range vars {
		if err := f.writeVar(name, value); err != nil {
			return errors.Wrapf(err, "failed to write %s", name)
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
	// This is critical for platforms where U-Boot reads from FAT32 boot partition
	f.syncBootSlotToBootPartition(partNum)

	return nil
}

// syncBootSlotToBootPartition copies the mender_boot_part file to the boot partition
// so that U-Boot can read it during boot. U-Boot cannot read from ext4 data partition.
// Supports multiple platforms: i.MX, Raspberry Pi, Rockchip, generic ARM boards.
func (f *FileBasedBootEnv) syncBootSlotToBootPartition(partNum string) {
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
		for _, dev := range bootDevices {
			if _, err := os.Stat(dev); err == nil {
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
				}
			}
		}
	}

	if bootFile == "" {
		log.Warn("FileBasedBootEnv: Could not find or mount boot partition for boot slot sync")
		return
	}

	// Write to boot partition
	if err := os.WriteFile(bootFile, []byte(partNum+"\n"), 0644); err != nil {
		log.Warnf("FileBasedBootEnv: Failed to sync boot slot to boot partition: %v", err)
		return
	}

	// Sync filesystem
	syscall.Sync()
	log.Infof("FileBasedBootEnv: Synced boot slot %s to %s", partNum, bootFile)

	// On direct-boot platforms (e.g. RPi4 without U-Boot), the VideoCore firmware
	// reads root= from cmdline.txt on the FAT boot partition. Without U-Boot to
	// switch partitions via fw_setenv, we must update cmdline.txt directly so the
	// device boots from the correct partition after OTA reboot.
	//
	// Only do this when U-Boot env tools are absent — on U-Boot platforms the
	// bootloader manages root= itself and cmdline.txt changes are unnecessary.
	_, errPrintenv := exec.LookPath("fw_printenv")
	_, errSetenv := exec.LookPath("fw_setenv")
	isDirectBoot := errPrintenv != nil && errSetenv != nil
	if isDirectBoot {
		bootDir := filepath.Dir(bootFile)
		newRootDev := f.getDeviceForPartNum(partNum)
		if newRootDev != "" {
			f.updateBootCmdline(bootDir, newRootDev)
		} else {
			log.Debugf("FileBasedBootEnv: Could not determine root device for partition %s, skipping cmdline.txt update", partNum)
		}
	}
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

// updateBootCmdline updates the root= parameter in /boot/cmdline.txt to point to
// newRootDev. This is required on direct-boot platforms (e.g. RPi4 without U-Boot)
// where the VideoCore firmware reads cmdline.txt directly.
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

	updated := strings.Join(parts, " ") + "\n"
	if err := os.WriteFile(cmdlinePath, []byte(updated), 0644); err != nil {
		log.Warnf("FileBasedBootEnv: Failed to update cmdline.txt: %v", err)
		return
	}
	syscall.Sync()
	log.Infof("FileBasedBootEnv: Updated %s: root → %s", cmdlinePath, newRootDev)
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

// ReconcileBootState detects and corrects mismatches between the actual boot
// partition and the recorded boot state files. This fixes BUG-001 where after
// a rollback, /data/ota/mender_boot_part still points to the failed partition.
//
// Returns true if a mismatch was detected and corrected.
func (f *FileBasedBootEnv) ReconcileBootState() (bool, error) {
	// Detect what partition we actually booted from
	actualPartNum, err := f.detectCurrentPartitionNumber()
	if err != nil {
		return false, errors.Wrap(err, "boot state reconciliation: failed to detect current partition")
	}

	// Read what the boot state file thinks
	recordedPartNum, err := f.readFile(f.menderBootPartFile, "")
	if err != nil || recordedPartNum == "" {
		// No recorded state yet — initialize it from actual
		log.Infof("ReconcileBootState: No recorded boot partition, initializing to %s", actualPartNum)
		if writeErr := f.writeFile(f.menderBootPartFile, actualPartNum); writeErr != nil {
			return false, errors.Wrap(writeErr, "boot state reconciliation: failed to write mender_boot_part")
		}
		// Also set slot file
		slot := f.partitionNumberToSlot(actualPartNum)
		if slot != "" {
			if writeErr := f.writeFile(f.slotFile, slot); writeErr != nil {
				log.Warnf("ReconcileBootState: Failed to initialize slot file: %v", writeErr)
			}
		}
		// Sync to boot partition so U-Boot sees the correct state
		f.syncBootSlotToBootPartition(actualPartNum)
		return true, nil
	}

	if actualPartNum == recordedPartNum {
		log.Debugf("ReconcileBootState: Boot state consistent (partition %s)", actualPartNum)
		return false, nil
	}

	// Mismatch detected — this is the BUG-001 scenario
	log.Warnf("ReconcileBootState: MISMATCH DETECTED — actual partition %s, recorded %s. Correcting.",
		actualPartNum, recordedPartNum)

	// Update mender_boot_part to match reality
	if writeErr := f.writeFile(f.menderBootPartFile, actualPartNum); writeErr != nil {
		return false, errors.Wrap(writeErr, "boot state reconciliation: failed to correct mender_boot_part")
	}

	// Update slot file
	slot := f.partitionNumberToSlot(actualPartNum)
	if slot != "" {
		if writeErr := f.writeFile(f.slotFile, slot); writeErr != nil {
			log.Warnf("ReconcileBootState: Failed to update slot file: %v", writeErr)
		}
	}

	// Reset upgrade_available to "0" since the rollback is complete
	if writeErr := f.writeFile(f.upgradeAvailFile, "0"); writeErr != nil {
		log.Warnf("ReconcileBootState: Failed to reset upgrade_available: %v", writeErr)
	}

	// Reset boot count
	if writeErr := f.writeFile(f.bootCountFile, "0"); writeErr != nil {
		log.Warnf("ReconcileBootState: Failed to reset boot_count: %v", writeErr)
	}

	// Sync to boot partition so U-Boot sees the corrected state
	f.syncBootSlotToBootPartition(actualPartNum)

	log.Infof("ReconcileBootState: Boot state corrected to partition %s (slot %s)", actualPartNum, slot)
	return true, nil
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
func (f *FileBasedBootEnv) writeFile(path, content string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return errors.Wrapf(err, "failed to create directory %s", dir)
	}

	// Retry logic for transient filesystem issues
	// After large writes (6GB OTA image), the filesystem may temporarily
	// appear read-only due to buffer pressure or pending syncs.
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

		lastErr = os.WriteFile(path, []byte(content+"\n"), 0644)
		if lastErr == nil {
			if i > 0 {
				log.Infof("FileBasedBootEnv: Successfully wrote to %s on attempt %d", path, i+1)
			}
			// Sync to ensure the write is persisted
			syscall.Sync()
			return nil
		}

		// Check if error is EROFS (read-only filesystem) - worth retrying
		if !errors.Is(lastErr, syscall.EROFS) {
			// Not a read-only error, don't retry
			break
		}
	}

	return lastErr
}
