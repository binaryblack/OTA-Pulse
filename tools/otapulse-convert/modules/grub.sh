#!/usr/bin/env bash
# Copyright 2024 Northern.tech AS
#
#    Licensed under the Apache License, Version 2.0 (the "License");
#    you may not use this file except in compliance with the License.
#    You may obtain a copy of the License at
#
#        http://www.apache.org/licenses/LICENSE-2.0
#
#    Unless required by applicable law or agreed to in writing, software
#    distributed under the License is distributed on an "AS IS" BASIS,
#    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#    See the License for the specific language governing permissions and
#    limitations under the License.

source modules/chroot.sh

# grub_create_grub_config
#
#
function grub_create_grub_config() {

    # The bootloader environment scripts are part of the tool, never downloaded.
    if [ ! -f "${OTAPULSE_GRUBENV_DIR}/Makefile" ]; then
        log_fatal "The bootloader environment scripts are missing: ${OTAPULSE_GRUBENV_DIR}/Makefile not found. This tool was not built completely."
    fi
    run_and_log_cmd "rm -rf work/grubenv"
    run_and_log_cmd "cp -a '${OTAPULSE_GRUBENV_DIR}' work/grubenv"

    cat <<- EOF > work/grubenv/mender_grubenv_defines
mender_rootfsa_part=${OTAPULSE_ROOTFS_PART_A_NUMBER}
mender_rootfsb_part=${OTAPULSE_ROOTFS_PART_B_NUMBER}
kernel_imagetype=kernel
initrd_imagetype=initrd
EOF

    # For partuuid support grub.cfg expects dedicated variables to be added
    if [ "${OTAPULSE_ENABLE_PARTUUID}" == "y" ]; then
        boot_partuuid=$(disk_get_partuuid_from_device "${boot_part_device}")
        rootfsa_partuuid=$(disk_get_partuuid_from_device "${root_part_a_device}")
        rootfsb_partuuid=$(disk_get_partuuid_from_device "${root_part_b_device}")
        log_info "Using boot partition partuuid in grubenv: $boot_partuuid"
        log_info "Using root partition A partuuid in grubenv: $rootfsa_partuuid"
        log_info "Using root partition B partuuid in grubenv: $rootfsb_partuuid"
        cat <<- EOF >> work/grubenv/mender_grubenv_defines
mender_boot_uuid=${boot_partuuid}
mender_rootfsa_uuid=${rootfsa_partuuid}
mender_rootfsb_uuid=${rootfsb_partuuid}
EOF
    else
        cat <<- EOF >> work/grubenv/mender_grubenv_defines
mender_kernel_root_base=${OTAPULSE_STORAGE_DEVICE_BASE}
EOF
    fi
}

# grub_install_standalone_grub_config
#
#
function grub_install_standalone_grub_config() {
    if [ -n "${OTAPULSE_GRUB_KERNEL_BOOT_ARGS}" ]; then
        cat <<- EOF > work/grubenv/11_bootargs_grub.cfg
set bootargs="${OTAPULSE_GRUB_KERNEL_BOOT_ARGS}"
EOF
    fi

    # Kernel panic -> reboot after N seconds, so that the boot counter can revert a failed update.
    # Written last (name order); the kernel honours the last panic= it sees.
    printf 'set bootargs="${bootargs} panic=%s"\n' "${OTAPULSE_PANIC_TIMEOUT_S}" \
        > work/grubenv/12_otapulse_panic_bootargs_grub.cfg

    (   
        cd work/grubenv
        run_and_log_cmd "make 2>&1"
        run_and_log_cmd "sudo make DESTDIR=$PWD/../ BOOT_DIR=boot install-standalone-boot-files"
        run_and_log_cmd "sudo make DESTDIR=$PWD/../rootfs install-tools"
    )

}

# grub_install_grub_d_config
#
#
function grub_install_grub_d_config() {
    grub_install_cmdline_dropin

    # When using grub.d integration, /boot/efi must point to the boot partition,
    # and /boot/grub must point to grub-mender-grubenv on the boot partition.
    if [ ! -d work/rootfs/boot/efi ]; then
        run_and_log_cmd "sudo mkdir work/rootfs/boot/efi"
    fi
    run_and_log_cmd "sudo mkdir work/boot/grub-mender-grubenv"
    if [ -e work/rootfs/boot/grub ]; then
        # Move this to the EFI partition.
        run_and_log_cmd "sudo mv work/rootfs/boot/grub/* work/boot/grub-mender-grubenv/"
        run_and_log_cmd "sudo rmdir work/rootfs/boot/grub"
    fi
    run_and_log_cmd "sudo ln -s efi/grub-mender-grubenv work/rootfs/boot/grub"

    (   
        cd work/grubenv
        run_and_log_cmd "make 2>&1"
        run_and_log_cmd "sudo make DESTDIR=$PWD/../ BOOT_DIR=boot install-boot-env"
        run_and_log_cmd "sudo make DESTDIR=$PWD/../rootfs install-grub.d-boot-scripts"
        run_and_log_cmd "sudo make DESTDIR=$PWD/../rootfs install-tools"
        # We need this for running the scripts once.
        run_and_log_cmd "sudo make DESTDIR=$PWD/../rootfs install-offline-files"
    )

    run_with_chroot_setup work/rootfs grub_install_in_chroot

    (   
        cd work/grubenv
        # Should be removed after running.
        run_and_log_cmd "sudo make DESTDIR=$PWD/../rootfs uninstall-offline-files"
    )
}

# grub_install_cmdline_dropin
#
# Kernel command line for the GRUB integration: a drop-in for the image's own GRUB configuration
# (read by update-grub), so that every menu entry it generates carries OTAPULSE_GRUB_KERNEL_BOOT_ARGS
# and, last, panic=N.
function grub_install_cmdline_dropin() {
    local extra="${OTAPULSE_GRUB_KERNEL_BOOT_ARGS}"
    if [[ ! "${extra}" =~ ^[A-Za-z0-9._:,=/@+%\ -]*$ ]]; then
        log_fatal "OTAPULSE_GRUB_KERNEL_BOOT_ARGS contains characters that are not allowed."
    fi
    run_and_log_cmd "sudo mkdir -p work/rootfs/etc/default/grub.d"
    printf 'GRUB_CMDLINE_LINUX="${GRUB_CMDLINE_LINUX:+${GRUB_CMDLINE_LINUX} }%spanic=%s"\n' \
        "${extra:+${extra} }" "${OTAPULSE_PANIC_TIMEOUT_S}" > work/otapulse-cmdline.cfg
    run_and_log_cmd "sudo install -m 644 work/otapulse-cmdline.cfg work/rootfs/etc/default/grub.d/90-otapulse-cmdline.cfg"
}

function grub_install_in_chroot() {
    # Use `--no-nvram`, since we cannot update firmware memory in an offline
    # build. Instead, use `--removable`, which creates entries that automate
    # booting if you put the image into a new device, which you almost certainly
    # will after using otapulse-convert.
    local -r target_name=$(probe_grub_install_target)
    run_in_chroot_and_log_cmd work/rootfs "grub-install --target=${target_name} --removable --no-nvram"
    run_in_chroot_and_log_cmd work/rootfs "grub-install --target=${target_name} --no-nvram"
    run_in_chroot_and_log_cmd work/rootfs "update-grub"
}

# grub_install_grub_editenv_binary
#
# Install the editenv binary
function grub_install_grub_editenv_binary() {
    log_info "Checking the GRUB editenv binary"

    # The image's own GRUB environment tool is used when it has one (every Debian and Ubuntu
    # system with grub-common does). Only an image without it needs the prebuilt binary of the
    # assets.
    for candidate in work/rootfs/usr/bin/grub-editenv work/rootfs/bin/grub-editenv; do
        if [ -x "${candidate}" ]; then
            log_info "Using the GRUB environment tool of the image: ${candidate#work/rootfs}"
            return
        fi
    done

    arch=$(probe_arch)
    asset=$(otapulse_asset_path "grub-editenv-${arch}")
    run_and_log_cmd "sudo install -m 751 ${asset} work/rootfs/usr/bin/grub-editenv"
}

# grub_install_standalone_grub
#
# Install a standalone GRUB EFI binary on the converted boot partition
function grub_install_standalone_grub() {
    kernel_imagetype=${OTAPULSE_GRUB_KERNEL_IMAGETYPE:-$(probe_kernel_in_boot_and_root)}
    initrd_imagetype=${OTAPULSE_GRUB_INITRD_IMAGETYPE:-$(probe_initrd_in_boot_and_root)}

    run_and_log_cmd "sudo ln -s ${kernel_imagetype} work/rootfs/boot/kernel"
    if [ "${initrd_imagetype}" != "" ]; then
        run_and_log_cmd "sudo ln -sf ${initrd_imagetype} work/rootfs/boot/initrd"
    fi

    # Remove conflicting boot files. These files do not necessarily effect the
    # functionality, but lets get rid of them to avoid confusion.
    #
    # There is no OTA-Pulse integration for EFI boot or systemd-boot.
    sudo rm -rf work/boot/loader work/rootfs/boot/loader
    sudo rm -rf work/boot/EFI/Linux
    sudo rm -rf work/boot/EFI/systemd
    sudo rm -rf work/boot/NvVars
    for empty_dir in $(
                     cd work/boot && find . -maxdepth 1 -type d -empty -not -name .
    ); do
        sudo rmdir work/boot/$empty_dir
    done

    log_info "Installing GRUB..."

    log_info "Installing the GRUB environment tool"
    grub_install_grub_editenv_binary

    local -r arch=$(probe_arch)
    local -r efi_name=$(probe_grub_efi_name)
    local -r efi_target_name=$(probe_grub_efi_target_name)

    log_info "GRUB EFI: ${efi_target_name}"

    efi_asset=$(otapulse_asset_path "${efi_name}")
    run_and_log_cmd "sudo mkdir -p work/boot/EFI/BOOT"
    run_and_log_cmd "sudo cp ${efi_asset} work/boot/EFI/BOOT/${efi_target_name}"

    # Copy dtb directory to the boot partition for use by the bootloader.
    if [ -d work/rootfs/boot/dtbs ]; then
        # Look for the first directory that has dtb files. First check the base
        # folder, then any subfolders in versioned order.
        for candidate in work/rootfs/boot/dtbs $(find work/rootfs/boot/dtbs/ -maxdepth 1 -type d | sort -V -r); do
            if [ $(find $candidate -maxdepth 1 -name '*.dtb' | wc -l) -gt 0 ]; then
                run_and_log_cmd "sudo cp -r $candidate work/boot/dtb"
                break
            fi
        done
    elif [ -d work/rootfs/boot/dtb ]; then
        # For armbian, dietpi and similar
        # Keep the existing dtb directory structure as known by u-boot
        if [ $(find -L work/rootfs/boot/dtb -name '*.dtb' | wc -l) -gt 0 ]; then
            run_and_log_cmd "sudo cp -rL work/rootfs/boot/dtb work/boot/dtb"
        fi
    fi
}
