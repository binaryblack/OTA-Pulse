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

	return nil
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
