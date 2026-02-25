#!/bin/sh
#
# OTA-Pulse First Boot Partition Setup Script
#
# This script runs on first boot to create the A/B partition layout
# for OTA updates when using dynamic partition mode.
#
# Partition Layout:
#   p1: boot (existing)
#   p2: rootfs_a (existing, currently running)
#   p3: rootfs_b (created by this script)
#   p4: data (created by this script)
#

set -e

MARKER_FILE="/data/.otapulse-firstboot-done"
LOG_TAG="otapulse-firstboot"

log() {
    logger -t "$LOG_TAG" "$1" 2>/dev/null || true
    echo "[otapulse-firstboot] $1" >&2
}

log_error() {
    logger -t "$LOG_TAG" -p err "$1" 2>/dev/null || true
    echo "[otapulse-firstboot] ERROR: $1" >&2
}

# Check if already completed
if [ -f "$MARKER_FILE" ]; then
    log "First boot already completed, skipping"
    exit 0
fi

log "Starting OTA-Pulse first boot partition setup..."

# Detect the boot device
detect_boot_device() {
    # Get the root device from /proc/cmdline
    ROOT_DEV=$(cat /proc/cmdline | grep -o 'root=[^ ]*' | cut -d= -f2-)

    if [ -z "$ROOT_DEV" ]; then
        # Fallback: detect from mounted rootfs
        ROOT_DEV=$(mount | grep ' / ' | cut -d' ' -f1)
    fi

    # Handle PARTUUID
    if echo "$ROOT_DEV" | grep -q "PARTUUID="; then
        PARTUUID=$(echo "$ROOT_DEV" | cut -d= -f2)
        ROOT_DEV=$(blkid -t PARTUUID="$PARTUUID" -o device 2>/dev/null | head -1)
    fi

    # Handle UUID
    if echo "$ROOT_DEV" | grep -q "UUID="; then
        UUID=$(echo "$ROOT_DEV" | cut -d= -f2)
        ROOT_DEV=$(blkid -t UUID="$UUID" -o device 2>/dev/null | head -1)
    fi

    if [ -z "$ROOT_DEV" ] || [ ! -b "$ROOT_DEV" ]; then
        log_error "Cannot detect root device"
        exit 1
    fi

    # Get base device (strip partition number)
    # Handle NVMe and MMC devices (e.g., /dev/mmcblk0p2 -> /dev/mmcblk0)
    if echo "$ROOT_DEV" | grep -q "nvme\|mmcblk"; then
        BASE_DEV=$(echo "$ROOT_DEV" | sed 's/p[0-9]*$//')
        PART_PREFIX="p"
    else
        # Handles /dev/sdaN, /dev/vdaN, etc.
        BASE_DEV=$(echo "$ROOT_DEV" | sed 's/[0-9]*$//')
        PART_PREFIX=""
    fi

    # If ROOT_DEV has no partition number (bare rootfs, e.g., root=/dev/vda),
    # BASE_DEV == ROOT_DEV — the device IS the rootfs, no partition table
    if [ "$BASE_DEV" = "$ROOT_DEV" ]; then
        log "Root device $ROOT_DEV has no partition number (bare rootfs image)"
        log "A/B OTA requires a full disk image with GPT partition table"
        echo "$ROOT_DEV"
        return 0
    fi

    log "Detected boot device: $BASE_DEV"
    echo "$BASE_DEV"
}

# Get current partition info
get_partition_info() {
    DEVICE=$1

    # Get total size in sectors
    TOTAL_SECTORS=$(cat /sys/block/$(basename $DEVICE)/size)
    SECTOR_SIZE=$(cat /sys/block/$(basename $DEVICE)/queue/hw_sector_size 2>/dev/null || echo 512)

    log "Device $DEVICE: $TOTAL_SECTORS sectors, $SECTOR_SIZE bytes/sector"

    # Get last partition end
    LAST_PART_END=$(sgdisk -p "$DEVICE" 2>/dev/null | grep "^ *[0-9]" | tail -1 | awk '{print $3}')
    if [ -z "$LAST_PART_END" ]; then
        log_error "Cannot determine last partition end"
        exit 1
    fi

    log "Last partition ends at sector: $LAST_PART_END"
}

# Calculate partition sizes
calculate_partitions() {
    # Read configured sizes from environment or use defaults
    ROOTFS_SIZE_MB=${OTAPULSE_ROOTFS_SIZE_MB:-1024}
    DATA_SIZE_MB=${OTAPULSE_DATA_SIZE_MB:-512}

    # Convert to sectors
    ROOTFS_SECTORS=$((ROOTFS_SIZE_MB * 1024 * 1024 / SECTOR_SIZE))
    DATA_SECTORS=$((DATA_SIZE_MB * 1024 * 1024 / SECTOR_SIZE))

    # Calculate partition boundaries (align to 1MB = 2048 sectors for 512-byte sectors)
    ALIGN_SECTORS=$((1024 * 1024 / SECTOR_SIZE))

    # rootfs_b starts after last partition (aligned)
    ROOTFS_B_START=$(( (LAST_PART_END + ALIGN_SECTORS) / ALIGN_SECTORS * ALIGN_SECTORS ))
    ROOTFS_B_END=$((ROOTFS_B_START + ROOTFS_SECTORS - 1))

    # data starts after rootfs_b (aligned)
    DATA_START=$(( (ROOTFS_B_END + ALIGN_SECTORS) / ALIGN_SECTORS * ALIGN_SECTORS ))
    DATA_END=$((DATA_START + DATA_SECTORS - 1))

    # Check if we have enough space
    if [ $DATA_END -ge $TOTAL_SECTORS ]; then
        log_error "Not enough space for partitions (need sector $DATA_END, have $TOTAL_SECTORS)"
        exit 1
    fi

    log "Partition plan:"
    log "  rootfs_b: sectors $ROOTFS_B_START - $ROOTFS_B_END (${ROOTFS_SIZE_MB}MB)"
    log "  data: sectors $DATA_START - $DATA_END (${DATA_SIZE_MB}MB)"
}

# Create partitions
create_partitions() {
    DEVICE=$1

    log "Creating rootfs_b partition..."
    sgdisk -n 0:${ROOTFS_B_START}:${ROOTFS_B_END} \
           -t 0:8300 \
           -c 0:"rootfs_b" \
           "$DEVICE"

    log "Creating data partition..."
    sgdisk -n 0:${DATA_START}:${DATA_END} \
           -t 0:8300 \
           -c 0:"data" \
           "$DEVICE"

    # Inform kernel of partition changes
    partprobe "$DEVICE" 2>/dev/null || true
    sleep 2

    # Verify partitions were created
    if ! sgdisk -p "$DEVICE" | grep -q "rootfs_b"; then
        log_error "rootfs_b partition not created"
        exit 1
    fi

    if ! sgdisk -p "$DEVICE" | grep -q "data"; then
        log_error "data partition not created"
        exit 1
    fi

    log "Partitions created successfully"
}

# Format partitions
format_partitions() {
    DEVICE=$1

    # Determine partition names
    if echo "$DEVICE" | grep -q "nvme\|mmcblk"; then
        ROOTFS_B_PART="${DEVICE}p3"
        DATA_PART="${DEVICE}p4"
    else
        ROOTFS_B_PART="${DEVICE}3"
        DATA_PART="${DEVICE}4"
    fi

    # Wait for device nodes
    for i in 1 2 3 4 5; do
        if [ -b "$ROOTFS_B_PART" ] && [ -b "$DATA_PART" ]; then
            break
        fi
        log "Waiting for device nodes... ($i/5)"
        sleep 1
    done

    if [ ! -b "$ROOTFS_B_PART" ]; then
        log_error "rootfs_b partition device not found: $ROOTFS_B_PART"
        exit 1
    fi

    if [ ! -b "$DATA_PART" ]; then
        log_error "data partition device not found: $DATA_PART"
        exit 1
    fi

    log "Formatting rootfs_b partition: $ROOTFS_B_PART"
    mkfs.ext4 -F -L "rootfs_b" "$ROOTFS_B_PART"

    log "Formatting data partition: $DATA_PART"
    mkfs.ext4 -F -L "data" "$DATA_PART"

    log "Partitions formatted successfully"
}

# Setup data partition mount
setup_data_mount() {
    DEVICE=$1

    if echo "$DEVICE" | grep -q "nvme\|mmcblk"; then
        DATA_PART="${DEVICE}p4"
    else
        DATA_PART="${DEVICE}4"
    fi

    # Create mount point
    mkdir -p /data

    # Mount data partition (skip if already mounted via fstab)
    if ! mountpoint -q /data 2>/dev/null; then
        if [ -b "$DATA_PART" ]; then
            mount "$DATA_PART" /data
        else
            log_error "Data partition device not found: $DATA_PART"
            exit 1
        fi
    else
        log "Data partition already mounted at /data"
    fi

    # Create standard directories on data partition
    mkdir -p /data/ota
    mkdir -p /data/etc
    mkdir -p /data/var/lib/otapulse

    # Create marker file
    touch /data/.otapulse-firstboot-done

    # Add fstab entry only if no /data entry already exists.
    # post-build.sh writes a LABEL=data entry; checking for UUID would always
    # miss it, causing a duplicate (non-nofail) entry to be appended on every boot.
    if ! grep -q " /data " /etc/fstab 2>/dev/null; then
        DATA_UUID=$(blkid -s UUID -o value "$DATA_PART" 2>/dev/null || true)
        if [ -n "$DATA_UUID" ]; then
            echo "UUID=$DATA_UUID /data ext4 defaults,noatime,nofail,x-systemd.device-timeout=60 0 2" >> /etc/fstab
        fi
    fi

    log "Data partition mounted at /data"
}

# Initialize boot environment
init_boot_env() {
    # Determine current boot partition
    ROOT_DEV=$(mount | grep ' / ' | cut -d' ' -f1)

    # Try to detect partition number
    PART_NUM=$(echo "$ROOT_DEV" | grep -o '[0-9]*$')

    log "Current root partition: $ROOT_DEV (partition $PART_NUM)"

    # Initialize boot environment files
    mkdir -p /data/ota

    # mender_boot_part: only write if no OTA update is pending.
    # When upgrade_available=1, the agent has already set mender_boot_part to the
    # new (inactive) slot — overwriting it here would cause VerifyReboot() to fail
    # and trigger a rollback, destroying the in-progress update.
    if [ "$(cat /data/ota/upgrade_available 2>/dev/null)" != "1" ]; then
        echo "$PART_NUM" > /data/ota/mender_boot_part
    fi

    # upgrade_available, boot_count, current_slot: write only if the file does
    # not yet exist.  Once the OTA agent sets upgrade_available=1 before a reboot,
    # we must NOT overwrite it — doing so would destroy the in-progress update state.
    if [ ! -f /data/ota/upgrade_available ]; then
        echo "0" > /data/ota/upgrade_available
    fi

    if [ ! -f /data/ota/boot_count ]; then
        echo "0" > /data/ota/boot_count
    fi

    if [ ! -f /data/ota/current_slot ]; then
        case "$PART_NUM" in
            2) echo "a" > /data/ota/current_slot ;;
            3) echo "b" > /data/ota/current_slot ;;
            *) echo "a" > /data/ota/current_slot ;;
        esac
    fi

    log "Boot env: mender_boot_part=$PART_NUM  upgrade_available=$(cat /data/ota/upgrade_available)  current_slot=$(cat /data/ota/current_slot)"

    # If U-Boot environment is available, set it there too
    if command -v fw_setenv >/dev/null 2>&1; then
        if [ -f /etc/fw_env.config ]; then
            fw_setenv mender_boot_part "$PART_NUM" 2>/dev/null || true
            fw_setenv upgrade_available 0 2>/dev/null || true
            fw_setenv bootcount 0 2>/dev/null || true
            log "U-Boot environment initialized"
        fi
    fi
}

# Main execution
main() {
    log "=== OTA-Pulse First Boot Setup ==="

    DEVICE=$(detect_boot_device)

    # Guard: verify device has a GPT partition table
    # Use 'sgdisk -p' (print) not 'sgdisk -v' (verify) — verify is too strict
    # and can fail on genimage-produced images due to backup GPT positioning.
    # If this is a bare rootfs (no GPT), exit gracefully — don't crash to emergency mode
    if ! sgdisk -p "$DEVICE" >/dev/null 2>&1; then
        log_error "Device $DEVICE has no GPT partition table."
        log_error "This image may be a bare rootfs without partition layout."
        log_error "OTA A/B updates require a full disk image with GPT."
        log_error "Skipping firstboot setup — OTA will not function."
        exit 0
    fi

    # Early check: if all partitions already exist (pre-created by genimage/WIC),
    # skip partitioning and just ensure data is mounted and boot env is initialized
    if sgdisk -p "$DEVICE" 2>/dev/null | grep -q "rootfs_b" && \
       sgdisk -p "$DEVICE" 2>/dev/null | grep -q "data"; then
        log "All partitions already exist (rootfs_b and data found), skipping partition creation"

        # Ensure data partition is mounted and boot env initialized
        setup_data_mount "$DEVICE"
        init_boot_env

        log "=== First boot setup completed (pre-partitioned image) ==="
        exit 0
    fi

    get_partition_info "$DEVICE"
    calculate_partitions
    create_partitions "$DEVICE"
    format_partitions "$DEVICE"
    setup_data_mount "$DEVICE"
    init_boot_env

    log "=== First boot setup completed successfully ==="
    log "System is now ready for OTA updates"
}

main "$@"
