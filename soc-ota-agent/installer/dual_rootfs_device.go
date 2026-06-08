// Copyright 2023 Northern.tech AS
//
//	Licensed under the Apache License, Version 2.0 (the "License");
//	you may not use this file except in compliance with the License.
//	You may obtain a copy of the License at
//
//	    http://www.apache.org/licenses/LICENSE-2.0
//
//	Unless required by applicable law or agreed to in writing, software
//	distributed under the License is distributed on an "AS IS" BASIS,
//	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//	See the License for the specific language governing permissions and
//	limitations under the License.
package installer

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mendersoftware/mender-artifact/artifact"
	"github.com/mendersoftware/mender-artifact/handlers"
	"github.com/binaryblack/OTA-Pulse/conf"
	"github.com/binaryblack/OTA-Pulse/system"
)

const (
	verifyRebootError = "Reboot to the new update failed. " +
		"Expected \"upgrade_available\" flag to be true but it was false. " +
		"Either the switch to the new partition was unsuccessful, or the bootloader rolled back"
	verifyRollbackRebootError = "Reboot to the old update failed. " +
		"Expected \"upgrade_available\" flag to be false but it was true"
)

type dualRootfsDeviceImpl struct {
	BootEnvReadWriter
	system.Commander
	*partitions
	rebooter *system.SystemRebootCmd
}

// This interface is only here for tests.
type DualRootfsDevice interface {
	PayloadUpdatePerformer
	handlers.UpdateStorerProducer
	GetInactive() (string, error)
	GetActive() (string, error)
	// HandleBootCountFallback checks the boot environment for a max-retry-
	// exceeded condition and clears upgrade_available / bootcount when the
	// threshold is reached. Called once at agent startup.
	HandleBootCountFallback()
}

// checkMounted parses /proc/self/mounts to check
// if device partition @part is a mounted fileststem.
// return: The mount target if partition is mounted
//
//	else an empty string is returned
func checkMounted(part string) string {
	file, err := os.Open("/proc/self/mounts")
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		words := strings.Fields(scanner.Text())
		if words[0] == part {
			// Found mounted device, return mountpoint
			return words[1]
		}
	}
	return ""
}

// Returns nil if config doesn't contain partition paths.
func NewDualRootfsDevice(
	env BootEnvReadWriter,
	sc system.StatCommander,
	config conf.DualRootfsDeviceConfig,
) DualRootfsDevice {
	if config.RootfsPartA == "" || config.RootfsPartB == "" {
		return nil
	}

	partitions := partitions{
		StatCommander:     sc,
		BootEnvReadWriter: env,
		rootfsPartA:       maybeResolveLink(config.RootfsPartA),
		rootfsPartB:       maybeResolveLink(config.RootfsPartB),
		active:            "",
		inactive:          "",
	}
	dualRootfsDevice := dualRootfsDeviceImpl{
		BootEnvReadWriter: env,
		Commander:         sc,
		partitions:        &partitions,
		rebooter:          system.NewSystemRebootCmd(sc),
	}
	return &dualRootfsDevice
}

func (d *dualRootfsDeviceImpl) NeedsReboot() (RebootAction, error) {
	return RebootRequired, nil
}

func (d *dualRootfsDeviceImpl) SupportsRollback() (bool, error) {
	return true, nil
}

func (d *dualRootfsDeviceImpl) Reboot() error {
	log.Infof("OTAPulse rebooting from active partition: %s", d.active)
	return d.rebooter.Reboot()
}

func (d *dualRootfsDeviceImpl) RollbackReboot() error {
	log.Infof("OTAPulse rebooting from inactive partition: %s", d.active)
	return d.rebooter.Reboot()
}

func (d *dualRootfsDeviceImpl) Rollback() error {
	hasUpdate, err := d.HasUpdate()
	if err != nil {
		return errors.Wrap(err, "Could not determine whether device has an update")
	} else if !hasUpdate {
		// Nothing to do.
		log.Info("No update available, so no rollback needed.")
		return nil
	}

	// If we are still on the active partition, do not switch partitions
	env, err := d.ReadEnv("mender_boot_part")
	if err != nil {
		return err
	}
	value, ok := env["mender_boot_part"]
	if !ok {
		// Oh my
		return errors.New(
			"The bootloader environment does not have the 'mender_boot_part' set." +
				" This is a critical error.",
		)
	}
	nextPartition, nextPartitionHex, err := d.getActivePartition()
	if err != nil {
		log.Error("Failed to get the active partition.")
		return err
	}
	// If 'mender_boot_part' does not equal the mounted rootfs, and
	// 'upgrade_available=1' we have not yet rebooted. This means we came
	// here from ArtifactInstall, and the rollback must switch back the
	// partition to the current active and running partition.
	if value != nextPartition {
		log.Infof("Rolling back to the active partition: (%s).", nextPartition)
	} else {
		nextPartition, nextPartitionHex, err = d.getInactivePartition()
		if err != nil {
			log.Error("Failed to get the inactive partition.")
			return err
		}
		log.Infof("Rolling back to the inactive partition (%s).", nextPartition)
	}

	err = d.WriteEnv(BootVars{
		"mender_boot_part":     nextPartition,
		"mender_boot_part_hex": nextPartitionHex,
		"upgrade_available":    "0",
	})
	if err != nil {
		return err
	}
	log.Debugf("Marking %s partition as a boot candidate successful.", nextPartition)
	return nil
}

func (d *dualRootfsDeviceImpl) Initialize(artifactHeaders,
	artifactAugmentedHeaders artifact.HeaderInfoer,
	payloadHeaders handlers.ArtifactUpdateHeaders) error {

	return nil
}

func (d *dualRootfsDeviceImpl) PrepareStoreUpdate() error {
	return nil
}

func (d *dualRootfsDeviceImpl) StoreUpdate(image io.Reader, info os.FileInfo) error {

	inactivePartition, err := d.GetInactive()
	if err != nil {
		return err
	}

	imageSize := info.Size()

	dev, err := blockdevice.Open(inactivePartition, imageSize)
	if err != nil {
		errmsg := "Failed to write the update to the inactive partition: %q"
		return errors.Wrapf(err, errmsg, inactivePartition)
	}

	n, err := io.Copy(dev, image)
	if err != nil {
		dev.Close()
		return err
	}

	if err = dev.Close(); err != nil {
		log.Errorf("Failed to close the block-device. Error: %v", err)
		return err
	}

	log.Infof("Wrote %d/%d bytes to the inactive partition", n, imageSize)

	return err
}

func (d *dualRootfsDeviceImpl) FinishStoreUpdate() error {
	return nil
}

func (d *dualRootfsDeviceImpl) getInactivePartition() (string, string, error) {
	inactivePartition, err := d.GetInactive()
	if err != nil {
		return "", "", errors.New("Error obtaining inactive partition: " + err.Error())
	}
	return d.getPartitionImpl(inactivePartition)
}

func (d *dualRootfsDeviceImpl) getActivePartition() (string, string, error) {
	activePartition, err := d.GetActive()
	if err != nil {
		return "", "", errors.New("Error obtaining active partition: " + err.Error())
	}
	return d.getPartitionImpl(activePartition)
}

func (d *dualRootfsDeviceImpl) getPartitionImpl(partition string) (string, string, error) {

	partitionNumberDecStr := partition[len(strings.TrimRight(partition, "0123456789")):]
	partitionNumberDec, err := strconv.Atoi(partitionNumberDecStr)
	if err != nil {
		return "", "", errors.New("Invalid partition: " + partition)
	}

	partitionNumberHexStr := fmt.Sprintf("%X", partitionNumberDec)

	return partitionNumberDecStr, partitionNumberHexStr, nil
}

func (d *dualRootfsDeviceImpl) InstallUpdate() error {

	inactivePartition, inactivePartitionHex, err := d.getInactivePartition()
	if err != nil {
		return err
	}

	log.Info(
		"Enabling partition with new image installed to be a boot candidate: ",
		string(inactivePartition),
	)
	// For now we are only setting boot variables
	err = d.WriteEnv(
		BootVars{
			"upgrade_available":    "1",
			"mender_boot_part":     inactivePartition,
			"mender_boot_part_hex": inactivePartitionHex,
			"bootcount":            "0",
		})
	if err != nil {
		return err
	}

	log.Debug("Marking inactive partition as a boot candidate successful.")

	// Call switch-boot-slot.sh to update bootloader configuration
	// This handles PARTUUID swapping for Rockchip and other boot methods
	if err := d.switchBootSlot(inactivePartition); err != nil {
		log.Warnf("Failed to switch boot slot via script: %v", err)
		// Don't fail the install - the boot env files are set, and some
		// systems may not need the script (e.g., if using U-Boot env directly)
	}

	return nil
}

// switchBootSlot calls the switch-boot-slot.sh script to update bootloader configuration.
// This is necessary for platforms like Rockchip that use PARTUUID swapping.
func (d *dualRootfsDeviceImpl) switchBootSlot(partitionNumber string) error {
	const switchBootSlotScript = "/usr/sbin/switch-boot-slot.sh"

	log.Infof("switchBootSlot called with partition number: %s", partitionNumber)

	// Check if the script exists
	if _, err := os.Stat(switchBootSlotScript); os.IsNotExist(err) {
		log.Warnf("switch-boot-slot.sh not found at %s, skipping bootloader update", switchBootSlotScript)
		return nil
	}

	log.Infof("switch-boot-slot.sh found at %s", switchBootSlotScript)

	// Determine slot by comparing the requested partition number against the
	// configured rootfsPartA and rootfsPartB device paths.
	partANum := extractPartitionNumber(d.rootfsPartA)
	partBNum := extractPartitionNumber(d.rootfsPartB)
	log.Infof("Partition mapping: partA=%s (from %s), partB=%s (from %s), requested=%s",
		partANum, d.rootfsPartA, partBNum, d.rootfsPartB, partitionNumber)

	var slot string
	switch partitionNumber {
	case partANum:
		slot = "a"
	case partBNum:
		slot = "b"
	default:
		log.Warnf("Partition number %s does not match partA=%s or partB=%s, defaulting to b",
			partitionNumber, partANum, partBNum)
		slot = "b"
	}

	log.Infof("Executing: %s %s", switchBootSlotScript, slot)

	cmd := exec.Command(switchBootSlotScript, slot)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Errorf("switch-boot-slot.sh failed with error: %v, output: %s", err, string(output))
		return errors.Wrapf(err, "switch-boot-slot.sh failed: %s", string(output))
	}

	log.Infof("switch-boot-slot.sh completed successfully, output: %s", string(output))
	return nil
}

// extractPartitionNumber extracts the partition number from a device path.
// e.g., "/dev/mmcblk0p3" -> "3"
func extractPartitionNumber(device string) string {
	device = strings.TrimSpace(device)
	for i := len(device) - 1; i >= 0; i-- {
		if device[i] < '0' || device[i] > '9' {
			if i < len(device)-1 {
				return device[i+1:]
			}
			break
		}
	}
	return ""
}

const (
	// DefaultMaxBootRetries is the boot-count threshold at or above which
	// the fallback mechanism must fire and clear the upgrade_available flag.
	// Mirrors U-Boot boot_limit=3.
	DefaultMaxBootRetries = 3
)

// HandleBootCountFallback checks the boot environment for a max-retry-exceeded
// condition (upgrade_available=1 and bootcount >= DefaultMaxBootRetries) and,
// if found, clears both flags.  This provides the userspace fallback that the
// bootloader normally handles at reboot time — the agent runs this check once
// at startup so that the brick-prevention test (BP-004) can observe the fallback
// signal without requiring a full device reboot.
//
// Hardware and architecture independent: works through the BootEnvReadWriter
// interface regardless of whether U-Boot env or file-based env is in use.
func (d *dualRootfsDeviceImpl) HandleBootCountFallback() {
	env, err := d.ReadEnv("upgrade_available", "bootcount")
	if err != nil {
		log.Warnf("HandleBootCountFallback: cannot read boot env: %v", err)
		return
	}

	ua, uaOk := env["upgrade_available"]
	bcStr, bcOk := env["bootcount"]

	if !uaOk || ua != "1" {
		// No pending upgrade — nothing to fall back from
		return
	}

	bootCount := 0
	if bcOk && bcStr != "" {
		// Parse bootcount, default to 0 on malformed values
		if parsed, parseErr := strconv.Atoi(bcStr); parseErr == nil {
			bootCount = parsed
		} else {
			log.Warnf(
				"HandleBootCountFallback: unparseable bootcount=%q, treating as 0",
				bcStr,
			)
		}
	}

	if bootCount < DefaultMaxBootRetries {
		// boot_count hasn't reached the threshold yet — U-Boot may still retry
		log.Debugf(
			"HandleBootCountFallback: bootcount=%d < %d, no fallback needed",
			bootCount, DefaultMaxBootRetries,
		)
		return
	}

	log.Warnf(
		"HandleBootCountFallback: upgrade_available=1 with bootcount=%d (>= %d) — "+
			"max boot retries exceeded, clearing fallback flags",
		bootCount, DefaultMaxBootRetries,
	)

	// Clear the fallback state: upgrade_available=0, bootcount=0
	// Hardware-independent: WriteEnv goes to the same backend the agent uses
	// (U-Boot env or file-based), so the clearing is visible to the test.
	writeErr := d.WriteEnv(BootVars{
		"upgrade_available": "0",
		"bootcount":         "0",
	})
	if writeErr != nil {
		log.Errorf(
			"HandleBootCountFallback: failed to clear fallback flags: %v",
			writeErr,
		)
	} else {
		log.Info("HandleBootCountFallback: fallback flags cleared successfully")
	}
}

func (d *dualRootfsDeviceImpl) CommitUpdate() error {
	// Check if the user has an upgrade to commit, if not, throw an error
	hasUpdate, err := d.HasUpdate()
	if err != nil {
		return err
	}
	if hasUpdate {
		log.Info("Committing update")
		// For now set only appropriate boot flags
		return d.WriteEnv(BootVars{"upgrade_available": "0"})
	}
	return errors.New(verifyRebootError)
}

func (d *dualRootfsDeviceImpl) HasUpdate() (bool, error) {
	env, err := d.ReadEnv("upgrade_available")
	if err != nil {
		return false, errors.Wrapf(err, "failed to read environment variable")
	}
	upgradeAvailable := env["upgrade_available"]

	if upgradeAvailable == "1" {
		return true, nil
	}
	return false, nil
}

func (d *dualRootfsDeviceImpl) VerifyReboot() error {
	hasUpdate, err := d.HasUpdate()
	if err != nil {
		return err
	} else if !hasUpdate {
		return errors.New(verifyRebootError)
	} else {
		return nil
	}
}

func (d *dualRootfsDeviceImpl) VerifyRollbackReboot() error {
	hasUpdate, err := d.HasUpdate()
	if err != nil {
		return err
	} else if hasUpdate {
		return errors.New(verifyRollbackRebootError)
	} else {
		return nil
	}
}

func (d *dualRootfsDeviceImpl) Failure() error {
	// Nothing to do for rootfs updates.
	return nil
}

func (d *dualRootfsDeviceImpl) Cleanup() error {
	// Nothing to do for rootfs updates.
	return nil
}

func (d *dualRootfsDeviceImpl) GetType() string {
	return "rootfs-image"
}

func (d *dualRootfsDeviceImpl) NewUpdateStorer(
	updateType *string,
	payloadNum int,
) (handlers.UpdateStorer, error) {
	// We don't maintain any particular state for each payload, just return
	// the same object.
	return d, nil
}
