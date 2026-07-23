#!/bin/sh
# Generic A/B Boot Slot Switcher
# Works with various bootloaders and SoC types
# Supports: U-Boot (fw_setenv), GPT attributes, extlinux, and direct boot flag

set -e

# BUG-074 (Defect 1): The Go agent passes "a" or "b" (lowercase); normalise here
# so the rest of the script's comparisons against "A"/"B" all work correctly.
# Using printf+tr avoids any dependency on bash-only ${var^^} (this is /bin/sh).
SLOT=$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')  # A or B
BOOT_CONFIG_DIR="/boot"
EXTLINUX_CONF="/boot/extlinux/extlinux.conf"
GRUB_CFG="/boot/grub/grub.cfg"
UENV_TXT="/boot/uEnv.txt"
BOOT_SLOT_FILE="/data/boot_slot"

# Partition mapping - can be overridden by environment.
#
# BUG (FRDM E2E-001, 2026-07-23): an older agent invoked this script WITHOUT
# ROOTFS_A_PARTITION/ROOTFS_B_PARTITION set. The hardcoded fallback below used
# to be /dev/mmcblk0p3 / /dev/mmcblk0p4 unconditionally, which is wrong for
# every board that doesn't happen to use those exact nodes (e.g. the FRDM's
# eMMC is mmcblk2, rootfs_a=mmcblk2p2, rootfs_b=mmcblk2p3). Method 5 then
# wrote the WRONG partition number ("4") into the FAT mender_boot_part file,
# U-Boot's boot.scr rejected it as an unrecognised slot, and the switch
# silently failed. Boards are provisioned fleet-wide with GPT partlabels
# rootfs_a/rootfs_b, so prefer resolving the real device nodes from those
# labels; only fall back to the old static guess (loudly) if partlabels are
# unavailable on this board.
if [ -z "${ROOTFS_A_PARTITION:-}" ] && [ -e /dev/disk/by-partlabel/rootfs_a ]; then
    ROOTFS_A_PARTITION="$(readlink -f /dev/disk/by-partlabel/rootfs_a)"
fi
if [ -z "${ROOTFS_B_PARTITION:-}" ] && [ -e /dev/disk/by-partlabel/rootfs_b ]; then
    ROOTFS_B_PARTITION="$(readlink -f /dev/disk/by-partlabel/rootfs_b)"
fi

if [ -z "${ROOTFS_A_PARTITION:-}" ]; then
    logger -t switch-boot-slot "WARNING: ROOTFS_A_PARTITION not set and no rootfs_a partlabel found; falling back to /dev/mmcblk0p3 (may be WRONG for this board)" 2>/dev/null || true
    echo "[switch-boot-slot] WARNING: ROOTFS_A_PARTITION not set and no rootfs_a partlabel found; falling back to /dev/mmcblk0p3 (may be WRONG for this board)" >&2
fi
if [ -z "${ROOTFS_B_PARTITION:-}" ]; then
    logger -t switch-boot-slot "WARNING: ROOTFS_B_PARTITION not set and no rootfs_b partlabel found; falling back to /dev/mmcblk0p4 (may be WRONG for this board)" 2>/dev/null || true
    echo "[switch-boot-slot] WARNING: ROOTFS_B_PARTITION not set and no rootfs_b partlabel found; falling back to /dev/mmcblk0p4 (may be WRONG for this board)" >&2
fi

ROOTFS_A="${ROOTFS_A_PARTITION:-/dev/mmcblk0p3}"
ROOTFS_B="${ROOTFS_B_PARTITION:-/dev/mmcblk0p4}"

log() {
    echo "[switch-boot-slot] $*"
    logger -t switch-boot-slot "$*" 2>/dev/null || true
}

get_partuuid() {
    local partition="$1"
    blkid -s PARTUUID -o value "$partition" 2>/dev/null
}

get_base_device() {
    echo "$1" | sed 's/p[0-9]*$//'
}

get_partition_number() {
    echo "$1" | grep -o '[0-9]*$'
}

# Method 1: Use Rockchip PARTUUID swapping method
# The kernel cmdline has root=PARTUUID=614e0000-0000 (prefix match)
# We swap the PARTUUIDs so the target slot gets the matching prefix
try_partuuid_swap() {
    if ! command -v sgdisk >/dev/null 2>&1; then
        log "sgdisk not available"
        return 1
    fi

    # Boot-effectiveness gate (BUG-157 follow-up): swapping PARTUUIDs only
    # switches the boot slot on boards whose kernel actually roots by the
    # fixed 614e0000-prefix PARTUUID scheme. On boards that root by
    # PARTLABEL / LABEL / device path (e.g. extlinux distroboot with
    # root=PARTLABEL=rootfs_a, or boot.scr numeric-slot boards), the sgdisk
    # calls below succeed operationally but change NOTHING about which slot
    # boots — a false success here would short-circuit main()'s method
    # chain, skip the genuinely effective method for the board, and strand
    # the device on the old slot with the agent believing the switch took.
    # (Historically this method always failed on such boards only because
    # the default /dev/mmcblk0pN paths didn't exist; once the agent passes
    # real ROOTFS_*_PARTITION paths, this gate is what keeps it honest.)
    # Probe the RUNNING cmdline — hardware-independent, no board names.
    if ! grep -q 'root=PARTUUID=614e0000' /proc/cmdline 2>/dev/null; then
        log "cmdline does not root by the 614e0000 PARTUUID scheme, skipping PARTUUID swap"
        return 1
    fi

    local target_partition other_partition
    local target_num other_num
    local base_device
    local UUID_ACTIVE_PREFIX="614e0000"
    local UUID_INACTIVE_PREFIX="614e0001"
    
    if [ "$SLOT" = "A" ]; then
        target_partition="$ROOTFS_A"
        other_partition="$ROOTFS_B"
    else
        target_partition="$ROOTFS_B"
        other_partition="$ROOTFS_A"
    fi
    
    base_device=$(get_base_device "$target_partition")
    target_num=$(get_partition_number "$target_partition")
    other_num=$(get_partition_number "$other_partition")
    
    log "Swapping PARTUUIDs for Rockchip boot: target=$target_partition ($target_num), other=$other_partition ($other_num)"
    
    # Set target partition UUID to match boot cmdline (614e0000-xxxx)
    if sgdisk -u ${target_num}:${UUID_ACTIVE_PREFIX}-0000-4b53-8000-1d28000054a9 "$base_device" 2>/dev/null; then
        log "Set active PARTUUID on partition $target_num"
    else
        log "Failed to set PARTUUID on partition $target_num"
        return 1
    fi
    
    # Set other partition UUID to non-matching prefix (614e0001-xxxx)
    if sgdisk -u ${other_num}:${UUID_INACTIVE_PREFIX}-0000-4b53-8000-1d28000054aa "$base_device" 2>/dev/null; then
        log "Set inactive PARTUUID on partition $other_num"
    else
        log "Warning: Failed to set PARTUUID on partition $other_num"
    fi
    
    # Sync changes
    sync
    partprobe "$base_device" 2>/dev/null || true
    
    log "PARTUUID swap completed successfully"
    return 0
}

# Method 2: Use fw_setenv (U-Boot environment)
try_fw_setenv() {
    if ! command -v fw_setenv >/dev/null 2>&1; then
        log "fw_setenv not available"
        return 1
    fi
    
    if [ ! -f /etc/fw_env.config ]; then
        log "fw_env.config not found, running setup"
        if [ -x /usr/sbin/setup-uboot-env.sh ]; then
            /usr/sbin/setup-uboot-env.sh
        else
            log "setup-uboot-env.sh not found"
            return 1
        fi
    fi
    
    local target_partition target_partuuid
    if [ "$SLOT" = "A" ]; then
        target_partition="$ROOTFS_A"
    else
        target_partition="$ROOTFS_B"
    fi
    
    target_partuuid=$(get_partuuid "$target_partition")
    if [ -z "$target_partuuid" ]; then
        log "Failed to get PARTUUID for $target_partition"
        return 1
    fi
    
    log "Setting U-Boot root to PARTUUID=$target_partuuid"
    
    # Try different U-Boot variable names
    if fw_setenv root "PARTUUID=$target_partuuid" 2>/dev/null; then
        log "Set root variable"
        return 0
    fi
    
    if fw_setenv bootargs_root "root=PARTUUID=$target_partuuid" 2>/dev/null; then
        log "Set bootargs_root variable"
        return 0
    fi
    
    log "fw_setenv failed"
    return 1
}

# Method 3: Update extlinux.conf
# extlinux.conf is the FIRST config U-Boot distroboot consults when present,
# so on extlinux boards (e.g. Radxa CM5) its root= line is the sole
# authority on which slot boots. Two robustness requirements learned on
# real hardware (BUG-157 follow-up):
#   1. The conf lives on the FAT boot partition and /boot is NOT reliably
#      mounted at install time — locate + mount the FAT by label ourselves
#      when the fstab path is absent.
#   2. Configs written as root=PARTLABEL=rootfs_a|b were untouched by the
#      old PARTUUID/dev-path-only seds, which then reported success while
#      changing nothing. Rewrite ANY root= form, prefer the target's
#      PARTLABEL (stable across sgdisk PARTUUID churn), and VERIFY the
#      rewrite actually landed before claiming success.
try_extlinux() {
    local conf="$EXTLINUX_CONF"
    local mnt=""

    if [ ! -f "$conf" ]; then
        # /boot path absent — find the FAT boot partition by label and mount it.
        local fat_dev=""
        if [ -e /dev/disk/by-label/boot ]; then
            fat_dev=$(readlink -f /dev/disk/by-label/boot)
        else
            for dev in /dev/mmcblk*p[1-4] /dev/sd?[1-4]; do
                [ -b "$dev" ] || continue
                if [ "$(blkid -s TYPE -o value "$dev" 2>/dev/null)" = "vfat" ] && \
                   [ "$(blkid -s LABEL -o value "$dev" 2>/dev/null)" = "boot" ]; then
                    fat_dev="$dev"
                    break
                fi
            done
        fi
        if [ -z "$fat_dev" ]; then
            log "extlinux.conf not found"
            return 1
        fi
        mnt="/tmp/.extlinux_$$"
        mkdir -p "$mnt"
        if ! mount "$fat_dev" "$mnt" 2>/dev/null; then
            log "extlinux.conf not found (cannot mount $fat_dev)"
            rmdir "$mnt" 2>/dev/null || true
            return 1
        fi
        conf="$mnt/extlinux/extlinux.conf"
        if [ ! -f "$conf" ]; then
            log "extlinux.conf not found"
            umount "$mnt" 2>/dev/null
            rmdir "$mnt" 2>/dev/null || true
            return 1
        fi
    fi

    local target_partition
    if [ "$SLOT" = "A" ]; then
        target_partition="$ROOTFS_A"
    else
        target_partition="$ROOTFS_B"
    fi

    # Prefer the target's PARTLABEL (matches root=PARTLABEL=rootfs_a|b
    # configs and survives PARTUUID churn); fall back to PARTUUID, then to
    # the raw device path.
    local new_root="" partlabel target_partuuid
    partlabel=$(blkid -s PARTLABEL -o value "$target_partition" 2>/dev/null)
    target_partuuid=$(get_partuuid "$target_partition")
    if [ -n "$partlabel" ]; then
        new_root="PARTLABEL=$partlabel"
    elif [ -n "$target_partuuid" ]; then
        new_root="PARTUUID=$target_partuuid"
    else
        new_root="$target_partition"
    fi

    if ! grep -q 'root=' "$conf"; then
        log "extlinux.conf has no root= parameter"
        if [ -n "$mnt" ]; then
            umount "$mnt" 2>/dev/null
            rmdir "$mnt" 2>/dev/null || true
        fi
        return 1
    fi

    log "Updating extlinux.conf root=$new_root"

    # Backup original
    cp "$conf" "${conf}.bak"

    # Rewrite any root= form (PARTUUID=, PARTLABEL=, LABEL=, /dev/...)
    sed -i "s|root=[^ ]*|root=$new_root|g" "$conf"

    # Verify the rewrite landed — a sed that matched nothing must not be
    # reported as success (that false success is what stranded slot
    # switches on PARTLABEL boards).
    if ! grep -q "root=$new_root" "$conf"; then
        log "extlinux.conf rewrite did not take, restoring backup"
        cp "${conf}.bak" "$conf"
        if [ -n "$mnt" ]; then
            umount "$mnt" 2>/dev/null
            rmdir "$mnt" 2>/dev/null || true
        fi
        return 1
    fi
    sync

    if [ -n "$mnt" ]; then
        umount "$mnt" 2>/dev/null
        rmdir "$mnt" 2>/dev/null || true
    fi

    log "Updated extlinux.conf"
    return 0
}

# Method 4: Update uEnv.txt (some U-Boot configs)
try_uenv() {
    if [ ! -f "$UENV_TXT" ]; then
        log "uEnv.txt not found"
        return 1
    fi
    
    local target_partition target_partuuid
    if [ "$SLOT" = "A" ]; then
        target_partition="$ROOTFS_A"
    else
        target_partition="$ROOTFS_B"
    fi
    
    target_partuuid=$(get_partuuid "$target_partition")
    if [ -z "$target_partuuid" ]; then
        log "Failed to get PARTUUID"
        return 1
    fi
    
    log "Updating uEnv.txt for PARTUUID=$target_partuuid"
    
    # Backup original
    cp "$UENV_TXT" "${UENV_TXT}.bak"
    
    # Update or add root variable
    if grep -q "^root=" "$UENV_TXT"; then
        sed -i "s|^root=.*|root=PARTUUID=$target_partuuid|" "$UENV_TXT"
    else
        echo "root=PARTUUID=$target_partuuid" >> "$UENV_TXT"
    fi
    
    log "Updated uEnv.txt"
    return 0
}

# Method 5: Update mender_boot_part on FAT boot partition
# boot.scr on i.MX8, some Rockchip, etc. reads this file via U-Boot
# 'load mmc' which can only access the FAT filesystem, not /data/ota/.
try_fat_boot_part() {
    local fat_dev=""
    local target_part

    if [ "$SLOT" = "A" ]; then
        target_part=$(get_partition_number "$ROOTFS_A")
    else
        target_part=$(get_partition_number "$ROOTFS_B")
    fi

    # Find FAT boot partition by label.
    # WARNING: the p1-only glob below is LOAD-BEARING — do NOT widen it to
    # p[1-4]. Boards whose FAT lives on p2+ (e.g. Radxa CM5) carry a
    # boot.scr there too, but their bootloader consults extlinux.conf, not
    # boot.scr; widening this glob would make this method false-succeed on
    # such boards and short-circuit main()'s chain before the genuinely
    # effective try_extlinux runs. boot.scr-numeric boards (iMX8MP,
    # BeaglePlay) all keep their FAT on p1.
    for dev in /dev/mmcblk*p1 /dev/sd?1; do
        [ -b "$dev" ] || continue
        local fstype=$(blkid -s TYPE -o value "$dev" 2>/dev/null)
        local label=$(blkid -s LABEL -o value "$dev" 2>/dev/null)
        if [ "$fstype" = "vfat" ] && [ "$label" = "boot" ]; then
            fat_dev="$dev"
            break
        fi
    done

    if [ -z "$fat_dev" ]; then
        log "No FAT boot partition found"
        return 1
    fi

    local mnt="/tmp/.bootfat_$$"
    mkdir -p "$mnt"

    if ! mount "$fat_dev" "$mnt" 2>/dev/null; then
        log "Failed to mount $fat_dev"
        rmdir "$mnt" 2>/dev/null || true
        return 1
    fi

    if [ ! -f "$mnt/boot.scr" ]; then
        log "No boot.scr on FAT partition"
        umount "$mnt" 2>/dev/null
        rmdir "$mnt" 2>/dev/null || true
        return 1
    fi

    printf '%s' "$target_part" > "$mnt/mender_boot_part"
    sync
    umount "$mnt" 2>/dev/null
    rmdir "$mnt" 2>/dev/null || true

    log "Updated FAT boot partition ($fat_dev) mender_boot_part=$target_part"
    return 0
}

# Method 7: systemd-boot EFI Automatic Boot Assessment (loader/entries)
# EFI ABA boards (e.g. Dragon Q6A) keep slot-a+N.conf / slot-b+N.conf under
# loader/entries on the ESP; the +N suffix is the remaining try-count
# (+0 = exhausted/skipped) and loader.conf's `default slot-X*` GLOB pins the
# choice (a bare name is fnmatch-dead — systemd #38813). Switching slots =
# ensure the target entry exists at +3, re-pin the default, exhaust the
# current entry. That ordering is deliberately brick-safe: a crash at any
# midpoint leaves the default pointing at an entry that exists with a
# positive count. Idempotent with the ArtifactReboot_Enter state script's
# Section 4 and with otapulse-sboot-bridge, which manage the same files.
# Self-gating: does nothing unless slot-*.conf ABA entries are present.
try_systemd_boot_aba() {
    local esp="" mnt=""

    # Prefer the mounted /boot; else locate + mount the ESP by label.
    if [ -d "$BOOT_CONFIG_DIR/loader/entries" ]; then
        esp="$BOOT_CONFIG_DIR"
    else
        local fat_dev=""
        if [ -e /dev/disk/by-label/boot ]; then
            fat_dev=$(readlink -f /dev/disk/by-label/boot)
        else
            for dev in /dev/mmcblk*p[1-4] /dev/sd?[1-4]; do
                [ -b "$dev" ] || continue
                if [ "$(blkid -s TYPE -o value "$dev" 2>/dev/null)" = "vfat" ] && \
                   [ "$(blkid -s LABEL -o value "$dev" 2>/dev/null)" = "boot" ]; then
                    fat_dev="$dev"
                    break
                fi
            done
        fi
        if [ -z "$fat_dev" ]; then
            log "no systemd-boot ESP found"
            return 1
        fi
        mnt="/tmp/.sdboot_$$"
        mkdir -p "$mnt"
        if ! mount "$fat_dev" "$mnt" 2>/dev/null; then
            log "no systemd-boot ESP found (cannot mount $fat_dev)"
            rmdir "$mnt" 2>/dev/null || true
            return 1
        fi
        if [ ! -d "$mnt/loader/entries" ]; then
            log "no systemd-boot loader/entries"
            umount "$mnt" 2>/dev/null
            rmdir "$mnt" 2>/dev/null || true
            return 1
        fi
        esp="$mnt"
    fi

    local tgt cur
    if [ "$SLOT" = "A" ]; then
        tgt="a"; cur="b"
    else
        tgt="b"; cur="a"
    fi

    local entries="$esp/loader/entries"
    # sort -r picks the highest try-count first (slot-a+3.conf before +0)
    local current_conf target_conf
    current_conf=$(find "$entries" -maxdepth 1 -name "slot-${cur}+*.conf" 2>/dev/null | sort -r | head -1)
    target_conf=$(find "$entries" -maxdepth 1 -name "slot-${tgt}+*.conf" 2>/dev/null | sort -r | head -1)

    if [ -z "$current_conf" ] && [ -z "$target_conf" ]; then
        # No ABA slot entries at all — not an ABA board, let other methods try.
        log "no slot-*.conf ABA entries"
        if [ -n "$mnt" ]; then
            umount "$mnt" 2>/dev/null
            rmdir "$mnt" 2>/dev/null || true
        fi
        return 1
    fi

    # Step 1: ensure the target entry exists with +3 tries.
    local target_3="$entries/slot-${tgt}+3.conf"
    if [ -n "$target_conf" ]; then
        [ "$target_conf" != "$target_3" ] && mv "$target_conf" "$target_3"
    elif [ -n "$current_conf" ]; then
        # Generate from the current entry, swapping the rootfs PARTLABEL.
        sed "s/root=PARTLABEL=rootfs_${cur}/root=PARTLABEL=rootfs_${tgt}/g" \
            "$current_conf" > "$target_3"
    fi
    if [ ! -f "$target_3" ]; then
        log "failed to materialise slot-${tgt}+3.conf"
        if [ -n "$mnt" ]; then
            umount "$mnt" 2>/dev/null
            rmdir "$mnt" 2>/dev/null || true
        fi
        return 1
    fi
    sync

    # Step 2: re-pin the default (GLOB form — a bare name matches nothing).
    if [ -f "$esp/loader/loader.conf" ]; then
        sed -i "s/^default .*/default slot-${tgt}*/" "$esp/loader/loader.conf"
    fi

    # Step 3: exhaust the current slot's entry (+0 = never auto-selected).
    if [ -n "$current_conf" ] && [ -f "$current_conf" ]; then
        local current_0="$entries/slot-${cur}+0.conf"
        [ "$current_conf" != "$current_0" ] && mv "$current_conf" "$current_0"
    fi
    sync

    # Verify: target entry present at +3 and (if loader.conf exists) the
    # default glob now points at the target slot.
    if [ ! -f "$target_3" ]; then
        log "systemd-boot ABA switch verification failed"
        if [ -n "$mnt" ]; then
            umount "$mnt" 2>/dev/null
            rmdir "$mnt" 2>/dev/null || true
        fi
        return 1
    fi
    if [ -f "$esp/loader/loader.conf" ] && \
       ! grep -q "^default slot-${tgt}\*" "$esp/loader/loader.conf"; then
        log "systemd-boot ABA default re-pin verification failed"
        if [ -n "$mnt" ]; then
            umount "$mnt" 2>/dev/null
            rmdir "$mnt" 2>/dev/null || true
        fi
        return 1
    fi

    if [ -n "$mnt" ]; then
        umount "$mnt" 2>/dev/null
        rmdir "$mnt" 2>/dev/null || true
    fi

    log "systemd-boot ABA: slot-${tgt}+3.conf active, default slot-${tgt}*, slot-${cur} exhausted"
    return 0
}

# Method 6: Store boot slot preference (for bootloader that reads it)
store_boot_slot() {
    mkdir -p "$(dirname "$BOOT_SLOT_FILE")"
    echo "$SLOT" > "$BOOT_SLOT_FILE"
    
    # Also store in /boot if writable
    if [ -w /boot ]; then
        echo "$SLOT" > /boot/boot_slot
    fi
    
    log "Stored boot slot preference: $SLOT"
    return 0
}

# Main
main() {
    if [ -z "$SLOT" ]; then
        echo "Usage: $0 <A|B>"
        echo "Switches boot to the specified partition slot"
        exit 1
    fi
    
    if [ "$SLOT" != "A" ] && [ "$SLOT" != "B" ]; then
        echo "ERROR: Slot must be A or B"
        exit 1
    fi
    
    log "Switching boot to slot $SLOT"
    
    local success=false
    
    # Try methods in order of preference
    # PARTUUID swapping is most reliable for Rockchip platforms.
    # try_systemd_boot_aba sits BEFORE try_fw_setenv deliberately: EFI ABA
    # boards self-identify via loader/entries, and letting them fall through
    # to fw_setenv would attempt a blind raw-offset env write that
    # systemd-boot never reads (false success / corruption hazard).
    if try_partuuid_swap; then
        success=true
    elif try_fat_boot_part; then
        success=true
    elif try_systemd_boot_aba; then
        success=true
    elif try_fw_setenv; then
        success=true
    elif try_extlinux; then
        success=true
    elif try_uenv; then
        success=true
    fi
    
    # Always store the preference
    store_boot_slot
    
    if [ "$success" = "true" ]; then
        log "Boot slot switch to $SLOT completed successfully"
        sync
        exit 0
    else
        log "WARNING: Could not update bootloader config, stored preference only"
        log "Boot may require manual intervention"
        exit 1
    fi
}

main "$@"
