# SPDX-License-Identifier: Apache-2.0
#
# Checks and additions that make a converted image behave like an OTA-Pulse device image, beyond
# what the soc-ota-agent and socmond packages bring.

# Fail unless the OTA-Pulse packages are installed in the image and in bootloader mode.
#
#  $1 - image root directory
function otapulse_check_packages_installed() {
    local -r root="$1"
    local pkg status

    for pkg in soc-ota-agent socmond; do
        status=$(dpkg-query --admindir="${root}/var/lib/dpkg" -W -f='${db:Status-Status}' "${pkg}" 2>/dev/null || true)
        if [ "${status}" != "installed" ]; then
            log_error "The package ${pkg} is not installed in the image (status: '${status:-unknown}')."
            log_fatal "Put the soc-ota-agent and socmond .deb files for the image architecture into ${OTAPULSE_INSTALL_INPUT_PACKAGES_PATH}."
        fi
    done
    if [ "$(tr -d '[:space:]' < "${root}/etc/otapulse/boot_mode" 2>/dev/null)" != "bootloader" ]; then
        log_fatal "/etc/otapulse/boot_mode of the image is not 'bootloader'. The converted image must run in bootloader compatibility mode."
    fi
    log_info "The soc-ota-agent and socmond packages are installed, bootloader compatibility mode"
}

# Install the helpers and defaults of a converted image.
#
#  $1 - image root directory
function otapulse_install_image_support() {
    local -r root="$1"
    local -r wants="${root}/etc/systemd/system"

    if [ "${OTAPULSE_MACHINE_ID_PERSIST}" = "y" ]; then
        log_info "Keeping the machine-id on the data partition across updates"
        run_and_log_cmd "sudo install -D -m 755 support/otapulse-machine-id ${root}/usr/lib/otapulse/otapulse-machine-id"
        run_and_log_cmd "sudo install -D -m 644 support/otapulse-machine-id.service ${root}/lib/systemd/system/otapulse-machine-id.service"
        run_and_log_cmd "sudo mkdir -p ${wants}/sysinit.target.wants"
        run_and_log_cmd "sudo ln -sf /lib/systemd/system/otapulse-machine-id.service ${wants}/sysinit.target.wants/otapulse-machine-id.service"
    fi

    # The package enables the watchdog like on a device image. See OTAPULSE_SOCMOND_WATCHDOG.
    if [ "${OTAPULSE_SOCMOND_WATCHDOG}" != "y" ]; then
        log_info "Disabling the monitoring watchdog (OTAPULSE_SOCMOND_WATCHDOG is not 'y')"
        run_and_log_cmd "sudo rm -f ${wants}/multi-user.target.wants/socmond-watchdog.service"
    fi
}
