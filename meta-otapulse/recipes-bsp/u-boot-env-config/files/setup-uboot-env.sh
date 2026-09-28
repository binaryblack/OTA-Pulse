#!/bin/sh
# Generic U-Boot Environment Setup Script
# Auto-detects SoC type and configures fw_env.config appropriately
# Supports: Rockchip, NXP i.MX, TI AM series, Allwinner, and others

set -e

# OTAPULSE_FW_ENV_CONFIG / OTAPULSE_DT_BASE are test seams only (see
# ../tests/test-setup-uboot-env.sh); on a device both are unset and the
# defaults below are exactly the paths this script has always used.
FW_ENV_CONFIG="${OTAPULSE_FW_ENV_CONFIG:-/etc/fw_env.config}"
DT_BASE="${OTAPULSE_DT_BASE:-/proc/device-tree}"
BOOT_DEVICE=""
ENV_OFFSET=""
ENV_SIZE="0x8000"  # 32KB default
# Set to 1 by detect_soc_and_offset for SoCs whose boot chain has NO U-Boot
# environment at a raw media offset (BUG-352) - see write_no_env_config.
NO_UBOOT_ENV=0

# Detect boot device
detect_boot_device() {
    # Try to find the boot device from kernel cmdline
    local root_dev=$(cat /proc/cmdline | tr ' ' '\n' | grep "root=" | head -1 | sed 's/root=//')
    
    if echo "$root_dev" | grep -q "PARTUUID"; then
        # Resolve PARTUUID to device
        local partuuid=$(echo "$root_dev" | sed 's/PARTUUID=//')
        root_dev=$(blkid -t PARTUUID="$partuuid" -o device 2>/dev/null | head -1)
    fi
    
    if [ -n "$root_dev" ]; then
        # Get the base device (remove partition number)
        BOOT_DEVICE=$(echo "$root_dev" | sed 's/p[0-9]*$//' | sed 's/[0-9]*$//')
    fi
    
    # Fallback: try common boot devices
    if [ -z "$BOOT_DEVICE" ] || [ ! -b "$BOOT_DEVICE" ]; then
        for dev in /dev/mmcblk0 /dev/mmcblk1 /dev/sda; do
            if [ -b "$dev" ]; then
                BOOT_DEVICE="$dev"
                break
            fi
        done
    fi
    
    echo "Detected boot device: $BOOT_DEVICE"
}

# Detect SoC type and set environment offset
detect_soc_and_offset() {
    local soc_type="unknown"
    local model=""
    
    # Try to detect from device tree
    if [ -f "$DT_BASE/compatible" ]; then
        local compat=$(cat "$DT_BASE/compatible" | tr '\0' '\n' | head -5)
        
        if echo "$compat" | grep -qi "rockchip"; then
            soc_type="rockchip"
        elif echo "$compat" | grep -qi "fsl,imx\|nxp,imx"; then
            soc_type="imx"
        elif echo "$compat" | grep -qi "ti,am\|ti,omap"; then
            soc_type="ti"
        elif echo "$compat" | grep -qi "allwinner"; then
            soc_type="allwinner"
        elif echo "$compat" | grep -qi "amlogic"; then
            soc_type="amlogic"
        elif echo "$compat" | grep -qi "broadcom,bcm\|brcm,bcm"; then
            soc_type="broadcom"
        fi
    fi
    
    # Also check model
    if [ -f "$DT_BASE/model" ]; then
        model=$(cat "$DT_BASE/model" | tr '\0' ' ')
    fi
    
    echo "Detected SoC type: $soc_type"
    echo "Model: $model"
    
    # Set offset based on SoC type
    case "$soc_type" in
        rockchip)
            # Rockchip: U-Boot env typically at 0x3F8000 (4MB - 32KB)
            # or in the uboot partition
            ENV_OFFSET="0x3F8000"
            ENV_SIZE="0x8000"
            ;;
        imx)
            # NXP i.MX: Usually at 0x400000
            ENV_OFFSET="0x400000"
            ENV_SIZE="0x4000"
            ;;
        ti)
            # TI AM series: Usually at 0x260000
            ENV_OFFSET="0x260000"
            ENV_SIZE="0x20000"
            ;;
        allwinner)
            # Allwinner: Usually at 0x88000
            ENV_OFFSET="0x88000"
            ENV_SIZE="0x8000"
            ;;
        amlogic)
            # Amlogic: Usually in dedicated partition
            ENV_OFFSET="0x7400000"
            ENV_SIZE="0x10000"
            ;;
        broadcom)
            # BUG-352: Raspberry Pi / Broadcom VideoCore boards boot straight
            # from the FAT partition (the VideoCore firmware reads
            # cmdline.txt) - there is no U-Boot environment at ANY raw media
            # offset. (Even an RPi built with RPI_USE_U_BOOT keeps its env in
            # a FAT file, uboot.env, never at a raw offset.) This branch used
            # to be missing, so a correctly-detected "broadcom" fell through
            # to the `*)` Rockchip-style default below and fabricated a
            # valid-looking "/dev/mmcblk0 0x3F8000 0x8000" line. That defeated
            # switch-boot-slot.sh's try_fw_setenv safety gate (which refuses
            # only a config with no real device line), let fw_setenv do a
            # blind raw write to the SD card, and reported a false Method-2
            # success that short-circuited the method chain before
            # try_cmdline_txt (Method 8, BUG-348) could run. Never fabricate
            # an offset here: main() writes a device-less config instead,
            # equivalent to the recipe's shipped placeholder.
            NO_UBOOT_ENV=1
            ENV_OFFSET=""
            ENV_SIZE=""
            ;;
        *)
            # Default: Try common offset
            ENV_OFFSET="0x3F8000"
            ENV_SIZE="0x8000"
            ;;
    esac
    
    echo "Environment offset: $ENV_OFFSET, size: $ENV_SIZE"
}

# Generate fw_env.config
generate_config() {
    cat > "$FW_ENV_CONFIG" << EOF
# U-Boot Environment Configuration
# Auto-generated by setup-uboot-env.sh
# Device: $BOOT_DEVICE
# Offset: $ENV_OFFSET
# Size: $ENV_SIZE

$BOOT_DEVICE	$ENV_OFFSET	$ENV_SIZE
EOF
    
    chmod 644 "$FW_ENV_CONFIG"
    echo "Generated $FW_ENV_CONFIG"
}

# BUG-352: SoC has no U-Boot env at a raw offset. Write a config with NO
# device line (equivalent to the recipe's shipped all-commented placeholder),
# so every consumer - switch-boot-slot.sh's try_fw_setenv gate in
# particular - sees "no real U-Boot env on this board" and self-gates out,
# and later installs do not re-run this script. Deliberately does NOT run
# fw_printenv: it SIGSEGVs against a device-less config on RPi4 (see the
# 2026-08-05 note in switch-boot-slot.sh's try_fw_setenv).
write_no_env_config() {
    cat > "$FW_ENV_CONFIG" << EOF
# U-Boot Environment Configuration
# Auto-generated by setup-uboot-env.sh
#
# No U-Boot environment on this SoC (BUG-352): the boot chain reads its
# configuration from the FAT boot partition, not from a raw media offset.
# Intentionally left WITHOUT a device line so fw_setenv/fw_printenv are never
# pointed at arbitrary SD-card bytes. An RPi image built with U-Boot must ship
# its own FAT-file config (e.g. "/boot/uboot.env 0x0000 0x4000") instead.
EOF
    chmod 644 "$FW_ENV_CONFIG"
    echo "No U-Boot environment on this SoC - wrote device-less $FW_ENV_CONFIG (fw_setenv will self-gate out)"
}

# Verify configuration works
verify_config() {
    if command -v fw_printenv >/dev/null 2>&1; then
        echo "Testing fw_printenv..."
        if fw_printenv >/dev/null 2>&1; then
            echo "SUCCESS: U-Boot environment is accessible"
            return 0
        else
            echo "WARNING: fw_printenv failed - environment may be at different offset"
            return 1
        fi
    else
        echo "fw_printenv not available"
        return 1
    fi
}

# Main
main() {
    echo "=== U-Boot Environment Setup ==="

    # SoC first (it only reads the device tree): a SoC with no U-Boot env
    # needs no boot device and must never reach generate_config.
    detect_soc_and_offset
    if [ "$NO_UBOOT_ENV" = "1" ]; then
        write_no_env_config
        echo "=== Setup Complete ==="
        exit 0
    fi

    detect_boot_device
    
    if [ -z "$BOOT_DEVICE" ] || [ ! -b "$BOOT_DEVICE" ]; then
        echo "ERROR: Could not detect boot device"
        exit 1
    fi
    
    generate_config
    verify_config || echo "Manual configuration may be required"
    
    echo "=== Setup Complete ==="
}

main "$@"
