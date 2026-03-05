#!/bin/bash
# OTAPulse Boot Slot Switching Script
# Switches the active boot slot between A and B partitions
#
# Usage:
#   switch-boot-slot.sh        # Toggle to the other slot
#   switch-boot-slot.sh a      # Switch to slot A
#   switch-boot-slot.sh b      # Switch to slot B
#   switch-boot-slot.sh status # Show current slot
#
# This script updates the file-based boot environment in /data/ota/

set -e

OTA_DIR="/data/ota"
BOOT_PART_FILE="${OTA_DIR}/mender_boot_part"
CURRENT_SLOT_FILE="${OTA_DIR}/current_slot"
UPGRADE_AVAILABLE_FILE="${OTA_DIR}/upgrade_available"

# Detect partition numbers from device
detect_partitions() {
    # Get rootfs_a partition number
    if [ -e "/dev/disk/by-partlabel/rootfs_a" ]; then
        PART_A=$(readlink -f /dev/disk/by-partlabel/rootfs_a | grep -oE '[0-9]+$')
    else
        # Fallback: assume partition 2 is rootfs_a
        PART_A=2
    fi

    # Get rootfs_b partition number
    if [ -e "/dev/disk/by-partlabel/rootfs_b" ]; then
        PART_B=$(readlink -f /dev/disk/by-partlabel/rootfs_b | grep -oE '[0-9]+$')
    else
        # Fallback: assume partition 3 is rootfs_b
        PART_B=3
    fi
}

get_current_slot() {
    if [ -f "$CURRENT_SLOT_FILE" ]; then
        cat "$CURRENT_SLOT_FILE"
    elif [ -f "$BOOT_PART_FILE" ]; then
        local part=$(cat "$BOOT_PART_FILE")
        if [ "$part" = "$PART_A" ]; then
            echo "a"
        else
            echo "b"
        fi
    else
        # Default to slot a
        echo "a"
    fi
}

get_current_part() {
    if [ -f "$BOOT_PART_FILE" ]; then
        cat "$BOOT_PART_FILE"
    else
        echo "$PART_A"
    fi
}

switch_to_slot() {
    local target_slot="$1"
    local target_part

    case "$target_slot" in
        a|A)
            target_slot="a"
            target_part="$PART_A"
            ;;
        b|B)
            target_slot="b"
            target_part="$PART_B"
            ;;
        *)
            echo "Error: Invalid slot '$target_slot'. Use 'a' or 'b'."
            exit 1
            ;;
    esac

    mkdir -p "$OTA_DIR"

    echo "$target_part" > "$BOOT_PART_FILE"
    echo "$target_slot" > "$CURRENT_SLOT_FILE"
    # Mark upgrade available so bootloader knows to switch
    echo "1" > "$UPGRADE_AVAILABLE_FILE"

    # If boot.scr reads mender_boot_part from a FAT boot partition,
    # we must also update the file there (boot.scr can only read from FAT).
    update_fat_boot_part "$target_part"

    echo "Switched to slot $target_slot (partition $target_part)"
    echo "Reboot to boot from the new slot"
}

# Update mender_boot_part on the FAT boot partition (if present).
# boot.scr on i.MX8, some Rockchip, etc. reads this file via U-Boot
# 'load mmc' which can only access the FAT filesystem, not /data/ota/.
update_fat_boot_part() {
    local target_part="$1"
    local fat_dev=""

    # Find the FAT boot partition by label
    for dev in /dev/mmcblk*p1 /dev/sd?1; do
        [ -b "$dev" ] || continue
        local fstype=$(blkid -s TYPE -o value "$dev" 2>/dev/null)
        local label=$(blkid -s LABEL -o value "$dev" 2>/dev/null)
        if [ "$fstype" = "vfat" ] && [ "$label" = "boot" ]; then
            fat_dev="$dev"
            break
        fi
    done

    [ -n "$fat_dev" ] || return 0

    local mnt="/tmp/.bootfat_$$"
    mkdir -p "$mnt"

    if mount "$fat_dev" "$mnt" 2>/dev/null; then
        # Only update if boot.scr exists (confirms this FAT is the boot source)
        if [ -f "$mnt/boot.scr" ]; then
            printf '%s' "$target_part" > "$mnt/mender_boot_part"
            sync
            echo "Updated FAT boot partition ($fat_dev) mender_boot_part=$target_part"
        fi
        umount "$mnt" 2>/dev/null
    fi
    rmdir "$mnt" 2>/dev/null || true
}

show_status() {
    detect_partitions
    local current_slot=$(get_current_slot)
    local current_part=$(get_current_part)
    local upgrade_avail="0"

    if [ -f "$UPGRADE_AVAILABLE_FILE" ]; then
        upgrade_avail=$(cat "$UPGRADE_AVAILABLE_FILE")
    fi

    echo "Boot Slot Status:"
    echo "  Current slot: $current_slot"
    echo "  Boot partition: $current_part"
    echo "  Partition A: $PART_A"
    echo "  Partition B: $PART_B"
    echo "  Upgrade available: $upgrade_avail"

    # Show mounted root
    local mounted_root=$(findmnt -n -o SOURCE /)
    echo "  Currently mounted root: $mounted_root"
}

# Main
detect_partitions

case "${1:-toggle}" in
    a|A)
        switch_to_slot a
        ;;
    b|B)
        switch_to_slot b
        ;;
    status|--status|-s)
        show_status
        ;;
    toggle|"")
        current=$(get_current_slot)
        if [ "$current" = "a" ]; then
            switch_to_slot b
        else
            switch_to_slot a
        fi
        ;;
    -h|--help|help)
        echo "Usage: $0 [a|b|status|toggle]"
        echo ""
        echo "Commands:"
        echo "  a, A       Switch to slot A"
        echo "  b, B       Switch to slot B"
        echo "  status     Show current boot slot status"
        echo "  toggle     Toggle to the other slot (default)"
        echo ""
        exit 0
        ;;
    *)
        echo "Error: Unknown command '$1'"
        echo "Use '$0 --help' for usage information"
        exit 1
        ;;
esac
